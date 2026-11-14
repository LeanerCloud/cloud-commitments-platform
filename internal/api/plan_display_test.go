package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #609: a plan step with no recommendations must be flagged, so the UI
// does not present its Count 0 / $0 / "Various" placeholders as data.
func TestPlannedPurchaseListFlagsStepWithoutRecommendations(t *testing.T) {
	t.Parallel()
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

func TestBuildPlannedPurchaseProjectsAttachedQuantitiesAndTuple(t *testing.T) {
	t.Parallel()
	rec := func(count int, resourceType, region string) config.RecommendationRecord {
		return config.RecommendationRecord{Provider: "azure", Service: "compute", Count: count, ResourceType: resourceType, Region: region, Term: 1, Payment: "monthly"}
	}
	azure := rec(20, "Standard_D2s_v3", "westeurope")
	azure.Purchased, azure.Selected = true, false
	aws := rec(6, "m5.large", "us-east-1")
	aws.Provider, aws.Service = "aws", "ec2"
	gcp := rec(12, "n2", "us-central1")
	gcp.Provider, gcp.Service = "gcp", "compute"
	details, err := json.Marshal(common.SavingsPlanDetails{HourlyCommitment: 0.12345})
	require.NoError(t, err)
	sp := config.RecommendationRecord{Provider: "aws", Service: "savings-plans-compute", Count: 1, Details: details, Term: 1, Payment: "no-upfront"}
	globalSP := sp
	globalSP.ResourceType, globalSP.Region = "ComputeSP", "global"
	cases := []struct {
		name         string
		resourceType string
		region       string
		recs         []config.RecommendationRecord
		count        int
	}{
		{name: "nil", recs: nil, count: 0, resourceType: "Various", region: "Multiple"},
		{name: "empty", recs: []config.RecommendationRecord{}, count: 0, resourceType: "Various", region: "Multiple"},
		{name: "Azure reservation quantity", recs: []config.RecommendationRecord{azure}, count: 20, resourceType: "Standard_D2s_v3", region: "westeurope"},
		{name: "homogeneous tuples", recs: []config.RecommendationRecord{rec(7, "Standard_D2s_v3", "westeurope"), rec(13, "Standard_D2s_v3", "westeurope")}, count: 20, resourceType: "Standard_D2s_v3", region: "westeurope"},
		{name: "mixed types", recs: []config.RecommendationRecord{rec(7, "Standard_D2s_v3", "westeurope"), rec(13, "Standard_D4s_v3", "westeurope")}, count: 20, resourceType: "Various", region: "westeurope"},
		{name: "mixed regions", recs: []config.RecommendationRecord{rec(7, "Standard_D2s_v3", "westeurope"), rec(13, "Standard_D2s_v3", "eastus")}, count: 20, resourceType: "Standard_D2s_v3", region: "Multiple"},
		{name: "unknown type", recs: []config.RecommendationRecord{rec(7, "", "westeurope"), rec(13, "Standard_D2s_v3", "westeurope")}, count: 20, resourceType: "Various", region: "westeurope"},
		{name: "unknown region", recs: []config.RecommendationRecord{rec(7, "Standard_D2s_v3", ""), rec(13, "Standard_D2s_v3", "westeurope")}, count: 20, resourceType: "Standard_D2s_v3", region: "Multiple"},
		{name: "reversed unknown fields", recs: []config.RecommendationRecord{rec(13, "Standard_D2s_v3", "westeurope"), rec(7, "", "")}, count: 20, resourceType: "Various", region: "Multiple"},
		{name: "reversed mixed types", recs: []config.RecommendationRecord{rec(13, "Standard_D4s_v3", "westeurope"), rec(7, "Standard_D2s_v3", "westeurope")}, count: 20, resourceType: "Various", region: "westeurope"},
		{name: "AWS RI instances", recs: []config.RecommendationRecord{aws}, count: 6, resourceType: "m5.large", region: "us-east-1"},
		{name: "GCP vCPU units", recs: []config.RecommendationRecord{gcp}, count: 12, resourceType: "n2", region: "us-central1"},
		{name: "Savings Plan hourly amount is not Count", recs: []config.RecommendationRecord{sp}, count: 1, resourceType: "Various", region: "Multiple"},
		{name: "explicit Savings Plan tuple", recs: []config.RecommendationRecord{globalSP}, count: 1, resourceType: "ComputeSP", region: "global"},
		{name: "mixed providers keep raw units", recs: []config.RecommendationRecord{aws, gcp}, count: 18, resourceType: "Various", region: "Multiple"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := config.ServiceConfig{Provider: "azure", Service: "compute", Term: 1, Payment: "monthly"}
			if len(tc.recs) > 0 {
				svc.Provider, svc.Service, svc.Term, svc.Payment = tc.recs[0].Provider, tc.recs[0].Service, tc.recs[0].Term, tc.recs[0].Payment
			}
			plan := &config.PurchasePlan{ID: "plan", Name: "Projection fixture", Services: map[string]config.ServiceConfig{"service": svc}, RampSchedule: config.RampSchedule{TotalSteps: 4}}
			creator := "creator"
			exec := &config.PurchaseExecution{ExecutionID: "execution", PlanID: plan.ID, Status: "notified", StepNumber: 1,
				ScheduledDate: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), CreatedByUserID: &creator,
				TotalUpfrontCost: 123.45, EstimatedSavings: 67.89, Recommendations: tc.recs}
			before, marshalErr := json.Marshal(exec)
			require.NoError(t, marshalErr)
			got := buildPlannedPurchase(plan, exec)
			want := PlannedPurchase{ID: "execution", PlanID: "plan", PlanName: "Projection fixture", ScheduledDate: "2026-10-18",
				Provider: svc.Provider, Service: svc.Service, ResourceType: tc.resourceType, Region: tc.region, Count: tc.count,
				Term: svc.Term, Payment: svc.Payment, UpfrontCost: 123.45, EstimatedSavings: 67.89, Status: "notified",
				StepNumber: 1, TotalSteps: 4, CreatedByUserID: &creator, HasRecommendations: len(tc.recs) > 0}
			assert.Equal(t, want, got)
			after, marshalErr := json.Marshal(exec)
			require.NoError(t, marshalErr)
			assert.Equal(t, string(before), string(after))
		})
	}
}
