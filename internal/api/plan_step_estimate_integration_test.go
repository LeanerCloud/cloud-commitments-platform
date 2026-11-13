//go:build integration

package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reported Azure plan (compute, 1y, monthly, 80%, weekly 25%, one account)
// against real PostgreSQL and the real scheduler read and manager tick.

func (f *combinedPlanFixture) plannedRow(t *testing.T, step int) PlannedPurchase {
	t.Helper()
	response := combinedRoute(t, f.handler, "GET", "/api/purchases/planned", nil)
	require.Equal(t, 200, response.StatusCode, response.Body)
	var planned PlannedPurchasesResponse
	require.NoError(t, json.Unmarshal([]byte(response.Body), &planned))
	for _, row := range planned.Purchases {
		if row.PlanID == f.planID && row.StepNumber == step {
			return row
		}
	}
	t.Fatalf("step %d absent from the planned list: %s", step, response.Body)
	return PlannedPurchase{}
}

func TestPlanStepEstimate_EqualsWhatTheTickAttaches(t *testing.T) {
	f := prepareCombinedPlanFixture(t, false, nil, "USD")
	ctx := context.Background()

	est, later := f.plannedRow(t, 1), f.plannedRow(t, 2)
	assert.True(t, est.Estimate)
	assert.False(t, est.HasRecommendations)
	assert.Equal(t, 20, est.Count)
	assert.False(t, later.Estimate)
	assert.Equal(t, "sized when step 1 runs", later.EstimateNote)
	require.Empty(t, f.rows(t, 1)[0].Recommendations, "listing must not attach")
	require.NoError(t, f.store.WithTx(ctx, func(tx pgx.Tx) error {
		baseline, err := f.store.GetPlanRampBaselineTx(ctx, tx, f.planID)
		assert.Empty(t, baseline, "listing must not freeze the baseline")
		return err
	}))

	f.tick(t)
	f.assertAttached(t)
	assert.Equal(t, f.root.Recommendations[0].Count, est.Count)
	assert.InDelta(t, f.root.TotalUpfrontCost, est.UpfrontCost, 1e-9)
	assert.InDelta(t, f.root.EstimatedSavings, est.EstimatedSavings, 1e-9)
}

func TestPlanStepEstimate_NoMatchIsANoteThenOneFailedRowAfterGrace(t *testing.T) {
	f := prepareCombinedPlanFixture(t, false, nil, "USD")
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `DELETE FROM recommendations`)
	require.NoError(t, err)

	row := f.plannedRow(t, 1)
	assert.False(t, row.Estimate)
	assert.Zero(t, row.Count)
	assert.Equal(t, "no matching recommendation right now", row.EstimateNote)

	f.tick(t)
	assert.Empty(t, f.root.Recommendations)
	assert.NotEqual(t, "failed", f.root.Status, "no failure inside the grace window")

	_, err = f.pool.Exec(ctx, `UPDATE purchase_plans SET next_execution_date = next_execution_date - interval '60 days' WHERE id = $1`, f.planID)
	require.NoError(t, err)
	f.tick(t)
	assert.Equal(t, "failed", f.root.Status)
	assert.Len(t, f.rows(t, 1), 1)
	plan, err := f.store.GetPurchasePlan(ctx, f.planID)
	require.NoError(t, err)
	assert.Zero(t, plan.RampSchedule.CurrentStep)
}
