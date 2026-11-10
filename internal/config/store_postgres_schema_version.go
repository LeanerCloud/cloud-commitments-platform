package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// settlementMigrationVersion is the migration that adds the outcome-settlement
// columns to purchase_executions (issue #999).
const settlementMigrationVersion = 155

// directResolveMigrationVersion is the migration that adds the resolution
// columns to ri_exchange_direct_executions (#1836). It mirrors
// directResolutionMigration in internal/database/postgres/migrations.
const directResolveMigrationVersion = 163

// readoptMigrationVersion is the migration that adds
// purchase_executions.approval_readopted_at (#1785). It mirrors the file
// 000164_purchase_executions_approval_readopted_at in internal/database/postgres/migrations.
const readoptMigrationVersion = 164

// sweepCeilingMigrationVersion is the migration that adds
// ladder_configs.max_hourly_commit_per_sweep (#70). It mirrors the file
// 000165_ladder_configs_sweep_ceiling in internal/database/postgres/migrations.
const sweepCeilingMigrationVersion = 165

// ReadoptMaxExpiredAge bounds how long after expiry an unapproved plan-step
// root may be re-armed. updated_at is the expiry time because nothing writes an
// expired row afterwards; a future writer that bumps it only widens the window.
const ReadoptMaxExpiredAge = 30 * 24 * time.Hour

// ErrLadderCeilingUnavailable: the sweep-ceiling migration is not applied yet.
var ErrLadderCeilingUnavailable = errors.New("the ladder sweep ceiling is not available until the database migration is applied")

// ErrReadoptNotReady: the re-adoption migration is not applied yet.
var ErrReadoptNotReady = errors.New("re-arming an expired plan step is not available until the database migration is applied")

// rowQuerier is satisfied by both the store's connection and a pgx.Tx, so a
// schema check can run inside the caller's transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// readSchemaVersion returns the applied migration version and its dirty flag.
// found is false when schema_migrations has no row (nothing applied yet).
func readSchemaVersion(ctx context.Context, q rowQuerier) (version int, dirty, found bool, err error) {
	err = q.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, fmt.Errorf("read schema version: %w", err)
	}
	return version, dirty, true, nil
}

// settlementSchemaReady reports whether the settlement columns exist. main
// deploys on merge while prod migrations are applied separately, so the code
// must run against a schema without them: the retry link and the managed
// delete guard fall back to their pre-settlement queries, and settling is
// refused (ErrExecutionSettleNotReady). A later version means the columns
// exist even if that later migration is dirty; at exactly this version a
// dirty flag means it may not have finished.
func settlementSchemaReady(ctx context.Context, q rowQuerier) (bool, error) {
	version, dirty, found, err := readSchemaVersion(ctx, q)
	if err != nil || !found {
		return false, err
	}
	return version > settlementMigrationVersion || (version == settlementMigrationVersion && !dirty), nil
}

// directResolveSchemaReady reports whether the direct-exchange resolution
// columns exist. Same N-1 rule as settlementSchemaReady: until migration 163 is
// applied the guard and finalize drop their resolved_at references and the
// list, get and resolve paths answer ErrRIExchangeDirectResolveNotReady.
func directResolveSchemaReady(ctx context.Context, q rowQuerier) (bool, error) {
	version, dirty, found, err := readSchemaVersion(ctx, q)
	if err != nil || !found {
		return false, err
	}
	return version > directResolveMigrationVersion || (version == directResolveMigrationVersion && !dirty), nil
}

// requireDirectResolveSchema fails with ErrRIExchangeDirectResolveNotReady
// while the resolution columns are missing.
func requireDirectResolveSchema(ctx context.Context, q rowQuerier) error {
	ready, err := directResolveSchemaReady(ctx, q)
	if err != nil {
		return err
	}
	if !ready {
		return ErrRIExchangeDirectResolveNotReady
	}
	return nil
}

// readoptSchemaReady reports whether approval_readopted_at exists. Same N-1
// rule as settlementSchemaReady: until migration 164 is applied the list
// returns nothing and the CAS answers ErrReadoptNotReady.
func readoptSchemaReady(ctx context.Context, q rowQuerier) (bool, error) {
	version, dirty, found, err := readSchemaVersion(ctx, q)
	if err != nil || !found {
		return false, err
	}
	return version > readoptMigrationVersion || (version == readoptMigrationVersion && !dirty), nil
}

// sweepCeilingSchemaReady reports whether max_hourly_commit_per_sweep exists.
// Same N-1 rule as settlementSchemaReady. A plain read is used instead of
// catching SQLSTATE 42703 because a failed statement would abort the caller's
// transaction.
func sweepCeilingSchemaReady(ctx context.Context, q rowQuerier) (bool, error) {
	version, dirty, found, err := readSchemaVersion(ctx, q)
	if err != nil || !found {
		return false, err
	}
	return version > sweepCeilingMigrationVersion || (version == sweepCeilingMigrationVersion && !dirty), nil
}
