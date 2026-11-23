package migrations

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // postgres driver (pgx/v5)
	_ "github.com/golang-migrate/migrate/v4/source/file"     // file source
	"github.com/jackc/pgx/v5/pgxpool"
)

// RunMigrations runs database migrations using golang-migrate
// adminEmail is optional - if provided, admin user will be created after migrations complete
// adminPassword is optional - if provided, admin is created with hashed password and active=true.
//
// ctx bounds the schema work itself: connection setup, advisory-lock wait and
// the running migration SQL. When ctx ends, the migrator's connections are
// canceled server-side and RunMigrations returns an error matching
// errors.Is(err, ctx.Err()). An interrupted migration file rolls back as a
// whole (PostgreSQL runs each file as one implicit transaction; see
// TestMigrationsAreTransactional) but leaves schema_migrations dirty, which
// the next run reports through maybeRefuseDirty without clearing it.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool, migrationsPath, adminEmail, adminPassword string) error {
	tracker := &connTracker{ctx: ctx}
	stopWatch := tracker.watch()
	defer stopWatch()

	return wrapContextError(ctx, runMigrations(ctx, tracker, pool, migrationsPath, adminEmail, adminPassword))
}

func runMigrations(ctx context.Context, tracker *connTracker, pool *pgxpool.Pool, migrationsPath, adminEmail, adminPassword string) error {
	// Create the migrator and run the pre-Up recovery hooks (operator force,
	// then the default-on dirty check). Kept in a helper so RunMigrations stays
	// under the cyclomatic-complexity budget as recovery paths grow.
	m, err := newMigratorWithRecovery(tracker, pool, migrationsPath)
	if err != nil {
		return err
	}
	defer m.Close()

	// Run migrations
	err = m.Up()
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	// Get current version
	version, dirty, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("failed to get migration version: %w", err)
	}

	if dirty {
		return fmt.Errorf("database is in dirty state at version %d", version)
	}

	logging.Infof("Database migrations completed successfully (version: %d)", version)

	// Create admin user if email provided (after migrations complete).
	// Admin-bootstrap failure is non-fatal: the schema migration step
	// succeeded and the app must not be prevented from starting because
	// of a bootstrap-only error (e.g. a stale migrate.go referencing a
	// dropped column). The failure is logged clearly so operators can
	// distinguish it from a migration-step failure (issue #945).
	if adminEmail != "" {
		if err := ensureAdminUser(ctx, pool, adminEmail, adminPassword); err != nil {
			logging.Warnf("admin bootstrap failed (schema migration completed successfully): %v", err)
		}
	}

	return nil
}

// newMigratorWithRecovery builds the golang-migrate migrator for migrationsPath
// and runs the pre-Up recovery hooks in order: the one-shot operator force
// (CUDLY_FORCE_MIGRATION_VERSION) first, then the default-on dirty check
// (maybeRefuseDirty, which fails loud and never clears the flag).
// On success it returns a migrator the caller must Close(); on any error it
// closes the migrator itself and returns the error so the caller never sees a
// half-initialized migrator.
//
// Ordering note: maybeRefuseDirty runs AFTER maybeForceMigrationVersion so an
// explicit force always takes precedence (it pins+cleans first, leaving nothing
// dirty for the check to report). The dirty-check error propagates so it is
// recorded with its recovery instructions (and the app fail-opens in ensureDB)
// instead of surfacing as golang-migrate's bare ErrDirty from Up().
func newMigratorWithRecovery(tracker *connTracker, pool *pgxpool.Pool, migrationsPath string) (*migrate.Migrate, error) {
	// Get database connection string from pool config (without admin email parameter - RDS Proxy doesn't support options)
	dsn := buildMigrateDSN(pool.Config())

	m, err := newCancelableMigrator(tracker, migrationsPath, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to create migrator: %w", err)
	}

	// One-shot operator recovery: if CUDLY_FORCE_MIGRATION_VERSION is set,
	// call Force(N) before Up(). Clears the dirty flag and pins state to
	// the given version. Used to recover from a dirty schema_migrations row
	// without direct DB access. Remove the env var after the next
	// successful deploy.
	if err := maybeForceMigrationVersion(m); err != nil {
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			logging.Errorf("migrate: close after force-version error: source=%v db=%v", srcErr, dbErr)
		}
		return nil, err
	}

	// Default-on dirty check: when the schema_migrations row is dirty, return
	// an error with recovery instructions and leave the flag set. It does NOT
	// clear the flag: whether the dirty migration's effects landed is unknown,
	// so recovery stays a manual CUDLY_FORCE_MIGRATION_VERSION decision.
	if err := maybeRefuseDirty(m); err != nil {
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			logging.Errorf("migrate: close after dirty-check error: source=%v db=%v", srcErr, dbErr)
		}
		return nil, err
	}

	return m, nil
}

// RollbackMigrations rolls back N migrations.
func RollbackMigrations(ctx context.Context, pool *pgxpool.Pool, migrationsPath string, steps int) error {
	if steps <= 0 {
		return fmt.Errorf("rollback steps must be positive, got %d", steps)
	}
	const maxRollbackSteps = 10
	if steps > maxRollbackSteps {
		return fmt.Errorf("refusing to rollback more than %d migrations at once (requested %d); use multiple calls for safety", maxRollbackSteps, steps)
	}

	dsn := buildMigrateDSN(pool.Config())

	m, err := migrate.New(
		fmt.Sprintf("file://%s", migrationsPath),
		dsn,
	)
	if err != nil {
		return fmt.Errorf("failed to create migrator: %w", err)
	}
	defer m.Close()

	logging.Infof("Rolling back %d migration(s)...", steps)

	// Rollback steps
	err = m.Steps(-steps)
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("failed to rollback migrations: %w", err)
	}

	version, dirty, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("failed to get migration version: %w", err)
	}

	if dirty {
		return fmt.Errorf("database is in dirty state at version %d", version)
	}

	logging.Infof("Rolled back %d migration(s) (current version: %d)", steps, version)
	return nil
}

