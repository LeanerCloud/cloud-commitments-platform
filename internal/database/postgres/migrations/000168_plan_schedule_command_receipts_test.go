//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/require"
)

func TestMigration_PlanScheduleCommandReceipts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	container := testhelpers.RequireEmptyDatabase(ctx, t)
	pool, path := container.DB.Pool(), getMigrationsPath()

	exists := func() bool {
		var ok bool
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT to_regclass('plan_schedule_command_receipts') IS NOT NULL`).Scan(&ok))
		return ok
	}

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, 167))
	require.False(t, exists())
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, 168))
	require.True(t, exists())
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, 167))
	require.False(t, exists())
}
