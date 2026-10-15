package api

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

func editorDisplayRecord(provider, service string, count int) config.RecommendationRecord {
	monthly := 12.0
	return config.RecommendationRecord{Provider: provider, Service: service, Count: count, Selected: true,
		ResourceType: "type-one", Region: "region-one", Term: 3, UpfrontCost: 100, MonthlyCost: &monthly,
		Details: json.RawMessage(`{"currency":"USD","private":"details-marker"}`)}
}

func TestPlanEditorDisplay(t *testing.T) {
	t.Parallel()
	aws := editorDisplayRecord("aws", "ec2", 6)
	azure := editorDisplayRecord("azure", "compute", 20)
	gcp := editorDisplayRecord("gcp", "compute", 12)
	bought, both, ignored, mixed, negative, largest := aws, aws, aws, aws, aws, aws
	bought.Selected, bought.Purchased = false, true
	both.Purchased, ignored.Selected = true, false
	ignored.Count, ignored.ResourceType, ignored.Region = 999, "ignored-type", "ignored-region"
	mixed.Count, mixed.ResourceType, mixed.Region = 2, "type-two", "region-two"
	mixed.Details = json.RawMessage(`{"currency":"EUR"}`)
	negative.Count, largest.Count = -1, math.MaxInt
	overflow := mixed
	overflow.Count = 1
	cases := []struct {
		name      string
		records   []config.RecommendationRecord
		quantity  int
		unit      PlanEditorQuantityUnit
		available bool
		resource  string
		region    string
	}{
		{"aws selected raw count", []config.RecommendationRecord{aws}, 6, "instances", true, "type-one", "region-one"},
		{"azure reservation units", []config.RecommendationRecord{azure}, 20, "reservation_units", true, "type-one", "region-one"},
		{"gcp vcpu", []config.RecommendationRecord{gcp}, 12, "vcpu", true, "type-one", "region-one"},
		{"nonterminal bought only", []config.RecommendationRecord{bought}, 6, "instances", true, "type-one", "region-one"},
		{"selected and bought once", []config.RecommendationRecord{both}, 6, "instances", true, "type-one", "region-one"},
		{"ignored record", []config.RecommendationRecord{aws, ignored}, 6, "instances", true, "type-one", "region-one"},
		{"neither selected nor bought", []config.RecommendationRecord{ignored}, 0, "", false, "Various", "Multiple"},
		{"empty", nil, 0, "", false, "Various", "Multiple"},
		{"true zero", []config.RecommendationRecord{editorDisplayRecord("aws", "ec2", 0)}, 0, "instances", true, "type-one", "region-one"},
		{"mixed labels and currency details", []config.RecommendationRecord{aws, mixed}, 8, "instances", true, "Various", "Multiple"},
		{"mixed unit families", []config.RecommendationRecord{aws, gcp}, 0, "", false, "type-one", "region-one"},
		{"savings plan money is not count", []config.RecommendationRecord{editorDisplayRecord("aws", "savings-plans-compute", 1)}, 0, "", false, "type-one", "region-one"},
		{"unknown pair", []config.RecommendationRecord{editorDisplayRecord("unknown", "compute", 8)}, 0, "", false, "type-one", "region-one"},
		{"unsupported known provider", []config.RecommendationRecord{editorDisplayRecord("aws", "rds", 5)}, 0, "", false, "type-one", "region-one"},
		{"negative count", []config.RecommendationRecord{negative}, 0, "", false, "type-one", "region-one"},
		{"overflow preserves labels", []config.RecommendationRecord{largest, overflow}, 0, "", false, "Various", "Multiple"},
		{"maximum valid count", []config.RecommendationRecord{largest}, math.MaxInt, "instances", true, "type-one", "region-one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actor := "actor-marker"
			plan := &config.PurchasePlan{Name: "private-plan-name"}
			execution := &config.PurchaseExecution{Status: "notified", Recommendations: tc.records,
				ApprovalToken: "token-marker", IdempotencyKey: "lineage-marker", Error: "error-marker",
				CreatedByUserID: &actor, TotalUpfrontCost: 1000, EstimatedSavings: 500}
			before, err := json.Marshal(execution)
			require.NoError(t, err)
			display := buildPlanEditorDisplay(plan, execution)
			after, err := json.Marshal(execution)
			require.NoError(t, err)
			require.Equal(t, before, after, "display must preserve the original unfiltered records")
			require.Equal(t, "private-plan-name", plan.Name)
			if tc.available {
				require.NotNil(t, display.Quantity, "supported stored raw quantity must be present")
				require.Equal(t, tc.quantity, *display.Quantity)
				require.NotNil(t, display.QuantityUnit)
				require.Equal(t, tc.unit, *display.QuantityUnit)
				require.EqualValues(t, "stored_snapshot", display.QuantitySource)
			} else {
				require.Nil(t, display.Quantity, "unsafe or unproven quantity must be unavailable")
				require.Nil(t, display.QuantityUnit)
				require.EqualValues(t, "unavailable", display.QuantitySource)
			}
			require.Equal(t, tc.resource, display.ResourceType)
			require.Equal(t, tc.region, display.Region)
			raw, err := json.Marshal(display)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(raw, &fields))
			keys := make([]string, 0, len(fields))
			for key := range fields {
				keys = append(keys, key)
			}
			require.ElementsMatch(t, []string{"resource_type", "region", "quantity", "quantity_unit",
				"full_term_commitment", "commitment_currency", "quantity_source", "commitment_source"}, keys)
			require.Nil(t, fields["full_term_commitment"])
			require.Nil(t, fields["commitment_currency"])
			require.Equal(t, "unavailable", fields["commitment_source"])
			if !tc.available {
				require.Nil(t, fields["quantity"])
				require.Nil(t, fields["quantity_unit"])
			}
			for _, marker := range []string{"actor-marker", "token-marker", "lineage-marker", "details-marker", "error-marker", "private-plan-name"} {
				require.NotContains(t, string(raw), marker)
			}
		})
	}
}
