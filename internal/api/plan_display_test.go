package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #609: a plan step with no recommendations must be flagged, so the UI
// does not present its Count 0 / $0 / "Various" placeholders as data.
func TestPlannedPurchaseListFlagsStepWithoutRecommendations(t *testing.T) {
	plan := &config.PurchasePlan{
		ID:   "11111111-1111-1111-1111-111111111609",
		Name: "Azure compute 80% weekly",
		Services: map[string]config.ServiceConfig{
			"azure:compute": {Provider: "azure", Service: "compute", Term: 1, Payment: "monthly"},
		},
		RampSchedule: config.RampSchedule{TotalSteps: 4, StepIntervalDays: 7},
	}
	bare := &config.PurchaseExecution{
		ExecutionID:   "22222222-3333-4444-5555-666666666609",
		PlanID:        plan.ID,
		Status:        "pending",
		StepNumber:    1,
		ScheduledDate: time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC),
	}
	withRecs := *bare
	withRecs.Recommendations = []config.RecommendationRecord{{Provider: "azure", Service: "compute", Count: 2}}

	asMap := func(p PlannedPurchase) map[string]any {
		raw, err := json.Marshal(p)
		require.NoError(t, err)
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m))
		return m
	}

	assert.Equal(t, false, asMap(buildPlannedPurchase(plan, bare))["has_recommendations"])
	assert.Equal(t, true, asMap(buildPlannedPurchase(plan, &withRecs))["has_recommendations"])
}
