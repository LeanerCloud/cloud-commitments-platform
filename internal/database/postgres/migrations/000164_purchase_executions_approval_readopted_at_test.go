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

// Migration 000164 adds the nullable purchase_executions.approval_readopted_at:
// existing rows stay NULL and the down migration drops it.
func TestMigration_0164_ApprovalReadoptedAt(t *testing.T) {
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
			WHERE table_name = 'purchase_executions' AND column_name = 'approval_readopted_at')`).Scan(&exists))
		return exists
	}

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, version)))
	assert.False(t, columnExists(), "column absent before 000164")
	_, err = pool.Exec(ctx, `INSERT INTO purchase_executions (status, scheduled_date) VALUES ('pending', NOW())`)
	require.NoError(t, err)

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, version))
	assert.True(t, columnExists(), "column present after 000164")
	var nullRows, total int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE approval_readopted_at IS NULL), count(*) FROM purchase_executions`).Scan(&nullRows, &total))
	assert.Equal(t, 1, total)
	assert.Equal(t, total, nullRows, "existing rows stay NULL")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, version)))
	assert.False(t, columnExists(), "the down migration drops the column")
}
