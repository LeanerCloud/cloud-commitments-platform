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

// Down of 000032 narrows the key back to 5 columns. Rows that differ only by
// term or payment_option must be collapsed (newest collected_at kept) instead
// of failing the CREATE UNIQUE INDEX (issue #1769).
func TestMigration_0032_DownCollapsesTermPaymentVariants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	migrationsPath := getMigrationsPath()

	container := testhelpers.RequireEmptyDatabase(ctx, t)
	pool := container.DB.Pool()
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 32))

	ins := func(svc, rt string, term int, pay, collected string) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO recommendations
			(collected_at, provider, service, region, resource_type, payload, term, payment_option)
			VALUES ($1::timestamptz, 'azure', $2, 'eastus', $3, '{}', $4, $5)`,
			collected, svc, rt, term, pay)
		require.NoError(t, err)
	}
	// Same key but term differs: the newer row (3yr) must survive.
	ins("vm", "D2", 12, "upfront", "2026-01-01T00:00:00Z")
	ins("vm", "D2", 36, "upfront", "2026-01-02T00:00:00Z")
	// Same key but payment_option differs: the newer row (partial) must survive.
	ins("vm", "D4", 36, "partial", "2026-01-05T00:00:00Z")
	ins("vm", "D4", 36, "upfront", "2026-01-03T00:00:00Z")
	// Unique row is untouched.
	ins("db", "E2", 12, "upfront", "2026-01-01T00:00:00Z")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 31))

	rows, err := pool.Query(ctx, `SELECT resource_type, collected_at = $1::timestamptz FROM recommendations ORDER BY resource_type`, "2026-01-01T00:00:00Z")
	require.NoError(t, err)
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var rt string
		var isOld bool
		require.NoError(t, rows.Scan(&rt, &isOld))
		_, dup := got[rt]
		require.False(t, dup, "one row per key, got a second for %s", rt)
		got[rt] = isOld
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, map[string]bool{"D2": false, "D4": false, "E2": true}, got)

	var partial int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM recommendations WHERE resource_type='D4' AND collected_at='2026-01-05T00:00:00Z'`).Scan(&partial))
	assert.Equal(t, 1, partial, "newest D4 row kept")
}
