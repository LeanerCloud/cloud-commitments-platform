package purchase

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	sptypes "github.com/aws/aws-sdk-go-v2/service/savingsplans/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func storedEC2SP(family string, weight float64) config.RecommendationRecord {
	return storedEC2SPIn("us-west-2", family, weight)
}

func storedEC2SPIn(region, family string, weight float64) config.RecommendationRecord {
	details, _ := common.MarshalServiceDetails(&common.SavingsPlanDetails{
		PlanType: "EC2Instance", InstanceFamily: family, Region: region, HourlyCommitment: weight,
	})
	acct := "acct-a"
	return config.RecommendationRecord{
		Provider: "aws", Service: string(common.ServiceSavingsPlansEC2Instance),
		Term: 1, Payment: "no-upfront", Details: details, CloudAccountID: &acct,
	}
}

func ec2Input(amount float64, stored ...config.RecommendationRecord) LadderBuildInput {
	tr := spTranche(amount, ladder.Term1Year)
	tr.LayerType = ladder.LayerEC2InstanceSP
	return LadderBuildInput{Tranche: tr, CloudAccountID: "acct-a", Region: "us-west-2", StoredRecs: stored}
}

type ec2Part struct {
	family string
	hourly float64
}

func buildEC2(t *testing.T, in LadderBuildInput) []ec2Part {
	t.Helper()
	recs, err := NewLadderInstrumentRegistry().Build(in)
	require.NoError(t, err)
	parts := make([]ec2Part, 0, len(recs))
	for _, r := range recs {
		d, err := common.DecodeServiceDetailsFor(r.Service, r.Details)
		require.NoError(t, err)
		sp := d.(*common.SavingsPlanDetails)
		assert.Equal(t, "EC2Instance", sp.PlanType)
		assert.Equal(t, "us-west-2", sp.Region)
		assert.Equal(t, string(common.ServiceSavingsPlansEC2Instance), r.Service)
		assert.Equal(t, 1, r.Term)
		assert.Equal(t, "no-upfront", r.Payment)
		assert.True(t, r.Selected)
		parts = append(parts, ec2Part{sp.InstanceFamily, sp.HourlyCommitment})
	}
	return parts
}

func TestLadderInstrument_EC2SPSplitsByWeightFlooredToPrecision(t *testing.T) {
	in := ec2Input(1.0, storedEC2SP("m5", 3), storedEC2SP("c5", 1), storedEC2SP("r5", 1))
	parts := buildEC2(t, in)
	assert.Equal(t, []ec2Part{{"c5", 0.2}, {"m5", 0.6}, {"r5", 0.2}}, parts)
	// A repeating split floors, never rounds up: 1/3 each.
	parts = buildEC2(t, ec2Input(1.0, storedEC2SP("a", 1), storedEC2SP("b", 1), storedEC2SP("c", 1)))
	assert.Equal(t, []ec2Part{{"a", 0.33333}, {"b", 0.33333}, {"c", 0.33333}}, parts)
}

func TestLadderInstrument_EC2SPPartsNeverExceedTheTranche(t *testing.T) {
	for _, amount := range []float64{0.001, 0.0123457, 0.29, 7.77777, 123.456789} {
		parts := buildEC2(t, ec2Input(amount, storedEC2SP("a", 1.3), storedEC2SP("b", 0.7), storedEC2SP("c", 0.0000001), storedEC2SP("d", 9)))
		var sum float64
		for _, p := range parts {
			assert.GreaterOrEqual(t, p.hourly, spMinHourly)
			sum += p.hourly
		}
		assert.LessOrEqual(t, sum, amount, "amount %g", amount)
	}
}

