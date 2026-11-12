package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/secrets"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultMigrateTimeout = 15 * time.Minute
	// migrateDeadlineMargin is kept free of the caller's deadline (Lambda) so a
	// slow migration ends with a clear error instead of a platform kill.
	migrateDeadlineMargin = 20 * time.Second
	minMigrateBudget      = 30 * time.Second
	forceVersionEnv       = "CUDLY_FORCE_MIGRATION_VERSION"
)

// MigrateOptions tunes one deploy-time migration run.
type MigrateOptions struct {
	// ForceVersion clears a dirty schema_migrations row at this version. It is
	// accepted only for the dirty version N or the highest version below N
	// that has a migration file (see validateForceVersion).
	ForceVersion *int
	// Timeout bounds the run; zero means defaultMigrateTimeout.
	Timeout time.Duration
}

// MigrateReport is the schema version before and after a run.
type MigrateReport struct {
	From int
	To   int
}

// migrateDeps lets unit tests replace the database-facing steps.
type migrateDeps struct {
	connect     func(ctx context.Context, cfg *database.Config, r secrets.Resolver) (*pgxpool.Pool, func(), error)
	readVersion func(ctx context.Context, pool *pgxpool.Pool, path string) (uint, bool, error)
	run         func(ctx context.Context, pool *pgxpool.Pool, path, adminEmail, adminPassword string) error
}

var realMigrateDeps = migrateDeps{
	connect: func(ctx context.Context, cfg *database.Config, r secrets.Resolver) (*pgxpool.Pool, func(), error) {
		conn, err := database.NewConnection(ctx, cfg, r)
		if err != nil {
			return nil, nil, err
		}
		return conn.Pool(), conn.Close, nil
	},
	readVersion: migrations.GetMigrationVersion,
	run:         migrations.RunMigrations,
}

// InitConfigStore exposes initConfigStore so cmd/server can run the migrator
// without building a full Application.
func InitConfigStore(ctx context.Context) (*database.Config, secrets.Resolver, error) {
	return initConfigStore(ctx)
}

// RunDeployMigrations applies pending migrations once and reports the version
// range. It is the deploy-time runner used by `cudly --migrate` and the Lambda
// migrate event; nothing here auto-heals a dirty row.
func RunDeployMigrations(ctx context.Context, dbCfg *database.Config, resolver secrets.Resolver, opts MigrateOptions) (MigrateReport, error) {
	return runDeployMigrations(ctx, dbCfg, resolver, opts, realMigrateDeps)
}

