package api

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixtures are hand-built plans, not real configuration (issue #1746).

func boundResponsePlan(ids int) *config.PurchasePlan {
	bound := make([]string, ids)
	for i := range bound {
		// About the size of a real RecIdentityKey: provider, account, service,
		// region, type, engine, term, payment, AZ, platform, tenancy.
		bound[i] = fmt.Sprintf("aws\x1f3f1c2a9e-0000-4000-8000-%012d\x1fec2\x1fap-southeast-3\x1fu-24tb1.112xlarge\x1f\x1f3\x1fpartial-upfront\x1fap-southeast-3a\x1fLinux with SQL Server Standard\x1fdedicated", i)
	}
	return &config.PurchasePlan{ID: "plan-r", Name: "r",
		RampSchedule: config.RampSchedule{AllocationMode: config.RampAllocationBoundGroups},
		Services: map[string]config.ServiceConfig{"aws/ec2#g1": {
			Provider: "aws", Service: "ec2", Enabled: true, MinCount: 1, GroupID: "g1", BoundIdentities: bound,
		}}}
}

func TestPlanForResponse_ReportsCountsAndKeepsTheStoredPlan(t *testing.T) {
	stored := boundResponsePlan(5000)
	out := planForResponse(stored)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "bound_identities")
	assert.Contains(t, string(raw), `"bound_count":5000`)
	assert.Less(t, len(raw), 100*1024, "a 5000-identity plan must stay small in responses")

	// The response copy never touches the stored plan: a write after a GET
	// on the same in-memory value still has every identity.
	assert.Len(t, stored.Services["aws/ec2#g1"].BoundIdentities, 5000)
	assert.Zero(t, stored.Services["aws/ec2#g1"].BoundCount)
	stored.Name = "renamed"
	saved, err := json.Marshal(stored.Services)
	require.NoError(t, err)
	assert.Contains(t, string(saved), "bound_identities")
}

// (guard) A plan without bound identities is returned as it is.
func TestPlanForResponse_UnboundPlanIsUnchanged(t *testing.T) {
	legacy := &config.PurchasePlan{ID: "plan-l", Services: map[string]config.ServiceConfig{"aws/ec2": {Provider: "aws", Service: "ec2", Enabled: true}}}
	assert.Same(t, legacy, planForResponse(legacy))
	before, err := json.Marshal(legacy)
	require.NoError(t, err)
	after, err := json.Marshal(planForResponse(legacy))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
	assert.Nil(t, planForResponse(nil))
}

func TestPlanResponses_UseTheCountedCopy(t *testing.T) {
	stored := boundResponsePlan(3)
	h := &Handler{}
	list := h.attachPlanHealth(t.Context(), []config.PurchasePlan{*stored}, stored.CreatedAt)
	require.Len(t, list, 1)
	g := list[0].Services["aws/ec2#g1"]
	assert.Nil(t, g.BoundIdentities)
	assert.Equal(t, 3, g.BoundCount)
	assert.Len(t, stored.Services["aws/ec2#g1"].BoundIdentities, 3, "the listed plan is not modified")
}