// MigrateToVersion migrates the schema up or down to exactly the given
// version. Unlike RunMigrations it applies no post-migration Go logic
// (admin seeding etc.), and unlike RollbackMigrations it targets a version
// rather than a step count. Migration tests use it to pin the database at
// the version just below the migration under test; fixed step counts from
// head silently drift every time a newer migration lands.
func MigrateToVersion(ctx context.Context, pool *pgxpool.Pool, migrationsPath string, version uint) error {
	dsn := buildMigrateDSN(pool.Config())

	m, err := migrate.New(
		fmt.Sprintf("file://%s", migrationsPath),
		dsn,
	)
	if err != nil {
		return fmt.Errorf("failed to create migrator: %w", err)
	}
	defer m.Close()

	err = m.Migrate(version)
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("failed to migrate to version %d: %w", version, err)
	}

	current, dirty, err := m.Version()
	if err != nil {
		return fmt.Errorf("failed to get migration version: %w", err)
	}
	if dirty {
		return fmt.Errorf("database is in dirty state at version %d", current)
	}
	if current != version {
		return fmt.Errorf("expected migration version %d, got %d", version, current)
	}

	logging.Infof("Migrated to version %d", current)
	return nil
}

// GetMigrationVersion returns the current migration version. ctx bounds the
// advisory-lock wait and the version query like RunMigrations: when it ends the
// migrator's connections are canceled server-side and the error matches
// errors.Is(err, ctx.Err()).
func GetMigrationVersion(ctx context.Context, pool *pgxpool.Pool, migrationsPath string) (version uint, dirty bool, err error) {
	tracker := &connTracker{ctx: ctx}
	stopWatch := tracker.watch()
	defer stopWatch()

	version, dirty, err = getMigrationVersion(tracker, pool, migrationsPath)
	return version, dirty, wrapContextError(ctx, err)
}

func getMigrationVersion(tracker *connTracker, pool *pgxpool.Pool, migrationsPath string) (uint, bool, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	m, err := newCancelableMigrator(tracker, migrationsPath, buildMigrateDSN(pool.Config()))
	if err != nil {
		return 0, false, fmt.Errorf("failed to create migrator: %w", err)
	}
	defer m.Close()

	version, dirty, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, fmt.Errorf("failed to get migration version: %w", err)
	}

	return version, dirty, nil
}

// migrateURLScheme selects which golang-migrate database driver handles the
// DSN. golang-migrate dispatches purely on the URL scheme, and the pgx/v5
// driver registers itself as "pgx5" only - "postgres"/"postgresql" belong to
// the lib/pq-backed driver this package deliberately does not import (issue
// #1849). A mismatch between this constant and the imported driver fails at
// migration time, not at compile time, which is why
// TestBuildMigrateDSN_SchemeMatchesRegisteredDriver pins the two together.
const migrateURLScheme = "pgx5"

// buildMigrateDSN builds a connection string for golang-migrate from pgx config.
// The SSL mode is recovered from the parsed TLSConfig so strict modes
// (verify-ca / verify-full) are preserved rather than silently downgraded to
// require. See sslModeFromTLSConfig for the exact mapping.
func buildMigrateDSN(config *pgxpool.Config) string {
	cc := config.ConnConfig

	// Don't add connection options - RDS Proxy doesn't support them
	u := url.URL{
		Scheme:   migrateURLScheme,
		User:     url.UserPassword(cc.User, cc.Password),
		Host:     net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))),
		Path:     "/" + cc.Database,
		RawQuery: url.Values{"sslmode": {sslModeFromTLSConfig(cc.TLSConfig)}}.Encode(),
	}
	return u.String()
}

// sslModeFromTLSConfig recovers the libpq sslmode string that pgx derived from
// the original DSN, so the migration DSN matches the security policy the
// application enforces in normal operation. pgx (v5) maps sslmode -> *tls.Config
// as follows, which we invert here:
//
//   - disable      -> nil
//   - require       -> InsecureSkipVerify=true, no custom chain/host verification
//   - verify-ca     -> InsecureSkipVerify=true + a VerifyPeerCertificate callback
//     (verifies the chain but not the hostname)
//   - verify-full   -> InsecureSkipVerify=false + ServerName set
//     (verifies both chain and hostname)
//
// Collapsing every TLS-enabled mode to "require" would silently drop the
// certificate-validation guarantees of verify-ca / verify-full during
// migrations, so we distinguish them explicitly.
func sslModeFromTLSConfig(tlsConfig *tls.Config) string {
	if tlsConfig == nil {
		return "disable"
	}
	// verify-full is the only mode where pgx leaves InsecureSkipVerify=false:
	// the standard library then verifies both the chain and the hostname.
	if !tlsConfig.InsecureSkipVerify {
		return "verify-full"
	}
	// verify-ca skips the standard hostname check but installs a custom
	// VerifyPeerCertificate callback to validate the chain; plain require
	// installs neither.
	if tlsConfig.VerifyPeerCertificate != nil {
		return "verify-ca"
	}
	return "require"
}

// ValidateMigrationsPath checks if migrations directory exists.
func ValidateMigrationsPath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("migrations directory does not exist: %s", path)
		}
		return fmt.Errorf("failed to check migrations directory: %w", err)
	}

	if !info.IsDir() {
		return fmt.Errorf("migrations path is not a directory: %s", path)
	}

	return nil
}
