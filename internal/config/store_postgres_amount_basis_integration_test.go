//go:build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func saveBasisRow(ctx context.Context, t *testing.T, s *PostgresStore, account string, upfront *float64, savings float64) string {
	t.Helper()
	rec := &PurchaseHistoryRecord{
		AccountID: account, PurchaseID: uuid.NewString(), Timestamp: time.Now().UTC().Add(-time.Hour),
		Provider: "azure", Service: "compute", Region: "westeurope", ResourceType: "D2s_v3", Count: 1, Term: 1,
		Payment: "all-upfront", UpfrontCost: upfront, EstimatedSavings: savings,
	}
	require.NoError(t, s.SavePurchaseHistory(ctx, rec))
	return rec.PurchaseID
}

func flagBasis(ctx context.Context, t *testing.T, s *PostgresStore, purchaseID string) {
	t.Helper()
	_, err := s.db.Exec(ctx, `UPDATE purchase_history SET amount_basis = 'legacy_window' WHERE purchase_id = $1`, purchaseID)
	require.NoError(t, err)
}

// Flagged rows keep their purchase count but add no dollars to the SQL summary,
// even when a flagged row has an unknown upfront cost, and every purchase_history
// reader returns the basis (issue #669).
func TestAmountBasis_SummaryAndReaders(t *testing.T) {
	ctx := context.Background()
	s := setupRampStepStore(ctx, t)
	up := func(v float64) *float64 { return &v }

	legacyUnknown := saveBasisRow(ctx, t, s, "mixed", nil, 500)
	legacyKnown := saveBasisRow(ctx, t, s, "mixed", up(50), 200)
	good := saveBasisRow(ctx, t, s, "mixed", up(100), 20)
	onlyLegacy := saveBasisRow(ctx, t, s, "only-legacy", up(70), 9)
	for _, id := range []string{legacyUnknown, legacyKnown, onlyLegacy} {
		flagBasis(ctx, t, s, id)
	}

	agg, err := s.HistorySummaryAggregate(ctx, HistorySummaryFilter{})
	require.NoError(t, err)
	byAccount := map[string]PurchaseHistorySummaryGroup{}
	for _, g := range agg.Purchases {
		byAccount[g.AccountID] = g
	}
	mixed := byAccount["mixed"]
	assert.Equal(t, 3, mixed.Count, "flagged rows stay counted")
	require.NotNil(t, mixed.UpfrontCost, "a flagged NULL upfront must not make the total unknown")
	assert.InDelta(t, 100.0, *mixed.UpfrontCost, 1e-9)
	assert.InDelta(t, 20.0, mixed.EstimatedSavings, 1e-9)
	only := byAccount["only-legacy"]
	assert.Equal(t, 1, only.Count)
	require.NotNil(t, only.UpfrontCost)
	assert.Zero(t, *only.UpfrontCost)
	assert.Zero(t, only.EstimatedSavings)

	rows, err := s.GetPurchaseHistory(ctx, "mixed", 10)
	require.NoError(t, err)
	got := map[string]PurchaseAmountBasis{}
	for _, r := range rows {
		got[r.PurchaseID] = r.AmountBasis
	}
	assert.Equal(t, PurchaseAmountBasisLegacyWindow, got[legacyKnown])
	assert.Empty(t, got[good])

	active, err := s.GetActivePurchaseHistory(ctx, time.Now(), nil, nil)
	require.NoError(t, err)
	flagged := 0
	for i := range active {
		if active[i].IsLegacyEstimate() {
			flagged++
		}
	}
	assert.Equal(t, 3, flagged)

	page, err := s.ListPurchaseHistoryPage(ctx, PurchaseHistoryFilter{Limit: 10}, nil)
	require.NoError(t, err)
	assert.Len(t, page, 4)
	byID, err := s.GetPurchaseHistoryByPurchaseID(ctx, legacyUnknown)
	require.NoError(t, err)
	assert.Equal(t, PurchaseAmountBasisLegacyWindow, byID.AmountBasis)
}

// Below the migration every reader works and reports no basis; the gate opens
// as soon as the migration is applied, without a restart.
func TestAmountBasis_BeforeTheMigration(t *testing.T) {
	ctx := context.Background()
	s := setupRampStepStore(ctx, t)
	conn, ok := s.db.(*database.Connection)
	require.True(t, ok)
	version := uint(amountBasisMigrationVersion)
	require.NoError(t, migrations.MigrateToVersion(ctx, conn.Pool(), getTestMigrationsPath(), migrationBelow(t, amountBasisMigrationVersion)))
	saveBasisRow(ctx, t, s, "pre", nil, 5)

	rows, err := s.GetAllPurchaseHistory(ctx, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].AmountBasis)
	_, err = s.HistorySummaryAggregate(ctx, HistorySummaryFilter{})
	require.NoError(t, err, "the summary aggregate runs without the column")
	_, err = s.ListPurchaseHistoryPage(ctx, PurchaseHistoryFilter{Limit: 10}, nil)
	require.NoError(t, err)

	require.NoError(t, migrations.MigrateToVersion(ctx, conn.Pool(), getTestMigrationsPath(), version))
	rows, err = s.GetAllPurchaseHistory(ctx, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, PurchaseAmountBasisLegacyWindow, rows[0].AmountBasis,
		"with no post-fix signature row the migration flags every Azure row")
}