func TestLadderInstrument_EC2SPDustMergesIntoTheLargestFamily(t *testing.T) {
	// b's share is 0.0005 (< $0.001/hr) and c's is 0.0004: both go to the largest, a.
	parts := buildEC2(t, ec2Input(1.0, storedEC2SP("a", 0.9991), storedEC2SP("b", 0.0005), storedEC2SP("c", 0.0004)))
	assert.Equal(t, []ec2Part{{"a", 1.0}}, parts)
	// The largest family is not the first one alphabetically.
	parts = buildEC2(t, ec2Input(1.0, storedEC2SP("a", 0.0004), storedEC2SP("b", 0.0006), storedEC2SP("z", 0.999)))
	assert.Equal(t, []ec2Part{{"z", 1.0}}, parts)
	// Everything is dust: one family gets the whole tranche.
	parts = buildEC2(t, ec2Input(0.001, storedEC2SP("a", 1), storedEC2SP("b", 1)))
	assert.Equal(t, []ec2Part{{"a", 0.001}}, parts)
}

func TestLadderInstrument_EC2SPAmountsComeFromTheTrancheNotTheRecs(t *testing.T) {
	small := buildEC2(t, ec2Input(2.0, storedEC2SP("a", 1), storedEC2SP("b", 1)))
	huge := buildEC2(t, ec2Input(2.0, storedEC2SP("a", 500000), storedEC2SP("b", 500000)))
	assert.Equal(t, small, huge)
	assert.Equal(t, []ec2Part{{"a", 1.0}, {"b", 1.0}}, small)
}

func TestLadderInstrument_EC2SPFailsClosedWithoutAMatchingRec(t *testing.T) {
	other := func(mut func(*config.RecommendationRecord)) config.RecommendationRecord {
		r := storedEC2SP("m5", 1)
		mut(&r)
		return r
	}
	badDetails := func(sp common.SavingsPlanDetails) config.RecommendationRecord {
		r := storedEC2SP("m5", 1)
		r.Details, _ = common.MarshalServiceDetails(&sp)
		return r
	}
	otherAcct := "acct-b"
	for name, rec := range map[string]config.RecommendationRecord{
		"other account":     other(func(r *config.RecommendationRecord) { r.CloudAccountID = &otherAcct }),
		"nil account":       other(func(r *config.RecommendationRecord) { r.CloudAccountID = nil }),
		"other region":      other(func(r *config.RecommendationRecord) { r.Region = "eu-west-1" }),
		"partial upfront":   other(func(r *config.RecommendationRecord) { r.Payment = "partial-upfront" }),
		"no payment":        other(func(r *config.RecommendationRecord) { r.Payment = "" }),
		"no details region": badDetails(common.SavingsPlanDetails{PlanType: "EC2Instance", InstanceFamily: "m5", HourlyCommitment: 1}),
		"negative weight":   badDetails(common.SavingsPlanDetails{PlanType: "EC2Instance", InstanceFamily: "m5", Region: "us-west-2", HourlyCommitment: -1}),
		"overflow weight": other(func(r *config.RecommendationRecord) {
			r.Details = json.RawMessage(`{"plan_type":"EC2Instance","instance_family":"m5","region":"us-west-2","hourly_commitment":1e999}`)
		}),
		"other term":     other(func(r *config.RecommendationRecord) { r.Term = 3 }),
		"other payment":  other(func(r *config.RecommendationRecord) { r.Payment = "all-upfront" }),
		"compute sp":     other(func(r *config.RecommendationRecord) { r.Service = string(common.ServiceSavingsPlansCompute) }),
		"other provider": other(func(r *config.RecommendationRecord) { r.Provider = "azure" }),
		"no family":      badDetails(common.SavingsPlanDetails{PlanType: "EC2Instance", Region: "us-west-2", HourlyCommitment: 1}),
		"zero weight":    badDetails(common.SavingsPlanDetails{PlanType: "EC2Instance", InstanceFamily: "m5", Region: "us-west-2"}),
		"wrong plan":     badDetails(common.SavingsPlanDetails{PlanType: "Compute", InstanceFamily: "m5", Region: "us-west-2", HourlyCommitment: 1}),
		"details region": badDetails(common.SavingsPlanDetails{PlanType: "EC2Instance", InstanceFamily: "m5", Region: "eu-west-1", HourlyCommitment: 1}),
		"bad json":       other(func(r *config.RecommendationRecord) { r.Details = json.RawMessage(`{`) }),
	} {
		t.Run(name, func(t *testing.T) {
			recs, err := NewLadderInstrumentRegistry().Build(ec2Input(1.0, rec))
			require.ErrorIs(t, err, errNoEC2SPFamily)
			assert.Nil(t, recs)
		})
	}
	_, err := NewLadderInstrumentRegistry().Build(ec2Input(1.0))
	require.ErrorIs(t, err, errNoEC2SPFamily)
}

