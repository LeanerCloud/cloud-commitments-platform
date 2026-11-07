package purchase

import (
	"errors"
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
	east, west := capRec("us-east-1", 100, ""), capRec("us-west-2", 100, "")
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
	recs := []config.RecommendationRecord{capRec("us-east-1", 100, "")}
	p := &config.PurchasePlan{AutoPurchase: true, Services: map[string]config.ServiceConfig{"aws/ec2": {Provider: "aws", Service: "ec2", Enabled: true}}}
	require.ErrorIs(t, CheckStepCommitmentCap(p, recs), ErrStepCommitmentCap)
	p.MaxStepCommitment = usd(150)
	assert.NoError(t, CheckStepCommitmentCap(p, recs))
	assert.False(t, errors.Is(CheckStepCommitmentCap(p, recs), ErrRecOutsideBoundGroups))
}
