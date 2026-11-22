//go:build integration
// +build integration

package migrations_test

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const migration179Version = 179

// TestMigration179_MarketplaceListingTerminalAt locks down migration 000179
// (issue #1586): existing closed and cancelled listings are backfilled with a
// fresh terminal_at and active ones stay NULL; afterwards terminal_at is
// stamped once on the first transition to a terminal state, kept by later
// writes, carried to the superseded-listing row on a relist; the rollback
// drops the column and triggers.
//
// Fail-before: at the previous version the column does not exist.
func TestMigration179_MarketplaceListingTerminalAt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)
	pool := container.DB.Pool()
	migrationsPath := getMigrationsPath()

	prev := previousMigrationVersion(t, migration179Version)
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, prev))

	old := time.Now().AddDate(-1, 0, 0)
	listedAt := time.Now().AddDate(0, 0, -300)
	seedListingRow(ctx, t, pool, "ri-179-closed", "ril-179-c", "closed", old, &listedAt, nil, nil)
	seedListingRow(ctx, t, pool, "ri-179-active", "ril-179-a", "active", old, &listedAt, nil, nil)

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, migration179Version))
	terminalAt := func(purchaseID string) *time.Time {
		var at *time.Time
		require.NoError(t, pool.QueryRow(ctx, `SELECT terminal_at FROM purchase_history WHERE purchase_id = $1`, purchaseID).Scan(&at))
		return at
	}
	backfilled := terminalAt("ri-179-closed")
	require.NotNil(t, backfilled, "existing terminal listing gets a fresh window")
	assert.WithinDuration(t, time.Now(), *backfilled, time.Minute)
	assert.Nil(t, terminalAt("ri-179-active"))

	// Stamped on the first transition, kept by a repeated write.
	_, err := pool.Exec(ctx, `UPDATE purchase_history SET listing_state = 'closed' WHERE purchase_id = 'ri-179-active'`)
	require.NoError(t, err)
	first := terminalAt("ri-179-active")
	require.NotNil(t, first)
	_, err = pool.Exec(ctx, `UPDATE purchase_history SET listing_state = 'closed', listing_client_token = NULL WHERE purchase_id = 'ri-179-active'`)
	require.NoError(t, err)
	assert.True(t, first.Equal(*terminalAt("ri-179-active")), "stamped once")

	// A relist moves the terminal listing to the settlement table with its
	// terminal_at and clears the row's.
	_, err = pool.Exec(ctx, `UPDATE purchase_history SET listing_id = 'ril-179-a2', listing_state = 'active' WHERE purchase_id = 'ri-179-active'`)
	require.NoError(t, err)
	assert.Nil(t, terminalAt("ri-179-active"))
	var carried *time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT terminal_at FROM marketplace_listing_settlements WHERE listing_id = 'ril-179-a'`).Scan(&carried))
	require.NotNil(t, carried)
	assert.True(t, first.Equal(*carried))

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, prev))
	var columns, triggers int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE column_name = 'terminal_at' AND table_name IN ('purchase_history', 'marketplace_listing_settlements')`).Scan(&columns))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname LIKE '%stamp%terminal_at'`).Scan(&triggers))
	assert.Zero(t, columns)
	assert.Zero(t, triggers)
	_, err = pool.Exec(ctx, `UPDATE purchase_history SET listing_id = 'ril-179-a3' WHERE purchase_id = 'ri-179-active'`)
	require.NoError(t, err, "the restored keep-superseded trigger still works")
}
