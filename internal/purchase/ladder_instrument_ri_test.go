package purchase

import (
	"errors"
	"math"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func storedRI(instance string, monthly float64) config.RecommendationRecord {
	d, _ := common.MarshalServiceDetails(&common.ComputeDetails{InstanceType: instance, Platform: "Linux/UNIX", Tenancy: "default", Scope: "regional"})
	acct := "acct-a"
	return config.RecommendationRecord{
		Provider: "aws", Service: "ec2", Region: "us-west-2", ResourceType: instance, Details: d,
		Count: 7, Term: 1, Payment: "no-upfront", MonthlyCost: &monthly, CloudAccountID: &acct,
	}
}

func riKeyFor(instance string) string {
	return instance + "|Linux/UNIX|default|regional"
}

func riInput(amount float64, units map[string]float64, stored ...config.RecommendationRecord) LadderBuildInput {
	in := spInput(spTranche(amount, ladder.Term1Year))
	in.Tranche.LayerType = ladder.LayerConvertibleRI
	in.StoredRecs, in.RIUnits = stored, map[string]RIUnit{}
	for k, v := range units {
		in.RIUnits[riKeyFor(k)] = RIUnit{Hourly: v}
	}
	return in
}

func riCounts(t *testing.T, in LadderBuildInput) map[string]int {
	t.Helper()
	recs, err := NewLadderInstrumentRegistry().Build(in)
	require.NoError(t, err)
	out := map[string]int{}
	var spent float64
	for _, r := range recs {
		assert.Equal(t, "ec2", r.Service)
		assert.Equal(t, "us-west-2", r.Region)
		assert.Equal(t, 1, r.Term)
		assert.Equal(t, "no-upfront", r.Payment)
		assert.True(t, r.Selected)
		require.NotNil(t, r.MonthlyCost)
		assert.InDelta(t, float64(r.Count)*in.RIUnits[riKeyFor(riTypeOf(t, r))].Hourly*common.HoursPerMonth, *r.MonthlyCost, 1e-9)
		assert.Equal(t, "acct-a", *r.CloudAccountID)
		d, err := common.DecodeServiceDetailsFor(r.Service, r.Details)
		require.NoError(t, err)
		cd := d.(*common.ComputeDetails)
		assert.Equal(t, "regional", cd.Scope)
		out[cd.InstanceType] = r.Count
		spent += float64(r.Count) * in.RIUnits[riKeyFor(cd.InstanceType)].Hourly
	}
	assert.LessOrEqual(t, spent, in.Tranche.AmountUSDHr, "the reservations never cost more than the tranche")
	return out
}

func TestBuildConvertibleRI_FloorsTheCount(t *testing.T) {
	for _, tc := range []struct {
		amount float64
		want   int
	}{
		{0.1, 1}, {0.1999, 1}, {0.3, 2}, {0.35, 3}, {1.0, 10}, {25.5, 255},
	} {
		got := riCounts(t, riInput(tc.amount, map[string]float64{"m5.large": 0.1}, storedRI("m5.large", 50)))
		assert.Equal(t, map[string]int{"m5.large": tc.want}, got, "amount %g", tc.amount)
	}
}

func TestBuildConvertibleRI_SplitsByWeightAndSkipsDust(t *testing.T) {
	units := map[string]float64{"m5.large": 0.1, "c5.large": 0.2}
	got := riCounts(t, riInput(1.0, units, storedRI("m5.large", 30), storedRI("c5.large", 10)))
	assert.Equal(t, map[string]int{"m5.large": 7, "c5.large": 1}, got)
	got = riCounts(t, riInput(0.5, units, storedRI("m5.large", 30), storedRI("c5.large", 10)))
	assert.Equal(t, map[string]int{"m5.large": 3}, got, "a share under one reservation buys nothing and is not given to another shape")
}

func TestBuildConvertibleRI_FailsClosed(t *testing.T) {
	good := map[string]float64{"m5.large": 0.1}
	bad := func(mut func(*config.RecommendationRecord)) LadderBuildInput {
		r := storedRI("m5.large", 50)
		mut(&r)
		return riInput(1, good, r)
	}
	other, nan := "acct-b", math.NaN()
	cases := map[string]LadderBuildInput{
		"no stored rec":      riInput(1, good),
		"too small for one":  riInput(0.0999, good, storedRI("m5.large", 50)),
		"no price":           riInput(1, nil, storedRI("m5.large", 50)),
		"zero price":         riInput(1, map[string]float64{"m5.large": 0}, storedRI("m5.large", 50)),
		"negative price":     riInput(1, map[string]float64{"m5.large": -0.1}, storedRI("m5.large", 50)),
		"NaN price":          riInput(1, map[string]float64{"m5.large": nan}, storedRI("m5.large", 50)),
		"Inf price":          riInput(1, map[string]float64{"m5.large": math.Inf(1)}, storedRI("m5.large", 50)),
		"one shape unpriced": riInput(1, good, storedRI("m5.large", 50), storedRI("c5.large", 10)),
		"huge count":         riInput(1000000, map[string]float64{"m5.large": 0.0001}, storedRI("m5.large", 50)),
		"other account":      bad(func(r *config.RecommendationRecord) { r.CloudAccountID = &other }),
		"nil account":        bad(func(r *config.RecommendationRecord) { r.CloudAccountID = nil }),
		"other region":       bad(func(r *config.RecommendationRecord) { r.Region = "eu-west-1" }),
		"3y rec":             bad(func(r *config.RecommendationRecord) { r.Term = 3 }),
		"partial rec":        bad(func(r *config.RecommendationRecord) { r.Payment = "partial-upfront" }),
		"nil weight":         bad(func(r *config.RecommendationRecord) { r.MonthlyCost = nil }),
		"NaN weight":         bad(func(r *config.RecommendationRecord) { r.MonthlyCost = &nan }),
		"wrong service":      bad(func(r *config.RecommendationRecord) { r.Service = "rds" }),
		"zonal": bad(func(r *config.RecommendationRecord) {
			r.Details = []byte(`{"instance_type":"m5.large","platform":"Linux/UNIX","tenancy":"default","scope":"zonal"}`)
		}),
		"no platform": bad(func(r *config.RecommendationRecord) {
			r.Details = []byte(`{"instance_type":"m5.large","tenancy":"default","scope":"regional"}`)
		}),
	}
	partial := riInput(1, good, storedRI("m5.large", 50))
	partial.Tranche.PaymentOption = ladder.PaymentPartialUpfront
	cases["partial tranche"] = partial
	threeYear := riInput(1, good, storedRI("m5.large", 50))
	threeYear.Tranche.Term = ladder.Term3Year
	cases["3y tranche"] = threeYear
	failed := riInput(1, good, storedRI("m5.large", 50))
	failed.RIUnits[riKeyFor("m5.large")] = RIUnit{Hourly: 0.1, Err: errors.New("boom")}
	cases["unit error"] = failed
	for name, in := range cases {
		recs, err := NewLadderInstrumentRegistry().Build(in)
		assert.Error(t, err, name)
		assert.Empty(t, recs, name)
	}
}

func TestRegistry_ListsTheConvertibleRILayer(t *testing.T) {
	assert.Contains(t, NewLadderInstrumentRegistry().Layers(), ladder.LayerConvertibleRI)
}

// 15 * 0.13 is 1.9500000000000002 although 1.95 / 0.13 divides to exactly 15.
func TestBuildConvertibleRI_DivisionRoundingUpNeverOverbuys(t *testing.T) {
	got := riCounts(t, riInput(1.95, map[string]float64{"m5.large": 0.13}, storedRI("m5.large", 50)))
	assert.Equal(t, map[string]int{"m5.large": 14}, got)
}

func TestBuildConvertibleRI_DedupesByMaxWeightAndIgnoresZonalTwins(t *testing.T) {
	units := map[string]float64{"m5.large": 0.1, "c5.large": 0.2}
	got := riCounts(t, riInput(1.0, units, storedRI("m5.large", 30), storedRI("m5.large", 10), storedRI("c5.large", 10)))
	assert.Equal(t, map[string]int{"m5.large": 7, "c5.large": 1}, got)

	zonal := storedRI("m5.large", 999)
	zonal.Details = []byte(`{"instance_type":"m5.large","platform":"Linux/UNIX","tenancy":"default","scope":"zonal","availability_zone":"us-west-2a"}`)
	in := riInput(1.0, map[string]float64{"m5.large": 0.1}, storedRI("m5.large", 10), zonal)
	in.RIUnits["m5.large|Linux/UNIX|default|zonal"] = RIUnit{Hourly: 0.1}
	assert.Equal(t, map[string]int{"m5.large": 10}, riCounts(t, in))
}

func riTypeOf(t *testing.T, r config.RecommendationRecord) string {
	t.Helper()
	d, err := common.DecodeServiceDetailsFor(r.Service, r.Details)
	require.NoError(t, err)
	return d.(*common.ComputeDetails).InstanceType
}

// The notice states the spend from the record's monthly cost, so it must equal
// what the reservations cost per hour.
func TestBuildConvertibleRI_NoticeStatesTheBoughtHourlySpend(t *testing.T) {
	in := riInput(1.95, map[string]float64{"m5.large": 0.13}, storedRI("m5.large", 50))
	recs, err := NewLadderInstrumentRegistry().Build(in)
	require.NoError(t, err)
	facts, err := ladderNoticeFactsFor(&in.Tranche, &config.PurchaseExecution{Recommendations: recs}, &config.CloudAccount{ExternalID: "x"})
	require.NoError(t, err)
	assert.InDelta(t, 14*0.13, facts.HourlyCommitment, 1e-9)
}
