package analytics

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Legacy-estimate rows (issue #669) are counted in their bucket but add no
// savings, amortized commitment or usage to the snapshot.
func TestAggregatePurchases_LegacyEstimateAddsNoDollars(t *testing.T) {
	t.Parallel()
	upfront, monthly := 1200.0, 30.0
	row := func(id string, basis config.PurchaseAmountBasis) config.PurchaseHistoryRecord {
		return config.PurchaseHistoryRecord{
			AccountID: "sub-1", PurchaseID: id, Provider: "azure", Service: "compute", Region: "westeurope",
			Term: 1, Timestamp: time.Now().Add(-time.Hour), EstimatedSavings: 100,
			UpfrontCost: &upfront, MonthlyCost: &monthly, AmountBasis: basis,
		}
	}
	purchases := []config.PurchaseHistoryRecord{
		row("legacy", config.PurchaseAmountBasisLegacyWindow),
		row("good", ""),
	}
	groups, active, _, err := aggregatePurchases(context.Background(), purchases, time.Now())
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, 2, active)
	for _, g := range groups {
		assert.Equal(t, 2, g.count, "the legacy row stays counted")
		assert.InDelta(t, 100.0, g.savings, 1e-9)
		assert.InDelta(t, upfront/MonthsPerYear, g.commitment, 1e-9)
		assert.InDelta(t, monthly, g.usage, 1e-9)
		assert.False(t, g.commitmentUnknown)
	}
}
