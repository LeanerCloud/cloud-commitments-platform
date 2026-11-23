package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultMigrationsTimeout bounds how long ensureDB waits for migrations
// before giving up and proceeding. Set well above the time a normal index
// build / DDL takes (the prior 20s could be blown mid-run by a single index
// build on a growing table, leaving schema_migrations.dirty = true and
// fail-opening every later boot) yet still comfortably under the Lambda
// 300s hard limit, so a slow-but-legitimate migration completes rather than
// being killed inside ensureDB. Override per-environment with
// CUDLY_MIGRATION_TIMEOUT (e.g. "180s") for genuinely long migrations.
const defaultMigrationsTimeout = 120 * time.Second

// resolveMigrationsTimeout reads CUDLY_MIGRATION_TIMEOUT from the environment.
// It is called once in NewApplicationFromDeps to initialize
// Application.migrationsTimeout. Because the timeout lives on the struct
// (not a package-level var), tests can set it on a specific Application
// instance without serializing parallel tests (04-M3).
func resolveMigrationsTimeout() time.Duration {
	v := os.Getenv("CUDLY_MIGRATION_TIMEOUT")
	if v == "" {
		return defaultMigrationsTimeout
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		logging.Warnf("CUDLY_MIGRATION_TIMEOUT invalid (%q); using default %s", v, defaultMigrationsTimeout) // #nosec G706 -- env var value is operator-controlled configuration; logged for diagnostics
		return defaultMigrationsTimeout
	}
	return d
}

// recordMigrationResult stores the outcome of the most recent migration
// attempt. Takes migrationMu briefly. Called from inside ensureDB which
// holds dbMu — lock order is dbMu then migrationMu. Never take them the
// other way around.
func (app *Application) recordMigrationResult(err error) {
	app.migrationMu.Lock()
	defer app.migrationMu.Unlock()
	app.migrationErr = err
	app.migrationFinishedAt = time.Now()
}

// snapshotMigrationState returns a point-in-time copy of the migration
// state suitable for /health rendering.
func (app *Application) snapshotMigrationState() (err error, finishedAt time.Time) { //nolint:revive,staticcheck // error-return/ST1008: named return order is the migrationErr then migrationFinishedAt reading order
	app.migrationMu.Lock()
	defer app.migrationMu.Unlock()
	return app.migrationErr, app.migrationFinishedAt
}

// runMigrationsBounded runs app.runMigrationsFunc in a goroutine bounded by
// app.migrationsTimeout. The returned error is either the runner's own error,
// a panic wrapped as an error, or a timeout error -- never a
// nil-with-goroutine-still-alive. The goroutine is guaranteed to have exited
// before this function returns (the timeout branch waits on <-done after
// canceling the ctx), so no orphan goroutine survives past this call --
// critical on Lambda where goroutines freeze between invocations.
//
// Using instance fields (not package globals) makes it safe to call
// t.Parallel() in tests that override migrationsTimeout or runMigrationsFunc
// on a specific Application -- no shared mutable global state (04-M3).
func (app *Application) runMigrationsBounded(pool *pgxpool.Pool, migrationsPath, adminEmail, adminPassword string) error {
	return runMigrationsBoundedWith(pool, migrationsPath, adminEmail, adminPassword, app.migrationsTimeout, app.runMigrationsFunc)
}

// runMigrationsBoundedWith is the underlying implementation used by the
// Application method and by the package-level tests that pre-date the
// instance-field migration (04-M3). Tests that want to exercise the
// timeout/panic/success paths can call this directly with a fake runner.
func runMigrationsBoundedWith(pool *pgxpool.Pool, migrationsPath, adminEmail, adminPassword string, timeout time.Duration, runner func(ctx context.Context, pool *pgxpool.Pool, migrationsPath, adminEmail, adminPassword string) error) error {
	migCtx, cancelMig := context.WithTimeout(context.Background(), timeout)
	defer cancelMig()

	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("migration panic: %v", r)
			}
		}()
		done <- runner(migCtx, pool, migrationsPath, adminEmail, adminPassword)
	}()

	select {
	case err := <-done:
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("migration timed out after %s: %w", timeout, err)
		}
		return err
	case <-migCtx.Done():
		<-done
		return fmt.Errorf("migration timed out after %s: %w", timeout, migCtx.Err())
	}
}
