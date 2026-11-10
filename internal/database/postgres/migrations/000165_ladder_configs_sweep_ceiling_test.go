//go:build integration

package migrations_test

import (
	"context"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Migration 000165 adds the nullable ladder_configs.max_hourly_commit_per_sweep
// with a positive-only CHECK: existing configs stay NULL (not set) and the down
// migration drops the column.
func TestMigration_0165_LadderConfigsSweepCeiling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	migrationsPath := getMigrationsPath()
	// The version comes from this file's own name, so a renumber at merge needs no edit here.
	_, thisFile, _, _ := runtime.Caller(0)
	n, err := strconv.ParseUint(filepath.Base(thisFile)[:6], 10, 32)
	require.NoError(t, err)
	version := uint(n)

	container := testhelpers.RequireEmptyDatabase(ctx, t)
	pool := container.DB.Pool()

	columnExists := func() bool {
		var exists bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'ladder_configs' AND column_name = 'max_hourly_commit_per_sweep')`).Scan(&exists))
		return exists
	}

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, version)))
	assert.False(t, columnExists(), "column absent before the migration")
	var acctID string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO cloud_accounts
		(name, enabled, provider, external_id, aws_is_org_root)
		VALUES ('Ceiling Test', true, 'aws', '123456789012', false) RETURNING id`).Scan(&acctID))
	var cfgID string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO ladder_configs
		(cloud_account_id, provider, enabled, mode, cadence, target_coverage, buffer_fraction,
		 baseline_percentile, lookback_days, buffer_utilization_threshold, max_actions_per_run, ramp_schedule)
		VALUES ($1, 'aws', true, 'email_approval', 'daily', 80, 0.1, 5, 30, 50, 5, '{}'::jsonb) RETURNING id`, acctID).Scan(&cfgID))

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, version))
	assert.True(t, columnExists(), "column present after the migration")
	var ceiling *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT max_hourly_commit_per_sweep::text FROM ladder_configs WHERE id = $1`, cfgID).Scan(&ceiling))
	assert.Nil(t, ceiling, "existing configs stay NULL, not set")

	for _, bad := range []string{"0", "-1"} {
		_, err = pool.Exec(ctx, `UPDATE ladder_configs SET max_hourly_commit_per_sweep = $2::numeric WHERE id = $1`, cfgID, bad)
		require.Error(t, err, "the CHECK rejects %s", bad)
	}
	_, err = pool.Exec(ctx, `UPDATE ladder_configs SET max_hourly_commit_per_sweep = 1.5 WHERE id = $1`, cfgID)
	require.NoError(t, err)

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, version)))
	assert.False(t, columnExists(), "the down migration drops the column")
}