func TestLadderInstrument_EC2SPUnusableWeightsAreIgnoredNotSpent(t *testing.T) {
	bad := func(w float64) config.RecommendationRecord { return storedEC2SP("x", w) }
	for _, w := range []float64{0, -3} {
		parts := buildEC2(t, ec2Input(0.29, storedEC2SP("good", 1), bad(w)))
		assert.Equal(t, []ec2Part{{"good", 0.29}}, parts)
	}
	// A realistic row: empty top-level region, region only in details.
	r := storedEC2SP("m5", 1)
	assert.Empty(t, r.Region)
	assert.Equal(t, []ec2Part{{"m5", 0.29}}, buildEC2(t, ec2Input(0.29, r)))
}

func TestLadderInstrument_EC2SPRefusesBadTrancheFields(t *testing.T) {
	in := ec2Input(1.0, storedEC2SP("m5", 1))
	in.Tranche.PaymentOption = ladder.PaymentAllUpfront
	_, err := NewLadderInstrumentRegistry().Build(in)
	require.Error(t, err)
	_, err = NewLadderInstrumentRegistry().Build(ec2Input(0.0005, storedEC2SP("m5", 1)))
	require.Error(t, err)
}

func TestLadderInstrument_EC2SPDeduplicatesAFamilyAndKeepsOneRecPerFamily(t *testing.T) {
	parts := buildEC2(t, ec2Input(1.0, storedEC2SP("m5", 1), storedEC2SP("m5", 1), storedEC2SP("c5", 1)))
	assert.Equal(t, []ec2Part{{"c5", 0.5}, {"m5", 0.5}}, parts)
}

func TestLadderInstrument_EC2SPExecutionVisibleThroughPrefetch(t *testing.T) {
	recs, err := NewLadderInstrumentRegistry().Build(ec2Input(1.0, storedEC2SP("m5", 2), storedEC2SP("c5", 1)))
	require.NoError(t, err)
	require.Len(t, recs, 2)
	recs[0].PurchaseID, recs[1].PurchaseID = "sp-1", "sp-2"
	execID := "exec-ec2"
	f := &visFake{
		execs: map[string]*config.PurchaseExecution{execID: {ExecutionID: execID, Status: config.StatusCompleted, Recommendations: recs}},
		plans: []sptypes.SavingsPlan{
			sp("sp-1", sptypes.SavingsPlanTypeEc2Instance, sptypes.SavingsPlanStateActive, "us-west-2"),
			sp("sp-2", sptypes.SavingsPlanTypeEc2Instance, sptypes.SavingsPlanStateActive, "us-west-2"),
		},
	}
	fired := config.LadderTrancheDB{ID: "tr-1", ExecutionID: &execID, LayerType: ladder.LayerEC2InstanceSP, Status: ladder.TrancheStatusFired}
	calls := 0
	factory := func(_ context.Context, account, region string) (SPDescriber, error) {
		calls++
		assert.Equal(t, "acct-a", account)
		assert.Equal(t, "us-west-2", region)
		return f, nil
	}
	vis := PrefetchLadderVisibility(context.Background(), f, factory, []config.LadderTrancheDB{fired})
	ok, err := vis.CommitmentVisible(context.Background(), config.LadderTrancheDB{}, execID)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, 1, calls)
	// One plan missing from the account: not visible, the netting stays pending.
	f.plans = f.plans[:1]
	vis = PrefetchLadderVisibility(context.Background(), f, factory, []config.LadderTrancheDB{fired})
	ok, _ = vis.CommitmentVisible(context.Background(), config.LadderTrancheDB{}, execID)
	assert.False(t, ok)
}
