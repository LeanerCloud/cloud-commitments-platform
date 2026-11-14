package purchase

import (
	"errors"
	"math"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixtures are hand-built plans and recs, not real configuration (issue #1746).

func capRec(region string, upfront float64, currency string) config.RecommendationRecord {
	acct := "acct-1"
	return config.RecommendationRecord{
		ID: "aws|acct-1|ec2|" + region, Provider: "aws", Service: "ec2", Region: region, ResourceType: "m5.large",
		Term: 1, Payment: "all-upfront", Count: 1, UpfrontCost: upfront, CloudAccountID: &acct, CommitmentCurrency: currency,
	}
}

func identity(t *testing.T, r config.RecommendationRecord) string {
	t.Helper()
	k, err := config.RecIdentityKey(&r)
	require.NoError(t, err)
	return k
}

func boundCapPlan(auto bool, planCap *float64, groups map[string]config.ServiceConfig) *config.PurchasePlan {
	return &config.PurchasePlan{ID: "plan-c", Enabled: true, AutoPurchase: auto, MaxStepCommitment: planCap,
		RampSchedule: config.RampSchedule{AllocationMode: config.RampAllocationBoundGroups}, Services: groups}
}

func group(cap *float64, ids ...string) config.ServiceConfig {
	return config.ServiceConfig{Provider: "aws", Service: "ec2", Enabled: true, MinCount: 1, BoundIdentities: ids, MaxStepCommitment: cap}
}

func usd(v float64) *float64 { return &v }

func TestCheckStepCommitmentCap_BoundPlans(t *testing.T) {
	east, west := capRec("us-east-1", 100, "USD"), capRec("us-west-2", 100, "USD")
	ke, kw := identity(t, east), identity(t, west)
	recs := []config.RecommendationRecord{east, west}

	t.Run("a group over its own cap is refused, naming the group", func(t *testing.T) {
		p := boundCapPlan(true, usd(1000), map[string]config.ServiceConfig{"aws/ec2#g1": group(usd(150), ke, kw)})
		err := CheckStepCommitmentCap(p, recs)
		require.ErrorIs(t, err, ErrStepCommitmentCap)
		assert.ErrorContains(t, err, "aws/ec2#g1")
	})
	t.Run("groups within their caps and the plan cap pass", func(t *testing.T) {
		p := boundCapPlan(true, usd(1000), map[string]config.ServiceConfig{
			"aws/ec2#g1": group(usd(150), ke), "aws/ec2#g2": group(usd(150), kw),
		})
		assert.NoError(t, CheckStepCommitmentCap(p, recs))
	})
	t.Run("the plan cap still applies to the total", func(t *testing.T) {
		p := boundCapPlan(true, usd(150), map[string]config.ServiceConfig{
			"aws/ec2#g1": group(usd(150), ke), "aws/ec2#g2": group(usd(150), kw),
		})
		require.ErrorIs(t, CheckStepCommitmentCap(p, recs), ErrStepCommitmentCap)
	})
	t.Run("a rec in no group's bound set is refused", func(t *testing.T) {
		p := boundCapPlan(true, usd(1000), map[string]config.ServiceConfig{"aws/ec2#g1": group(nil, ke)})
		err := CheckStepCommitmentCap(p, recs)
		require.ErrorIs(t, err, ErrRecOutsideBoundGroups)
		require.ErrorIs(t, err, ErrStepCommitmentCap, "refused like any cap failure at every site")
	})
	t.Run("an auto plan with no plan cap is refused even with group caps", func(t *testing.T) {
		p := boundCapPlan(true, nil, map[string]config.ServiceConfig{"aws/ec2#g1": group(usd(500), ke, kw)})
		require.ErrorIs(t, CheckStepCommitmentCap(p, recs), ErrStepCommitmentCap)
	})
	t.Run("a manual plan with no caps only checks the mapping", func(t *testing.T) {
		nonUSD := capRec("eu-west-1", 100, "EUR")
		p := boundCapPlan(false, nil, map[string]config.ServiceConfig{"aws/ec2#g1": group(nil, ke, identity(t, nonUSD))})
		assert.NoError(t, CheckStepCommitmentCap(p, []config.RecommendationRecord{east, nonUSD}))
		p.Services["aws/ec2#g1"] = group(nil, ke)
		require.ErrorIs(t, CheckStepCommitmentCap(p, []config.RecommendationRecord{east, west}), ErrRecOutsideBoundGroups)
	})
	t.Run("a manual plan with no plan cap still checks group caps", func(t *testing.T) {
		p := boundCapPlan(false, nil, map[string]config.ServiceConfig{"aws/ec2#g1": group(usd(150), ke, kw)})
		require.ErrorIs(t, CheckStepCommitmentCap(p, recs), ErrStepCommitmentCap)
		nonUSD := capRec("eu-west-1", 10, "EUR")
		p.Services["aws/ec2#g1"] = group(usd(150), identity(t, nonUSD))
		require.ErrorIs(t, CheckStepCommitmentCap(p, []config.RecommendationRecord{nonUSD}), ErrStepCommitmentCap, "a capped group still needs USD prices")
	})
}

// Issue #1427: a bound plan's caps are USD too, so a rec whose currency is
// unknown is refused wherever a cap applies to it, and ignored where none does.
func TestCheckStepCommitmentCap_BoundEmptyCurrency(t *testing.T) {
	unknown := capRec("us-east-1", 100, "")
	k := identity(t, unknown)
	recs := []config.RecommendationRecord{unknown}

	t.Run("a plan cap refuses it", func(t *testing.T) {
		p := boundCapPlan(true, usd(1000), map[string]config.ServiceConfig{"aws/ec2#g1": group(nil, k)})
		require.ErrorIs(t, CheckStepCommitmentCap(p, recs), ErrStepCommitmentCap)
	})
	t.Run("a group cap refuses it", func(t *testing.T) {
		p := boundCapPlan(false, nil, map[string]config.ServiceConfig{"aws/ec2#g1": group(usd(500), k)})
		require.ErrorIs(t, CheckStepCommitmentCap(p, recs), ErrStepCommitmentCap)
	})
	t.Run("no cap applies, so it is not compared", func(t *testing.T) {
		p := boundCapPlan(false, nil, map[string]config.ServiceConfig{"aws/ec2#g1": group(nil, k)})
		assert.NoError(t, CheckStepCommitmentCap(p, recs))
	})
}

func TestStepCapApplies(t *testing.T) {
	cap := usd(100)
	assert.False(t, StepCapApplies(&config.PurchasePlan{}), "(guard) a manual uncapped plan skips the check, as before")
	assert.True(t, StepCapApplies(&config.PurchasePlan{AutoPurchase: true}))
	assert.True(t, StepCapApplies(&config.PurchasePlan{MaxStepCommitment: cap}))
	assert.True(t, StepCapApplies(&config.PurchasePlan{RampSchedule: config.RampSchedule{AllocationMode: config.RampAllocationBoundGroups}}),
		"a bound plan always runs the mapping check")
	assert.False(t, StepCapApplies(nil))
}

// (guard) An unmarked plan keeps today's rules exactly.
func TestCheckStepCommitmentCap_UnmarkedUnchanged(t *testing.T) {
	recs := []config.RecommendationRecord{capRec("us-east-1", 100, "USD")}
	p := &config.PurchasePlan{AutoPurchase: true, Services: map[string]config.ServiceConfig{"aws/ec2": {Provider: "aws", Service: "ec2", Enabled: true}}}
	require.ErrorIs(t, CheckStepCommitmentCap(p, recs), ErrStepCommitmentCap)
	p.MaxStepCommitment = usd(150)
	assert.NoError(t, CheckStepCommitmentCap(p, recs))
	assert.False(t, errors.Is(CheckStepCommitmentCap(p, recs), ErrRecOutsideBoundGroups))
}

// A step exactly at a cap passes, as for an unbound plan; one cent over is
// refused. The same holds for the plan cap and for a group cap.
func TestCheckStepCommitmentCap_BoundCapBoundary(t *testing.T) {
	rec := capRec("us-east-1", 100, "USD")
	recs := []config.RecommendationRecord{rec}
	key := identity(t, rec)
	for _, tc := range []struct {
		name    string
		plan    func(cap float64) *config.PurchasePlan
		wantErr bool
	}{
		{"plan cap", func(c float64) *config.PurchasePlan {
			return boundCapPlan(true, usd(c), map[string]config.ServiceConfig{"aws/ec2#g1": group(nil, key)})
		}, false},
		{"group cap", func(c float64) *config.PurchasePlan {
			return boundCapPlan(true, usd(1e9), map[string]config.ServiceConfig{"aws/ec2#g1": group(usd(c), key)})
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.NoError(t, CheckStepCommitmentCap(tc.plan(100.01), recs), "the step is one cent under the cap")
			assert.NoError(t, CheckStepCommitmentCap(tc.plan(100), recs), "exactly at the cap passes")
			require.ErrorIs(t, CheckStepCommitmentCap(tc.plan(99.99), recs), ErrStepCommitmentCap, "one cent over is refused")
		})
	}
}

// A group cap that is not a usable positive finite number refuses the step, as
// the plan cap does (planCapUsable), instead of silently turning the cap off.
func TestCheckStepCommitmentCap_BoundGroupCapUnusableFailsClosed(t *testing.T) {
	rec := capRec("us-east-1", 100, "USD")
	recs := []config.RecommendationRecord{rec}
	key := identity(t, rec)
	for name, c := range map[string]float64{"NaN": math.NaN(), "+Inf": math.Inf(1), "zero": 0, "negative": -5} {
		t.Run(name, func(t *testing.T) {
			p := boundCapPlan(true, usd(1e9), map[string]config.ServiceConfig{"aws/ec2#g1": group(usd(c), key)})
			err := CheckStepCommitmentCap(p, recs)
			require.ErrorIs(t, err, ErrStepCommitmentCap)
			assert.ErrorContains(t, err, "aws/ec2#g1")
			assert.ErrorContains(t, err, "no usable max_step_commitment", "refused as unusable, not as an over-cap step")
		})
	}
}

// A rec's commitment is upfront plus monthly * term * 12, so a cap that only
// fits the upfront part must refuse it, and a cap equal to the whole passes.
func TestCheckStepCommitmentCap_BoundCountsMonthlyCost(t *testing.T) {
	rec := capRec("us-east-1", 50, "USD")
	monthly := 10.0
	rec.MonthlyCost = &monthly
	rec.Payment = "partial-upfront"
	require.InDelta(t, 170.0, rec.TotalCommitment(), 0.001, "50 upfront + 10 * 12 months")
	recs := []config.RecommendationRecord{rec}
	key := identity(t, rec)
	t.Run("group cap", func(t *testing.T) {
		p := boundCapPlan(true, usd(1e9), map[string]config.ServiceConfig{"aws/ec2#g1": group(usd(150), key)})
		require.ErrorIs(t, CheckStepCommitmentCap(p, recs), ErrStepCommitmentCap, "150 fits the upfront 50 but not the 170 total")
		p.Services["aws/ec2#g1"] = group(usd(170), key)
		assert.NoError(t, CheckStepCommitmentCap(p, recs))
	})
	t.Run("plan cap", func(t *testing.T) {
		p := boundCapPlan(true, usd(150), map[string]config.ServiceConfig{"aws/ec2#g1": group(nil, key)})
		require.ErrorIs(t, CheckStepCommitmentCap(p, recs), ErrStepCommitmentCap)
		p.MaxStepCommitment = usd(170)
		assert.NoError(t, CheckStepCommitmentCap(p, recs))
	})
}

// A group with no cap of its own is held only by the plan cap: its recs count
// toward the plan total, and it adds no group limit.
func TestCheckStepCommitmentCap_BoundNilGroupCapUnderAPlanCap(t *testing.T) {
	east, west := capRec("us-east-1", 100, "USD"), capRec("us-west-2", 100, "USD")
	ke, kw := identity(t, east), identity(t, west)
	recs := []config.RecommendationRecord{east, west}
	p := boundCapPlan(true, usd(150), map[string]config.ServiceConfig{
		"aws/ec2#g1": group(nil, ke), "aws/ec2#g2": group(usd(150), kw),
	})
	require.ErrorIs(t, CheckStepCommitmentCap(p, recs), ErrStepCommitmentCap, "the nil-cap group's 100 counts toward the plan total of 200")
	p.MaxStepCommitment = usd(200)
	assert.NoError(t, CheckStepCommitmentCap(p, recs), "and it is not itself group-capped")
	p.MaxStepCommitment = usd(1e9)
	assert.NoError(t, CheckStepCommitmentCap(p, []config.RecommendationRecord{east}), "a nil group cap limits nothing alone")
}

// The resolver skips a disabled group, so a rec bound only to one is outside
// every group here too, instead of being held to a cap that never applies.
func TestCheckStepCommitmentCap_BoundDisabledGroupIsOutside(t *testing.T) {
	rec := capRec("us-east-1", 100, "USD")
	key := identity(t, rec)
	disabled := group(usd(1e9), key)
	disabled.Enabled = false
	p := boundCapPlan(true, usd(1e9), map[string]config.ServiceConfig{"aws/ec2#g1": disabled})
	require.ErrorIs(t, CheckStepCommitmentCap(p, []config.RecommendationRecord{rec}), ErrRecOutsideBoundGroups)
}

// An identity bound by two groups is refused, not assigned to whichever group
// the map visits last (validation refuses it, but a stored plan may hold one).
func TestCheckStepCommitmentCap_BoundIdentityInTwoGroupsIsRefused(t *testing.T) {
	rec := capRec("us-east-1", 100, "USD")
	key := identity(t, rec)
	p := boundCapPlan(true, usd(1e9), map[string]config.ServiceConfig{
		"aws/ec2#g1": group(usd(1e9), key), "aws/ec2#g2": group(usd(1e9), key),
	})
	for i := 0; i < 20; i++ {
		err := CheckStepCommitmentCap(p, []config.RecommendationRecord{rec})
		require.ErrorIs(t, err, ErrStepCommitmentCap)
		assert.ErrorContains(t, err, `"aws/ec2#g1" and "aws/ec2#g2"`, "the same pair is named every time")
	}
}

// The group-level refusals reach the History note with their group key, which
// is closed text (a key and amounts), so the operator sees which group failed.
// A rec id carries an account number, so "outside every group" keeps only the
// fixed head.
func TestCheckStepCommitmentCap_BoundRefusalNotesKeepTheGroup(t *testing.T) {
	rec := capRec("us-east-1", 100, "USD")
	key := identity(t, rec)
	recs := []config.RecommendationRecord{rec}
	for name, plan := range map[string]*config.PurchasePlan{
		"over its cap":   boundCapPlan(true, usd(1e9), map[string]config.ServiceConfig{"aws/ec2#g1": group(usd(50), key)}),
		"NaN cap":        boundCapPlan(true, usd(1e9), map[string]config.ServiceConfig{"aws/ec2#g1": group(usd(math.NaN()), key)}),
		"negative cap":   boundCapPlan(true, usd(1e9), map[string]config.ServiceConfig{"aws/ec2#g1": group(usd(-5), key)}),
		"infinite cap":   boundCapPlan(true, usd(1e9), map[string]config.ServiceConfig{"aws/ec2#g1": group(usd(math.Inf(1)), key)}),
		"huge negative":  boundCapPlan(true, usd(1e9), map[string]config.ServiceConfig{"aws/ec2#g1": group(usd(-1e21), key)}),
		"in two groups":  boundCapPlan(true, usd(1e9), map[string]config.ServiceConfig{"aws/ec2#g1": group(nil, key), "aws/ec2#g2": group(nil, key)}),
		"plan cap total": boundCapPlan(true, usd(50), map[string]config.ServiceConfig{"aws/ec2#g1": group(nil, key)}),
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckStepCommitmentCap(plan, recs)
			require.ErrorIs(t, err, ErrStepCommitmentCap)
			assert.Equal(t, err.Error(), SanitizeStoredError(err.Error()), "a closed note is shown whole")
		})
	}
	outside := CheckStepCommitmentCap(boundCapPlan(true, usd(1e9), map[string]config.ServiceConfig{"aws/ec2#g1": group(nil, "other")}), recs)
	require.ErrorIs(t, outside, ErrRecOutsideBoundGroups)
	assert.Equal(t, "plan step refused by commitment cap", SanitizeStoredError(outside.Error()), "the rec id and its account stay out of History")
	t.Run("an odd group key is not shown", func(t *testing.T) {
		p := boundCapPlan(true, usd(1e9), map[string]config.ServiceConfig{"aws/ec2#team 7; drop": group(usd(50), key)})
		err := CheckStepCommitmentCap(p, recs)
		require.ErrorIs(t, err, ErrStepCommitmentCap)
		shown := SanitizeStoredError(err.Error())
		assert.NotContains(t, shown, "team 7")
		assert.NotContains(t, shown, "drop")
	})
}
