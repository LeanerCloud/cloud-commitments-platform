package api

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

func TestMergeUpdatedPlanPreservesUnrepresentedServiceFields(t *testing.T) {
	svc := config.ServiceConfig{Provider: "aws", Service: "ec2", Enabled: false, Term: 1, Payment: "no-upfront", Coverage: 80, RampSchedule: "custom", MinCount: 3,
		IncludeEngines: []string{}, ExcludeEngines: []string{"engine"}, IncludeRegions: []string{"region"}, ExcludeRegions: []string{}, IncludeTypes: nil, ExcludeTypes: []string{"type"}}
	existing := &config.PurchasePlan{Name: "stored", Services: map[string]config.ServiceConfig{"aws/ec2": svc}, RampSchedule: config.PresetRampSchedules["immediate"]}
	plan, err := mergeUpdatedPlan(existing, &PlanRequest{Name: "rename", Provider: "aws", Service: "ec2", Term: 1, Payment: "no-upfront", TargetCoverage: 80, RampSchedule: "immediate"}, "id", false)
	require.NoError(t, err)
	require.Equal(t, svc, plan.Services["aws/ec2"])
	require.Equal(t, svc, existing.Services["aws/ec2"])
}
