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

// Migration 000158 creates purchase_attempts; the down migration drops it.
func TestMigration_0158_PurchaseAttempts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	migrationsPath := getMigrationsPath()

	container := testhelpers.RequireEmptyDatabase(ctx, t)
	pool := container.DB.Pool()

	tableExists := func() bool {
		var exists bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.tables WHERE table_name = 'purchase_attempts')`).Scan(&exists))
		return exists
	}

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, 158)))
	assert.False(t, tableExists(), "table absent before 000158")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 158))
	assert.True(t, tableExists(), "table present after 000158")

	_, err := pool.Exec(ctx, `INSERT INTO purchase_attempts (idempotency_token, execution_id, state) VALUES ('t', 'e', 'bogus')`)
	require.Error(t, err, "state CHECK rejects unknown states")
	_, err = pool.Exec(ctx, `INSERT INTO purchase_attempts (idempotency_token, execution_id, state, reservation_id) VALUES ('t', 'e', 'sending', 'rn-1')`)
	require.Error(t, err, "reservation_id is only valid when purchased")
	_, err = pool.Exec(ctx, `INSERT INTO purchase_attempts (idempotency_token, execution_id, state) VALUES ('t', 'e', 'purchased')`)
	require.Error(t, err, "purchased requires a reservation_id")
	_, err = pool.Exec(ctx, `INSERT INTO purchase_attempts (idempotency_token, execution_id, state) VALUES ('t', 'e', 'sending')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO purchase_attempts (idempotency_token, execution_id, state) VALUES ('t', 'e', 'sending')`)
	require.Error(t, err, "primary key (token, execution)")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, 158)))
	assert.False(t, tableExists(), "the down migration drops the table")
}
