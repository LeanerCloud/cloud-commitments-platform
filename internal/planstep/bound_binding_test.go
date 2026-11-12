package planstep

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixtures are hand-built plans, not real configuration (issue #1746).

func boundPlan(marked bool, entries map[string]config.ServiceConfig) *config.PurchasePlan {
	p := &config.PurchasePlan{ID: "plan-bound", Services: entries, RampSchedule: config.RampSchedule{Type: "weekly", PercentPerStep: 25}}
	if marked {
		p.RampSchedule.AllocationMode = config.RampAllocationBoundGroups
	}
	return p
}

func boundEntry(ids ...string) config.ServiceConfig {
	return config.ServiceConfig{Provider: "aws", Service: "ec2", Enabled: true, Coverage: 80, MinCount: 1, GroupID: "g1", BoundIdentities: ids}
}

func TestPrecheck_BoundBinding(t *testing.T) {
	for _, tc := range []struct {
		plan    *config.PurchasePlan
		name    string
		want    string // other refusal substring; "" = accepted
		missing bool   // want ErrBoundPlanMissingBinding
	}{
		{name: "marked plan with bindings and the sentinel", plan: boundPlan(true, map[string]config.ServiceConfig{"aws/ec2#g1": boundEntry("k1")}), missing: false, want: ""},
		{name: "marked plan, entry lost its binding", plan: boundPlan(true, map[string]config.ServiceConfig{"aws/ec2#g1": boundEntry()}), missing: true, want: ""},
		{name: "#-keyed entry without marker or binding", plan: boundPlan(false, map[string]config.ServiceConfig{"aws/ec2#g1": boundEntry()}), missing: true, want: ""},
		{name: "marked plan, legacy key without binding", plan: boundPlan(true, map[string]config.ServiceConfig{"aws/ec2": {Provider: "aws", Service: "ec2", Enabled: true, Coverage: 80}}), missing: true, want: ""},
		{name: "disabled entry without binding is not checked", plan: boundPlan(true, map[string]config.ServiceConfig{
			"aws/ec2#g1": boundEntry("k1"),
			"aws/rds#g2": {Provider: "aws", Service: "rds", MinCount: 1, GroupID: "g2"},
		}), missing: false, want: ""},
		{name: "sentinel other than 1 on a #-key is still a filter", plan: boundPlan(true, map[string]config.ServiceConfig{"aws/ec2#g1": withSvc(boundEntry("k1"), func(s *config.ServiceConfig) { s.MinCount = 2 })}), missing: false, want: "min_count"},
		{name: "other filters on a #-key are still refused", plan: boundPlan(true, map[string]config.ServiceConfig{"aws/ec2#g1": withSvc(boundEntry("k1"), func(s *config.ServiceConfig) { s.IncludeTypes = []string{"m5.large"} })}), missing: false, want: "include_types"},
		// (guard) a legacy key keeps today's refusal of min_count 1.
		{name: "min_count 1 on a legacy key is refused", plan: boundPlan(false, map[string]config.ServiceConfig{"aws/ec2": {Provider: "aws", Service: "ec2", Enabled: true, Coverage: 80, MinCount: 1}}), missing: false, want: "min_count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Precheck(tc.plan)
			switch {
			case tc.missing:
				require.ErrorIs(t, err, ErrBoundPlanMissingBinding)
				require.ErrorIs(t, err, ErrUnsupportedPlan, "attach must record a failed row, not retry")
				assert.ErrorContains(t, err, "plan-bound")
			case tc.want != "":
				require.ErrorIs(t, err, ErrUnsupportedPlan)
				assert.ErrorContains(t, err, tc.want)
			default:
				assert.NoError(t, err)
			}
		})
	}
}
