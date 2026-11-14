package testhelpers

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// schemaSeq makes schema names unique within one test binary, so two
// subtests with the same sanitized name never collide.
var schemaSeq atomic.Int64

var nonIdentChars = regexp.MustCompile(`[^a-z0-9]+`)

// extensionsLockID serializes the one-off extension setup across tests.
const extensionsLockID = 338338

// maxSchemaPrefix keeps the whole name under PostgreSQL's 63-byte identifier
// limit with room for "t_" and the "_<pid>_<seq>" suffix.
const maxSchemaPrefix = 40

// NewIsolatedSchema creates a schema of its own on the database c points at
// and returns a pool whose search_path is that schema, so tests can run in
// parallel on one database without seeing each other's rows. The schema is
// named from t.Name() plus a unique suffix and dropped, with the pool closed,
// in t.Cleanup.
//
// Apply migrations with the same call RequireMigratedDatabase uses, passing
// the returned pool: migrations.RunMigrations(ctx, pool, path, "", ""). The
// migrator creates its tables and schema_migrations in the first schema on
// search_path, which is the isolated one.
//
// "public" stays second on search_path because the migrations install the
// uuid-ossp and pg_trgm extensions. Left to the migrations they would land in
// the first test's schema and vanish with it while other schemas still use
// them, so they are created once in public here.
func NewIsolatedSchema(ctx context.Context, t *testing.T, c *PostgresContainer) *pgxpool.Pool {
	t.Helper()
	require.NotNil(t, c, "NewIsolatedSchema needs a container")
	require.NotNil(t, c.Config, "NewIsolatedSchema needs a container config")

	poolCfg, err := pgxpool.ParseConfig(c.Config.DSN(""))
	require.NoError(t, err, "parse DSN for isolated schema")
	poolCfg.MaxConns = int32(max(1, min(c.Config.MaxConnections, 10))) // #nosec G115 -- clamped to 1..10

	admin, err := pgxpool.NewWithConfig(ctx, poolCfg.Copy())
	require.NoError(t, err, "connect admin pool for isolated schema")
	defer admin.Close()

	require.NoError(t, ensureExtensions(ctx, admin), "create shared extensions")

	name := isolatedSchemaName(t.Name())
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{name}.Sanitize())
	require.NoError(t, err, "create schema %s", name)
	// Registered before the pool, so the pool closes first (LIFO) and the drop
	// is not blocked by its connections.
	t.Cleanup(func() { dropSchema(t, poolCfg, name) })

	poolCfg.ConnConfig.RuntimeParams["search_path"] = pgx.Identifier{name}.Sanitize() + ", public"
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	require.NoError(t, err, "connect to isolated schema %s", name)
	t.Cleanup(pool.Close)

	return pool
}

// isolatedSchemaName turns a test name into a lowercase identifier with a
// unique suffix: t_<sanitized>_<seq>_<nanos>.
func isolatedSchemaName(testName string) string {
	base := strings.Trim(nonIdentChars.ReplaceAllString(strings.ToLower(testName), "_"), "_")
	if len(base) > maxSchemaPrefix {
		base = strings.TrimRight(base[:maxSchemaPrefix], "_")
	}
	return fmt.Sprintf("t_%s_%d_%d", base, schemaSeq.Add(1), time.Now().UnixNano()%1_000_000)
}

// ensureExtensions installs the extensions the migrations need into public,
// under an advisory lock because concurrent CREATE EXTENSION IF NOT EXISTS
// can still fail on the catalog unique index.
func ensureExtensions(ctx context.Context, pool *pgxpool.Pool) (err error) {
	conn, aerr := pool.Acquire(ctx)
	if aerr != nil {
		return aerr
	}
	defer conn.Release()

	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock($1)", extensionsLockID); err != nil {
		return err
	}
	// The lock is session-level, so releasing the connection to the pool must
	// not leave it held.
	defer func() {
		if _, uerr := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", extensionsLockID); uerr != nil && err == nil {
			err = fmt.Errorf("release extension lock: %w", uerr)
		}
	}()

	for _, ext := range []string{"uuid-ossp", "pg_trgm"} {
		if _, err = conn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS "`+ext+`" SCHEMA public`); err != nil {
			return fmt.Errorf("create extension %s: %w", ext, err)
		}
	}
	return nil
}

// dropSchema removes the schema on a fresh connection, detached from the
// test's context, which is already canceled when Cleanup runs.
func dropSchema(t *testing.T, cfg *pgxpool.Config, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Errorf("connect to drop schema %s: %v", name, err)
		return
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{name}.Sanitize()+" CASCADE"); err != nil {
		t.Errorf("drop schema %s: %v", name, err)
	}
}
