package api

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

func boundPlanForEdit() *config.PurchasePlan {
	return &config.PurchasePlan{
		ID: "p1", Name: "bound", Enabled: true,
		RampSchedule: config.RampSchedule{Type: "immediate", PercentPerStep: 100, TotalSteps: 1, AllocationMode: config.RampAllocationBoundGroups},
		Services: map[string]config.ServiceConfig{
			"aws/ec2#g1": {Provider: "aws", Service: "ec2", Enabled: true, Term: 3, Payment: "no-upfront", Coverage: 80,
				MinCount: 1, GroupID: "g1", BoundIdentities: []string{"aws\x1facct\x1fec2"}},
		},
	}
}

func TestMergeUpdatedPlanBoundPlanRefusesServiceFields(t *testing.T) {
	t.Parallel()
	for name, req := range map[string]*PlanRequest{
		"same service":  {Name: "bound", RampSchedule: "immediate", Enabled: true, Provider: "aws", Service: "ec2"},
		"other service": {Name: "bound", RampSchedule: "immediate", Enabled: true, Provider: "aws", Service: "rds"},
	} {
		t.Run(name, func(t *testing.T) {
			existing := boundPlanForEdit()
			_, err := mergeUpdatedPlan(existing, req, "p1", false)
			ce, ok := IsClientError(err)
			require.True(t, ok)
			require.Equal(t, 400, ce.code)
			require.Equal(t, []string{"aws\x1facct\x1fec2"}, existing.Services["aws/ec2#g1"].BoundIdentities)
		})
	}
}

func TestMergeUpdatedPlanBoundPlanAllowsPlanLevelEdit(t *testing.T) {
	t.Parallel()
	existing := boundPlanForEdit()
	plan, err := mergeUpdatedPlan(existing, &PlanRequest{Name: "renamed", RampSchedule: "immediate", Enabled: true}, "p1", false)
	require.NoError(t, err)
	require.Equal(t, "renamed", plan.Name)
	require.Equal(t, existing.Services, plan.Services)
	require.Equal(t, config.RampAllocationBoundGroups, plan.RampSchedule.AllocationMode)
}

func TestMergeUpdatedPlanUnboundPlanStillTakesServiceFields(t *testing.T) {
	t.Parallel()
	existing := boundPlanForEdit()
	existing.RampSchedule.AllocationMode = ""
	existing.Services = map[string]config.ServiceConfig{
		"aws/ec2": {Provider: "aws", Service: "ec2", Enabled: true, Term: 3, Payment: "no-upfront", Coverage: 80},
	}
	_, err := mergeUpdatedPlan(existing, &PlanRequest{Name: "x", RampSchedule: "immediate", Enabled: true, Provider: "aws", Service: "ec2"}, "p1", false)
	require.NoError(t, err)
}
