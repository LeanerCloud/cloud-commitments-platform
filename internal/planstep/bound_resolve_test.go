package planstep

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixtures are in-memory fakes, not a real database or cloud (issue #1746).

func azureRecIn(region, payment string) config.RecommendationRecord {
	r := azureRec(10, 100)
	r.Region, r.Payment = region, payment
	r.ID = "azure|acct-T|compute|" + region + "|Standard_D2s_v3||1|" + payment
	return r
}

func boundAzurePlan(entries map[string]config.ServiceConfig) *config.PurchasePlan {
	return &config.PurchasePlan{ID: "plan-b", Services: entries, RampSchedule: config.RampSchedule{
		Type: "custom", PercentPerStep: 50, TotalSteps: 2, AllocationMode: config.RampAllocationBoundGroups,
	}}
}

func azureGroup(payment string, coverage float64, ids ...string) config.ServiceConfig {
	return config.ServiceConfig{Provider: "azure", Service: "compute", Enabled: true, Term: 1, Payment: payment,
		Coverage: coverage, MinCount: 1, BoundIdentities: ids}
}

func TestResolve_BoundGroupBuysOnlyItsSelection(t *testing.T) {
	west, north := azureRecIn("westeurope", "monthly"), azureRecIn("northeurope", "monthly")
	rows := []config.RecommendationRecord{west, north}
	plan := boundAzurePlan(map[string]config.ServiceConfig{"azure/compute#g1": azureGroup("monthly", 100, cumKey(t, west))})
	res, err := Resolve(t.Context(), fakeSource{shown: rows}, fakeStore{rows: rows}, plan, accts, 1, nil, nil)
	require.NoError(t, err)
	require.Len(t, res.Recs, 1, "the matching but unselected northeurope rec is not bought")
	assert.Equal(t, "westeurope", res.Recs[0].Region)
	assert.Empty(t, res.MissingBound)
	assert.Contains(t, res.Baseline, cumKey(t, west))
	assert.NotContains(t, res.Baseline, cumKey(t, north), "an unselected rec never enters the baseline")
}

func TestResolve_BoundIdentitiesMissingFromTheStrictRead(t *testing.T) {
	west, north := azureRecIn("westeurope", "monthly"), azureRecIn("northeurope", "monthly")
	gone := azureRecIn("uksouth", "monthly")
	// Some missing: the present one is sized, the missing one is reported.
	plan := boundAzurePlan(map[string]config.ServiceConfig{"azure/compute#g1": azureGroup("monthly", 100, cumKey(t, west), cumKey(t, gone))})
	rows := []config.RecommendationRecord{west, north}
	res, err := Resolve(t.Context(), fakeSource{shown: rows}, fakeStore{rows: rows}, plan, accts, 1, nil, nil)
	require.NoError(t, err)
	require.Len(t, res.Recs, 1)
	assert.Equal(t, []string{cumKey(t, gone)}, res.MissingBound)
	// All missing: nothing to buy takes the ErrNoMatch grace path.
	plan = boundAzurePlan(map[string]config.ServiceConfig{"azure/compute#g1": azureGroup("monthly", 100, cumKey(t, gone))})
	res, err = Resolve(t.Context(), fakeSource{shown: rows}, fakeStore{rows: rows}, plan, accts, 1, nil, nil)
	require.ErrorIs(t, err, ErrNoMatch)
	assert.Equal(t, []string{cumKey(t, gone)}, res.MissingBound)
}

// targetMet judges a baselined identity only against the group that bound it.
func TestResolve_TargetMetJudgesByBoundGroup(t *testing.T) {
	monthly, upfront := azureRecIn("westeurope", "monthly"), azureRecIn("westeurope", "upfront")
	km, ku := cumKey(t, monthly), cumKey(t, upfront)
	baseline := config.PlanRampBaseline{km: {Count: 10}, ku: {Count: 10}}
	committed := map[string]config.PlanRampQuantity{km: {Count: 10}, ku: {Count: 5}}
	// Step 2 at 50%/step is cumulative 100%: g1 (coverage 100) needs 10, g2 (coverage 50) needs 5.
	bound := boundAzurePlan(map[string]config.ServiceConfig{
		"azure/compute#g1": azureGroup("monthly", 100, km),
		"azure/compute#g2": azureGroup("upfront", 50, ku),
	})
	_, err := Resolve(t.Context(), fakeSource{}, fakeStore{}, bound, accts, 2, baseline, committed)
	require.ErrorIs(t, err, ErrTargetMet, "each group is judged against its own coverage")

	// (guard) an unmarked plan with the same payment split keeps today's
	// first-entry match: the upfront key is judged at coverage 100 and is short.
	legacy := &config.PurchasePlan{ID: "plan-l", RampSchedule: config.RampSchedule{Type: "custom", PercentPerStep: 50, TotalSteps: 2},
		Services: map[string]config.ServiceConfig{
			"azure:compute":   {Provider: "azure", Service: "compute", Enabled: true, Term: 1, Payment: "monthly", Coverage: 100},
			"azure:compute-u": {Provider: "azure", Service: "compute", Enabled: true, Term: 1, Payment: "upfront", Coverage: 50},
		}}
	_, err = Resolve(t.Context(), fakeSource{}, fakeStore{}, legacy, accts, 2, baseline, committed)
	require.ErrorIs(t, err, ErrNoMatch)

	// A disabled group's identities are not judged, as for any disabled entry.
	g2 := bound.Services["azure/compute#g2"]
	g2.Enabled = false
	bound.Services["azure/compute#g2"] = g2
	short := map[string]config.PlanRampQuantity{km: {Count: 10}, ku: {Count: 0}}
	_, err = Resolve(t.Context(), fakeSource{}, fakeStore{}, bound, accts, 2, baseline, short)
	require.ErrorIs(t, err, ErrTargetMet)
	g2.Enabled = true
	bound.Services["azure/compute#g2"] = g2

	// A baselined key that no group bound is not judged, even though its
	// attributes match g1: with nothing judged the step is not met.
	other := azureRecIn("northeurope", "monthly")
	ko := cumKey(t, other)
	onlyOther := config.PlanRampBaseline{ko: {Count: 10}}
	_, err = Resolve(t.Context(), fakeSource{}, fakeStore{}, bound, accts, 2, onlyOther, map[string]config.PlanRampQuantity{ko: {Count: 10}})
	require.ErrorIs(t, err, ErrNoMatch)
}
