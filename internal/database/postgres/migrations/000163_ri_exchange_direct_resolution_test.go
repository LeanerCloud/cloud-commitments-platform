//go:build integration
// +build integration

package migrations_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// directResolutionMigration is this migration's version. A renumber at merge
// changes this constant and the two file names, nothing else.
const directResolutionMigration uint = 163

var directResolutionColumns = []string{"resolved_status", "resolved_by_user_id", "resolved_by", "resolved_at", "resolution_note"}

func directResolutionColumnState(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT column_name, is_nullable = 'YES' FROM information_schema.columns
		WHERE table_name = 'ri_exchange_direct_executions' AND column_name = ANY($1)`, directResolutionColumns)
	require.NoError(t, err)
	defer rows.Close()
	nullable := map[string]bool{}
	for rows.Next() {
		var name string
		var isNullable bool
		require.NoError(t, rows.Scan(&name, &isNullable))
		nullable[name] = isNullable
	}
	require.NoError(t, rows.Err())
	return nullable
}

// insertDirectRow inserts an unknown AWS row with the given extra columns and
// their SQL literal values.
func insertDirectRow(ctx context.Context, pool *pgxpool.Pool, cols, vals string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO ri_exchange_direct_executions
			(provider, source_ids, targets, max_payment_due, max_payment_currency, executed_by, status, completed_at`+cols+`)
		VALUES ('aws', ARRAY['ri-1'], '[{"offering_id":"o"}]'::jsonb, 100, 'USD', 'alice@example.com', 'unknown', now()`+vals+`)`)
	return err
}

// TestMigration_RIExchangeDirectResolution pins the #1836 migration against a
// real PostgreSQL: five nullable columns appear, a resolution must be whole or
// absent, resolved_status is limited to completed or failed, the up statement
// is idempotent, and the down migration drops the columns.
func TestMigration_RIExchangeDirectResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	migrationsPath := getMigrationsPath()

	container := testhelpers.RequireEmptyDatabase(ctx, t)
	pool := container.DB.Pool()

	prev := previousMigrationVersion(t, directResolutionMigration)
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, prev))
	require.Empty(t, directResolutionColumnState(t, ctx, pool), "columns must not exist before the migration")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, directResolutionMigration))
	state := directResolutionColumnState(t, ctx, pool)
	require.Len(t, state, len(directResolutionColumns))
	for _, c := range directResolutionColumns {
		assert.True(t, state[c], "%s must be nullable", c)
	}

	const whole = ", resolved_status, resolved_by, resolved_at, resolution_note"
	// An unresolved row (all NULL) and whole resolutions are accepted.
	require.NoError(t, insertDirectRow(ctx, pool, "", ""))
	require.NoError(t, insertDirectRow(ctx, pool, whole, ", 'failed', 'ops', now(), 'note'"))
	require.NoError(t, insertDirectRow(ctx, pool, whole+", resolved_by_user_id",
		", 'completed', 'ops', now(), 'note', '00000000-0000-0000-0000-000000000001'"))

	// Each partial resolution is refused.
	for name, c := range map[string]struct{ cols, vals string }{
		"status only":                  {", resolved_status", ", 'failed'"},
		"no note":                      {", resolved_status, resolved_by, resolved_at", ", 'failed', 'ops', now()"},
		"no resolved_at":               {", resolved_status, resolved_by, resolution_note", ", 'failed', 'ops', 'n'"},
		"no resolved_by":               {", resolved_status, resolved_at, resolution_note", ", 'failed', now(), 'n'"},
		"no status":                    {", resolved_by, resolved_at, resolution_note", ", 'ops', now(), 'n'"},
		"note only":                    {", resolution_note", ", 'n'"},
		"user id only (no resolution)": {", resolved_by_user_id", ", '00000000-0000-0000-0000-000000000001'"},
	} {
		err := insertDirectRow(ctx, pool, c.cols, c.vals)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "ri_exchange_direct_executions_resolution_all_or_none", name)
	}

	// A whole resolution with a verdict outside completed/failed is refused.
	for _, bad := range []string{"unknown", "submitted", ""} {
		err := insertDirectRow(ctx, pool, whole, fmt.Sprintf(", '%s', 'ops', now(), 'n'", bad))
		require.Error(t, err, "resolved_status %q", bad)
		assert.Contains(t, err.Error(), "ri_exchange_direct_executions_resolved_status_check", "resolved_status %q", bad)
	}

	up, err := os.ReadFile(filepath.Join(migrationsPath, fmt.Sprintf("%06d_ri_exchange_direct_resolution.up.sql", directResolutionMigration)))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err, "the up statement must be idempotent")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, prev))
	assert.Empty(t, directResolutionColumnState(t, ctx, pool), "down migration must drop the columns")
	var rows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM ri_exchange_direct_executions`).Scan(&rows))
	assert.Equal(t, 3, rows, "down is lossy for the columns only, never the rows")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, directResolutionMigration))
	state = directResolutionColumnState(t, ctx, pool)
	require.Len(t, state, len(directResolutionColumns), "up again restores the columns")
	err = insertDirectRow(ctx, pool, ", resolved_status", ", 'failed'")
	require.Error(t, err, "up again restores the all-or-none CHECK")
	assert.Contains(t, err.Error(), "ri_exchange_direct_executions_resolution_all_or_none")
	err = insertDirectRow(ctx, pool, whole, ", 'unknown', 'ops', now(), 'n'")
	require.Error(t, err, "up again restores the enum CHECK")
	assert.Contains(t, err.Error(), "ri_exchange_direct_executions_resolved_status_check")
}
