//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Migration 000174 creates purchase_claims; the down migration drops it.
func TestMigration_0174_PurchaseClaims(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	migrationsPath := getMigrationsPath()

	container := testhelpers.RequireEmptyDatabase(ctx, t)
	pool := container.DB.Pool()

	tableExists := func() bool {
		var exists bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.tables WHERE table_name = 'purchase_claims')`).Scan(&exists))
		return exists
	}

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, 174)))
	assert.False(t, tableExists(), "table absent before 000174")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 174))
	assert.True(t, tableExists(), "table present after 000174")

	ins := `INSERT INTO purchase_claims (idempotency_token, holder, generation, phase, order_id, expires_at, last_renewed_at)
		VALUES ($1, 'h', $2, $3, NULLIF($4, ''), now(), now())`
	_, err := pool.Exec(ctx, ins, "t1", 1, "bogus", "")
	require.Error(t, err, "phase CHECK rejects unknown phases")
	_, err = pool.Exec(ctx, ins, "t1", 0, "claimed", "")
	require.Error(t, err, "generation starts at 1")
	_, err = pool.Exec(ctx, ins, "t1", 1, "claimed", "o-1")
	require.Error(t, err, "order_id is only valid when completed")
	_, err = pool.Exec(ctx, ins, "t1", 1, "completed", "")
	require.Error(t, err, "completed requires an order_id")
	_, err = pool.Exec(ctx, ins, "t1", 1, "completed", "o-1")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, ins, "t1", 1, "claimed", "")
	require.Error(t, err, "primary key (token)")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, 174)))
	assert.False(t, tableExists(), "the down migration drops the table")
}
