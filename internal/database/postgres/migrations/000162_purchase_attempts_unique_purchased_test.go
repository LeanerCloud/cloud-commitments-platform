//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const purchasedIndexName = "uq_purchase_attempts_purchased_reservation"

func purchasedIndexExists(ctx context.Context, t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)`,
		purchasedIndexName).Scan(&exists))
	return exists
}

// Migration 000162 adds the unique purchased-reservation index; down drops it.
func TestMigration_0162_UniquePurchasedReservation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	migrationsPath := getMigrationsPath()

	container := testhelpers.RequireEmptyDatabase(ctx, t)
	pool := container.DB.Pool()

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 161))
	require.False(t, purchasedIndexExists(ctx, t, pool), "index absent before 000162")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 162))
	assert.True(t, purchasedIndexExists(ctx, t, pool), "index present after 000162")

	_, err := pool.Exec(ctx, `INSERT INTO purchase_attempts (idempotency_token, execution_id, state, reservation_id) VALUES ('t1', 'e1', 'purchased', 'rn-1')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO purchase_attempts (idempotency_token, execution_id, state, reservation_id) VALUES ('t2', 'e2', 'purchased', 'rn-1')`)
	require.Error(t, err, "a second purchased row for the same reservation is rejected")
	_, err = pool.Exec(ctx, `INSERT INTO purchase_attempts (idempotency_token, execution_id, state) VALUES ('t2', 'e2', 'sending')`)
	require.NoError(t, err, "non-purchased rows are not constrained")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 161))
	assert.False(t, purchasedIndexExists(ctx, t, pool), "the down migration drops the index")
	_, err = pool.Exec(ctx, `INSERT INTO purchase_attempts (idempotency_token, execution_id, state, reservation_id) VALUES ('t3', 'e3', 'purchased', 'rn-1')`)
	require.NoError(t, err, "duplicates are allowed again after down")
}

// The pre-check refuses to build the index over existing duplicates and names them.
func TestMigration_0162_PreCheckNamesDuplicates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	migrationsPath := getMigrationsPath()

	container := testhelpers.RequireEmptyDatabase(ctx, t)
	pool := container.DB.Pool()

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 161))
	for _, row := range [][3]string{{"t1", "e1", "rn-dup"}, {"t2", "e2", "rn-dup"}, {"t3", "e3", "rn-ok"}} {
		_, err := pool.Exec(ctx, `INSERT INTO purchase_attempts (idempotency_token, execution_id, state, reservation_id) VALUES ($1, $2, 'purchased', $3)`,
			row[0], row[1], row[2])
		require.NoError(t, err)
	}

	err := migrations.MigrateToVersion(ctx, pool, migrationsPath, 162)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate purchased reservation_id")
	assert.Contains(t, err.Error(), "rn-dup")
	assert.NotContains(t, err.Error(), "rn-ok")
	assert.False(t, purchasedIndexExists(ctx, t, pool), "no index after a refused migration")
}
