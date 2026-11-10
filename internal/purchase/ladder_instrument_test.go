package purchase

import (
	"math"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func spTranche(amount float64, term ladder.Term) config.LadderTrancheDB {
	basis, rate := config.LadderAmountCommitment, "1/2"
	return config.LadderTrancheDB{
		ID: "tr-1", LayerType: ladder.LayerComputeSP, Term: term, PaymentOption: ladder.PaymentNoUpfront,
		Status: ladder.TrancheStatusScheduled, AmountUSDHr: amount,
		AmountBasis: &basis, CommitmentRate: &rate, ScheduledDate: time.Unix(0, 0),
	}
}

func spInput(tr config.LadderTrancheDB) LadderBuildInput {
	return LadderBuildInput{Tranche: tr, CloudAccountID: "acct-a", Region: "us-west-2"}
}

func buildOne(t *testing.T, in LadderBuildInput) config.RecommendationRecord {
	t.Helper()
	recs, err := NewLadderInstrumentRegistry().Build(in)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	return recs[0]
}

func TestLadderInstrument_Layers(t *testing.T) {
	assert.Equal(t, []ladder.LayerType{ladder.LayerComputeSP}, NewLadderInstrumentRegistry().Layers())
}

func TestLadderInstrument_ComputeSPGoldenRecord(t *testing.T) {
	rec := buildOne(t, spInput(spTranche(0.123456, ladder.Term1Year)))

	assert.Equal(t, "aws", rec.Provider)
	assert.Equal(t, "savings-plans-compute", rec.Service)
	assert.Equal(t, "us-west-2", rec.Region)
	assert.Equal(t, "compute-sp", rec.ResourceType)
	assert.Equal(t, 1, rec.Count)
	assert.Equal(t, 1, rec.Term)
	assert.Equal(t, "no-upfront", rec.Payment)
	require.NotNil(t, rec.CloudAccountID)
	assert.Equal(t, "acct-a", *rec.CloudAccountID)
	assert.True(t, rec.Selected)
	assert.False(t, rec.Purchased)
	assert.Equal(t, "USD", rec.CommitmentCurrency)
	assert.Zero(t, rec.UpfrontCost)
	require.NotNil(t, rec.MonthlyCost)
	assert.InDelta(t, 90.1185, *rec.MonthlyCost, 1e-9)
	assert.InDelta(t, 1081.422, rec.TotalCommitment(), 1e-9)

	decoded, err := common.DecodeServiceDetailsFor(rec.Service, rec.Details)
	require.NoError(t, err)
	d, ok := decoded.(*common.SavingsPlanDetails)
	require.True(t, ok)
	assert.Equal(t, common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: 0.12345}, *d)
}

func TestLadderInstrument_ComputeSPThreeYearAndMaxGolden(t *testing.T) {
	rec := buildOne(t, spInput(spTranche(0.12345, ladder.Term3Year)))
	assert.Equal(t, 3, rec.Term)
	assert.InDelta(t, 90.1185, *rec.MonthlyCost, 1e-9)
	assert.InDelta(t, 3244.266, rec.TotalCommitment(), 1e-9)

	rec = buildOne(t, spInput(spTranche(1000000, ladder.Term3Year)))
	assert.InDelta(t, 1000000*8760*3, rec.TotalCommitment(), 1e-3)
	rec = buildOne(t, spInput(spTranche(1000000, ladder.Term1Year)))
	assert.InDelta(t, 8.76e9, rec.TotalCommitment(), 1e-3)
}

func TestLadderInstrument_AcceptedRowsKeepInvariants(t *testing.T) {
	for _, amount := range []float64{0.123456, 0.29, 0.001, 0.001009, 7.000009, 999999.999999, 1000000} {
		tr := spTranche(amount, ladder.Term1Year)
		rec := buildOne(t, spInput(tr))
		decoded, err := common.DecodeServiceDetailsFor(rec.Service, rec.Details)
		require.NoError(t, err)
		d := decoded.(*common.SavingsPlanDetails)
		assert.LessOrEqual(t, d.HourlyCommitment, amount, "%v", amount)
		assert.Greater(t, rec.TotalCommitment(), 0.0, "%v", amount)
		require.NotNil(t, rec.MonthlyCost)
	}
}

func TestLadderInstrument_BuildDoesNotMutateInput(t *testing.T) {
	in := spInput(spTranche(0.123456, ladder.Term1Year))
	basis, rate := *in.Tranche.AmountBasis, *in.Tranche.CommitmentRate
	want := in.Tranche
	buildOne(t, in)
	assert.Equal(t, 0.123456, in.Tranche.AmountUSDHr)
	assert.Equal(t, want, in.Tranche)
	assert.Equal(t, basis, *in.Tranche.AmountBasis)
	assert.Equal(t, rate, *in.Tranche.CommitmentRate)
	assert.Equal(t, "acct-a", in.CloudAccountID)
}

