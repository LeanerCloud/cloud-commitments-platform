//go:build integration

package migrations_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
)

// TestMigration70_FreshInstallStoresTheDocumentedPurchaseDelay: a database
// migrated from scratch stores purchase_delay_hours = 48 on the seeded
// global_config row, the value config.DefaultPurchaseDelayHours documents, so a
// fresh install has the free-cancel window (#1934).
//
// Fail-before: with the column added as DEFAULT 0 the seeded row held 0 and
// approved purchases executed immediately.
func TestMigration70_FreshInstallStoresTheDocumentedPurchaseDelay(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := testhelpers.RequireMigratedDatabase(ctx, t, getMigrationsPath()).DB.Pool()

	var got int
	require.NoError(t, pool.QueryRow(ctx, `SELECT purchase_delay_hours FROM global_config WHERE id = 1`).Scan(&got))
	require.Equal(t, config.DefaultPurchaseDelayHours, got)
	require.Equal(t, 48, got)
}
