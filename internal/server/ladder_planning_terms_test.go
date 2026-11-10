package server

import (
	"context"
	"errors"
	"math"
	"testing"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	termsPlan   = ladderTerms{term: pkgladder.Term1Year, payment: pkgladder.PaymentNoUpfront}
	termsGlobal = ladderTerms{term: pkgladder.Term3Year, payment: pkgladder.PaymentAllUpfront}
)

type rateMap = map[pkgladder.LayerType]float64

// termRecorder returns rates keyed by the requested pair and records each call.
type termRecorder struct {
	*fakeLadderCapability
	byPair map[ladderTerms]rateMap
	errs   map[ladderTerms]error
	calls  []ladderTerms
}

func (r *termRecorder) GetCommitmentRates(_ context.Context, _ pkgladder.Scope, term pkgladder.Term, payment pkgladder.PaymentOption) (map[pkgladder.LayerType]float64, error) {
	pair := ladderTerms{term: term, payment: payment}
	r.calls = append(r.calls, pair)
	if err := r.errs[pair]; err != nil {
		return nil, err
	}
	return r.byPair[pair], nil
}

func TestLowerOfTwoRates(t *testing.T) {
	ri, ec2, comp := pkgladder.LayerConvertibleRI, pkgladder.LayerEC2InstanceSP, pkgladder.LayerComputeSP
	held := map[pkgladder.LayerType]bool{ri: true, ec2: true, comp: true}
	tests := []struct {
		name        string
		sizing, ref rateMap
		held        map[pkgladder.LayerType]bool
		want        rateMap
	}{
		{"ref lower", rateMap{ri: 0.75}, rateMap{ri: 0.5}, held, rateMap{ri: 0.5}},
		{"sizing lower", rateMap{ri: 0.5}, rateMap{ri: 0.75}, held, rateMap{ri: 0.5}},
		{"equal", rateMap{ri: 0.625}, rateMap{ri: 0.625}, held, rateMap{ri: 0.625}},
		{"only in sizing", rateMap{ri: 0.75}, rateMap{}, held, rateMap{}},
		{"only in ref", rateMap{}, rateMap{ri: 0.75}, held, rateMap{}},
		{"mixed", rateMap{ri: 0.75, ec2: 0.5, comp: 0.625}, rateMap{ri: 0.5, ec2: 0.75, comp: 0.625, pkgladder.LayerType("x"): 1}, held, rateMap{ri: 0.5, ec2: 0.5, comp: 0.625}},
		{"nil sizing", nil, rateMap{ri: 0.5}, held, rateMap{}},
		{"nil ref", rateMap{ri: 0.5}, nil, held, rateMap{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := lowerOfTwoRates(tt.sizing, tt.ref, tt.held)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLowerOfTwoRates_InvalidRates(t *testing.T) {
	ri := pkgladder.LayerConvertibleRI
	for _, bad := range []float64{0, -0.1, math.NaN(), math.Inf(1), 1.3} {
		for _, onRef := range []bool{true, false} {
			sizing, ref := rateMap{ri: 0.75}, rateMap{ri: 0.5}
			if onRef {
				ref[ri] = bad
			} else {
				sizing[ri] = bad
			}
			_, err := lowerOfTwoRates(sizing, ref, map[pkgladder.LayerType]bool{ri: true})
			require.ErrorContains(t, err, string(ri), "bad=%v onRef=%v", bad, onRef)

			got, err := lowerOfTwoRates(sizing, ref, nil)
			require.NoError(t, err, "a layer without holdings is never converted")
			assert.Contains(t, got, ri)
		}
	}
}

func TestLowerOfTwoRates_DoesNotMutateInputs(t *testing.T) {
	ri := pkgladder.LayerConvertibleRI
	sizing, ref := rateMap{ri: 0.75}, rateMap{ri: 0.5}
	_, err := lowerOfTwoRates(sizing, ref, map[pkgladder.LayerType]bool{ri: true})
	require.NoError(t, err)
	assert.Equal(t, rateMap{ri: 0.75}, sizing)
	assert.Equal(t, rateMap{ri: 0.5}, ref)
}

func TestHeldLayers(t *testing.T) {
	one, zero := 1.0, 0.0
	got := heldLayers(map[pkgladder.LayerType]pkgladder.LayerState{
		pkgladder.LayerConvertibleRI: {ExistingUSDPerHour: &one},
		pkgladder.LayerEC2InstanceSP: {ExpiringUSDPerHour: &one},
		pkgladder.LayerComputeSP:     {ExistingUSDPerHour: &zero},
	})
	assert.Equal(t, map[pkgladder.LayerType]bool{pkgladder.LayerConvertibleRI: true, pkgladder.LayerEC2InstanceSP: true}, got)
}

func TestLadderRates(t *testing.T) {
	ri := pkgladder.LayerConvertibleRI
	newRec := func() *termRecorder {
		return &termRecorder{
			fakeLadderCapability: &fakeLadderCapability{},
			byPair:               map[ladderTerms]rateMap{termsPlan: {ri: 0.75}, termsGlobal: {ri: 0.5}},
			errs:                 map[ladderTerms]error{},
		}
	}
	held := heldState(ri)
	ctx := context.Background()

	t.Run("equal pairs fetch once", func(t *testing.T) {
		rec := newRec()
		sizing, base, err := ladderRates(ctx, rec, pkgladder.Scope{}, termsPlan, termsPlan, held)
		require.NoError(t, err)
		assert.Equal(t, []ladderTerms{termsPlan}, rec.calls)
		assert.Equal(t, rateMap{ri: 0.75}, sizing)
		assert.Equal(t, sizing, base)
	})
	t.Run("different pairs fetch plan then global", func(t *testing.T) {
		rec := newRec()
		sizing, base, err := ladderRates(ctx, rec, pkgladder.Scope{}, termsPlan, termsGlobal, held)
		require.NoError(t, err)
		assert.Equal(t, []ladderTerms{termsPlan, termsGlobal}, rec.calls)
		assert.Equal(t, rateMap{ri: 0.75}, sizing)
		assert.Equal(t, rateMap{ri: 0.5}, base)
	})
	t.Run("reference error", func(t *testing.T) {
		rec := newRec()
		rec.errs[termsGlobal] = errors.New("boom")
		sizing, base, err := ladderRates(ctx, rec, pkgladder.Scope{}, termsPlan, termsGlobal, held)
		require.ErrorContains(t, err, "boom")
		assert.Nil(t, sizing)
		assert.Nil(t, base)
	})
	t.Run("sizing error skips reference", func(t *testing.T) {
		rec := newRec()
		rec.errs[termsPlan] = errors.New("boom")
		_, _, err := ladderRates(ctx, rec, pkgladder.Scope{}, termsPlan, termsGlobal, held)
		require.ErrorContains(t, err, "boom")
		assert.Equal(t, []ladderTerms{termsPlan}, rec.calls)
	})
	t.Run("no rate capability", func(t *testing.T) {
		_, _, err := ladderRates(ctx, noRatesCapability{}, pkgladder.Scope{}, termsPlan, termsGlobal, held)
		require.ErrorContains(t, err, "does not provide commitment rates")
	})
}

// heldState builds layer states where only layer holds commitment.
func heldState(layer pkgladder.LayerType) map[pkgladder.LayerType]pkgladder.LayerState {
	v := 1.0
	return map[pkgladder.LayerType]pkgladder.LayerState{layer: {ExistingUSDPerHour: &v}}
}

// noRatesCapability implements LadderCapability without CommitmentRateCapability.
type noRatesCapability struct{ pkgladder.LadderCapability }

func TestMissingRateRationale(t *testing.T) {
	m := ladderMissingRate{layer: pkgladder.LayerConvertibleRI}

	same := m.rationale(termsPlan, termsPlan)
	assert.Contains(t, same, "1yr no-upfront")
	assert.Contains(t, same, "neither an observed rate from the commitments held nor a new-purchase rate")
	assert.NotContains(t, same, "both")

	diff := m.rationale(termsPlan, termsGlobal)
	for _, want := range []string{"1yr no-upfront", "3yr all-upfront", "both", "lower of the two"} {
		assert.Contains(t, diff, want)
	}
}