func runDeployMigrations(ctx context.Context, dbCfg *database.Config, resolver secrets.Resolver, opts MigrateOptions, deps migrateDeps) (MigrateReport, error) {
	if dbCfg == nil {
		return MigrateReport{}, errors.New("database is not configured: cannot run migrations")
	}
	// RunMigrations would force this value unvalidated, bypassing the guard.
	if v := os.Getenv(forceVersionEnv); v != "" && opts.ForceVersion == nil {
		return MigrateReport{}, fmt.Errorf("%s=%s is set in the environment but no force_version was given: remove the env var or pass force_version so the value is validated", forceVersionEnv, v)
	}
	timeout, err := migrateBudget(ctx, opts.Timeout)
	if err != nil {
		return MigrateReport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pool, closePool, err := deps.connect(ctx, dbCfg, resolver)
	if err != nil {
		return MigrateReport{}, fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}
	defer closePool()
	return migrateOnPool(ctx, pool, dbCfg, resolver, opts, deps)
}

func migrateOnPool(ctx context.Context, pool *pgxpool.Pool, dbCfg *database.Config, resolver secrets.Resolver, opts MigrateOptions, deps migrateDeps) (MigrateReport, error) {
	adminEmail := os.Getenv("ADMIN_EMAIL")
	adminPassword, err := migrateAdminPassword(ctx, resolver, adminEmail)
	if err != nil {
		return MigrateReport{}, err
	}
	from, dirty, err := deps.readVersion(ctx, pool, dbCfg.MigrationsPath)
	if err != nil {
		return MigrateReport{}, err
	}
	if opts.ForceVersion != nil {
		if verr := validateForceVersion(*opts.ForceVersion, from, dirty, dbCfg.MigrationsPath); verr != nil {
			return MigrateReport{}, verr
		}
		restore := setForceVersionEnv(*opts.ForceVersion)
		defer restore()
	}
	if rerr := deps.run(ctx, pool, dbCfg.MigrationsPath, adminEmail, adminPassword); rerr != nil {
		return MigrateReport{}, rerr
	}
	to, _, err := deps.readVersion(ctx, pool, dbCfg.MigrationsPath)
	if err != nil {
		return MigrateReport{}, err
	}
	fromInt, err := versionToInt(from)
	if err != nil {
		return MigrateReport{}, err
	}
	toInt, err := versionToInt(to)
	if err != nil {
		return MigrateReport{}, err
	}
	return MigrateReport{From: fromInt, To: toInt}, nil
}

// migrateAdminPassword resolves the password only when an admin user will be
// created: RunMigrations ignores it when adminEmail is empty.
func migrateAdminPassword(ctx context.Context, resolver secrets.Resolver, adminEmail string) (string, error) {
	if adminEmail == "" {
		return "", nil
	}
	return resolveAdminPassword(ctx, resolver)
}

// timeUntil is a seam so tests can pin the exact-minimum boundary.
var timeUntil = time.Until

// migrateBudget is min(requested, ctx deadline - margin). A cancel mid-DDL
// still leaves dirty=true, so the margin buys a clear error, not a clean row.
func migrateBudget(ctx context.Context, requested time.Duration) (time.Duration, error) {
	if requested <= 0 {
		requested = defaultMigrateTimeout
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return requested, nil
	}
	left := timeUntil(deadline) - migrateDeadlineMargin
	if left < minMigrateBudget {
		return 0, fmt.Errorf("only %s left before the caller deadline; need at least %s plus a %s margin to migrate",
			timeUntil(deadline).Round(time.Second), minMigrateBudget, migrateDeadlineMargin)
	}
	return min(requested, left), nil
}

// validateForceVersion allows forcing only a dirty row, and only to the dirty
// version (its SQL landed) or the highest version below it that has a file in
// path (it rolled back; numbering has gaps, so that is not always N-1).
// Anything else would mark unapplied migrations as applied.
func validateForceVersion(n int, version uint, dirty bool, path string) error {
	if !dirty {
		return fmt.Errorf("refusing force_version %d: schema_migrations is not dirty (version %d)", n, version)
	}
	v, err := versionToInt(version)
	if err != nil {
		return err
	}
	lower, err := previousMigrationVersion(path, v)
	if err != nil {
		return err
	}
	if n != v && n != lower {
		return fmt.Errorf("refusing force_version %d: dirty version is %d, only %d or %d are allowed", n, v, v, lower)
	}
	return nil
}

// previousMigrationVersion is the highest <version>_*.up.sql version below v in
// path, or 0 when there is none.
func previousMigrationVersion(path string, v int) (int, error) {
	files, err := filepath.Glob(filepath.Join(path, "*.up.sql"))
	if err != nil {
		return 0, fmt.Errorf("failed to list migrations in %q: %w", path, err)
	}
	best := 0
	for _, f := range files {
		prefix, _, ok := strings.Cut(filepath.Base(f), "_")
		n, perr := strconv.Atoi(prefix)
		if ok && perr == nil && n < v && n > best {
			best = n
		}
	}
	return best, nil
}

func versionToInt(v uint) (int, error) {
	if v > math.MaxInt32 {
		return 0, fmt.Errorf("migration version %d is out of range", v)
	}
	return int(v), nil
}

// setForceVersionEnv feeds migrations.RunMigrations, which reads the force
// version from the process env, and returns a func restoring the prior value.
// Safe only with one runner per process: a Lambda instance serves one
// invocation at a time and --migrate is its own process.
func setForceVersionEnv(n int) func() {
	prev, had := os.LookupEnv(forceVersionEnv)
	if err := os.Setenv(forceVersionEnv, strconv.Itoa(n)); err != nil {
		log.Printf("failed to set %s: %v", forceVersionEnv, err)
	}
	return func() {
		if had {
			_ = os.Setenv(forceVersionEnv, prev)
		} else {
			_ = os.Unsetenv(forceVersionEnv)
		}
	}
}