func TestLadderInstrument_FailsClosed(t *testing.T) {
	od, commit := config.LadderAmountOnDemand, config.LadderAmountCommitment
	str := func(s string) *string { return &s }
	exec := "exec-1"
	base := func() LadderBuildInput {
		return spInput(spTranche(0.5, ladder.Term1Year))
	}
	for name, mutate := range map[string]func(*LadderBuildInput){
		"empty region":        func(in *LadderBuildInput) { in.Region = "" },
		"blank region":        func(in *LadderBuildInput) { in.Region = "  " },
		"empty account":       func(in *LadderBuildInput) { in.CloudAccountID = "" },
		"fired":               func(in *LadderBuildInput) { in.Tranche.Status = ladder.TrancheStatusFired },
		"canceled":            func(in *LadderBuildInput) { in.Tranche.Status = ladder.TrancheStatusCanceled },
		"completed":           func(in *LadderBuildInput) { in.Tranche.Status = ladder.TrancheStatusCompleted },
		"has execution":       func(in *LadderBuildInput) { in.Tranche.ExecutionID = &exec },
		"nil basis":           func(in *LadderBuildInput) { in.Tranche.AmountBasis, in.Tranche.CommitmentRate = nil, nil },
		"on-demand basis":     func(in *LadderBuildInput) { in.Tranche.AmountBasis, in.Tranche.CommitmentRate = &od, nil },
		"on-demand with rate": func(in *LadderBuildInput) { in.Tranche.AmountBasis = &od },
		"nil rate":            func(in *LadderBuildInput) { in.Tranche.AmountBasis, in.Tranche.CommitmentRate = &commit, nil },
		"rate zero":           func(in *LadderBuildInput) { in.Tranche.CommitmentRate = str("0") },
		"rate text":           func(in *LadderBuildInput) { in.Tranche.CommitmentRate = str("abc") },
		"rate above one":      func(in *LadderBuildInput) { in.Tranche.CommitmentRate = str("1.5") },
		"ec2 instance sp":     func(in *LadderBuildInput) { in.Tranche.LayerType = ladder.LayerEC2InstanceSP },
		"convertible ri":      func(in *LadderBuildInput) { in.Tranche.LayerType = ladder.LayerConvertibleRI },
		"azure reservation":   func(in *LadderBuildInput) { in.Tranche.LayerType = ladder.LayerAzureReservation },
		"bogus layer":         func(in *LadderBuildInput) { in.Tranche.LayerType = "bogus" },
		"empty term":          func(in *LadderBuildInput) { in.Tranche.Term = "" },
		"2yr term":            func(in *LadderBuildInput) { in.Tranche.Term = "2yr" },
		"bare 1 term":         func(in *LadderBuildInput) { in.Tranche.Term = "1" },
		"all upfront":         func(in *LadderBuildInput) { in.Tranche.PaymentOption = ladder.PaymentAllUpfront },
		"partial upfront":     func(in *LadderBuildInput) { in.Tranche.PaymentOption = ladder.PaymentPartialUpfront },
		"empty payment":       func(in *LadderBuildInput) { in.Tranche.PaymentOption = "" },
		"display payment":     func(in *LadderBuildInput) { in.Tranche.PaymentOption = "No Upfront" },
		"upper payment":       func(in *LadderBuildInput) { in.Tranche.PaymentOption = "NO-UPFRONT" },
		"zero amount":         func(in *LadderBuildInput) { in.Tranche.AmountUSDHr = 0 },
		"negative amount":     func(in *LadderBuildInput) { in.Tranche.AmountUSDHr = -0.5 },
		"below minimum":       func(in *LadderBuildInput) { in.Tranche.AmountUSDHr = 0.000999 },
		"above maximum":       func(in *LadderBuildInput) { in.Tranche.AmountUSDHr = 1000000.000001 },
		"nan amount":          func(in *LadderBuildInput) { in.Tranche.AmountUSDHr = math.NaN() },
		"inf amount":          func(in *LadderBuildInput) { in.Tranche.AmountUSDHr = math.Inf(1) },
	} {
		t.Run(name, func(t *testing.T) {
			in := base()
			mutate(&in)
			recs, err := NewLadderInstrumentRegistry().Build(in)
			require.Error(t, err)
			assert.Empty(t, recs)
		})
	}
}

func TestLadderInstrument_AcceptsValidRates(t *testing.T) {
	for _, rate := range []string{"1", "1/2"} {
		tr := spTranche(0.5, ladder.Term1Year)
		tr.CommitmentRate = &rate
		buildOne(t, spInput(tr))
	}
}
