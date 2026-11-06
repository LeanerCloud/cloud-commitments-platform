package server

import (
	"context"
	"errors"
	"testing"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/testutil"
)

// observedFake adds GetObservedCommitmentRates to the ladder fake.
type observedFake struct {
	*fakeLadderCapability
	observedRates map[pkgladder.LayerType]float64
	observedErr   error
}

func (o *observedFake) GetObservedCommitmentRates(_ context.Context, _ pkgladder.Scope) (map[pkgladder.LayerType]float64, error) {
	if o.observedErr != nil {
		return nil, o.observedErr
	}
	return o.observedRates, nil
}

// singleFlexFake is a one-flex-layer fake (no base layer takes the gap) with
// low-water 25 (target 20 at 80% coverage) and a state holding existing
// commitment USD/hr on that layer. All rates here are exact in binary.
func singleFlexFake(t *testing.T, layer pkgladder.LayerType, existing float64, marginal map[pkgladder.LayerType]float64) *fakeLadderCapability {
	t.Helper()
	return &fakeLadderCapability{
		t: t, baseline: testBaselineLowWater(25), rates: marginal,
		supportedLayers: []pkgladder.LayerSpec{{Type: layer, Roles: []pkgladder.LayerRole{pkgladder.RoleFlex}}},
		layerStates: map[pkgladder.LayerType]pkgladder.LayerState{
			layer: {Layer: layer, ExistingUSDPerHour: f64(existing), ExpiringUSDPerHour: f64(0)},
		},
	}
}

func TestExecuteLadderRun_ObservedRateConvertsExistingPlannerKeepsMarginal(t *testing.T) {
	store := &ladderTestStore{}
	base := singleFlexFake(t, pkgladder.LayerEC2InstanceSP, 5, map[pkgladder.LayerType]float64{pkgladder.LayerEC2InstanceSP: 0.75})
	cap := &observedFake{fakeLadderCapability: base, observedRates: map[pkgladder.LayerType]float64{pkgladder.LayerEC2InstanceSP: 0.5}}
	require.NoError(t, runWithRates(t, store, cap))

	run := store.savedRun
	require.NotNil(t, run)
	require.NotNil(t, run.ExistingUSDHr)
	assert.InDelta(t, 10.0, *run.ExistingUSDHr, 1e-9, "5 commitment at the observed 1/2 is 10 OD (marginal would give 6.67)")
	require.Len(t, store.savedTranches, 1)
	tr := store.savedTranches[0]
	assert.InDelta(t, 7.5, tr.AmountUSDHr, 1e-9, "the gap of 10 OD is sized at the marginal 3/4")
	require.NotNil(t, tr.CommitmentRate)
	assert.Equal(t, "3/4", *tr.CommitmentRate)
}

func TestExecuteLadderRun_AbsentObservedKeyFallsBackToMarginal(t *testing.T) {
	store := &ladderTestStore{}
	base := singleFlexFake(t, pkgladder.LayerEC2InstanceSP, 5, map[pkgladder.LayerType]float64{pkgladder.LayerEC2InstanceSP: 0.5})
	cap := &observedFake{fakeLadderCapability: base, observedRates: map[pkgladder.LayerType]float64{pkgladder.LayerComputeSP: 0.75}}
	require.NoError(t, runWithRates(t, store, cap))

	require.NotNil(t, store.savedRun.ExistingUSDHr)
	assert.InDelta(t, 10.0, *store.savedRun.ExistingUSDHr, 1e-9, "no observed EC2 rate: marginal 1/2 converts")
}

func TestExecuteLadderRun_NoObservedCapabilityConvertsAtMarginal(t *testing.T) {
	store := &ladderTestStore{}
	cap := singleFlexFake(t, pkgladder.LayerEC2InstanceSP, 5, map[pkgladder.LayerType]float64{pkgladder.LayerEC2InstanceSP: 0.5})
	require.NoError(t, runWithRates(t, store, cap))

	require.NotNil(t, store.savedRun.ExistingUSDHr)
	assert.InDelta(t, 10.0, *store.savedRun.ExistingUSDHr, 1e-9)
}

// Compute has only an observed rate: its coverage converts (no
// missing_discount_rate), and the planner, with no marginal rate, holds the gap.
func TestExecuteLadderRun_ObservedOnlyLayerConvertsButPlannerHolds(t *testing.T) {
	store := &ladderTestStore{}
	base := singleFlexFake(t, pkgladder.LayerComputeSP, 2, map[pkgladder.LayerType]float64{})
	cap := &observedFake{fakeLadderCapability: base, observedRates: map[pkgladder.LayerType]float64{pkgladder.LayerComputeSP: 0.5}}
	require.NoError(t, runWithRates(t, store, cap))

	run := store.savedRun
	require.NotNil(t, run)
	plan := decodeLadderPlan(t, run.Plan)
	assert.NotEqual(t, "missing_discount_rate", plan.ReasonCode)
	require.NotNil(t, run.ExistingUSDHr)
	assert.InDelta(t, 4.0, *run.ExistingUSDHr, 1e-9, "2 commitment at the observed 1/2 is 4 OD")
	assert.Empty(t, store.savedTranches, "new tranches need a marginal rate")
	require.Len(t, plan.Actions, 1)
	assert.Equal(t, string(pkgladder.ActionHold), plan.Actions[0].Action)
	assert.Contains(t, plan.Actions[0].Rationale, "no commitment rate for layer compute-sp")
}

func TestExecuteLadderRun_LayerWithNeitherRateIsMissingHold(t *testing.T) {
	store := &ladderTestStore{}
	base := singleFlexFake(t, pkgladder.LayerComputeSP, 2, map[pkgladder.LayerType]float64{})
	cap := &observedFake{fakeLadderCapability: base, observedRates: map[pkgladder.LayerType]float64{}}
	require.NoError(t, runWithRates(t, store, cap))

	plan := decodeLadderPlan(t, store.savedRun.Plan)
	assert.Equal(t, "missing_discount_rate", plan.ReasonCode)
	require.Len(t, plan.Actions, 1)
	assert.Contains(t, plan.Actions[0].Rationale, "neither an observed rate from the commitments held nor a new-purchase rate")
	assert.Empty(t, store.savedTranches)
}

func TestExecuteLadderRun_ObservedRatesErrorFailsRun(t *testing.T) {
	store := &ladderTestStore{}
	base := singleFlexFake(t, pkgladder.LayerEC2InstanceSP, 5, map[pkgladder.LayerType]float64{pkgladder.LayerEC2InstanceSP: 0.5})
	cap := &observedFake{fakeLadderCapability: base, observedErr: errors.New("ce throttled")}
	err := runWithRates(t, store, cap)
	require.ErrorContains(t, err, "observed commitment rates")
	require.ErrorContains(t, err, "ce throttled")
	assert.Nil(t, store.savedRun)
}

func TestLadderConversionRates_NilMarginalWithObserved(t *testing.T) {
	base := singleFlexFake(t, pkgladder.LayerComputeSP, 2, nil)
	cap := &observedFake{fakeLadderCapability: base, observedRates: map[pkgladder.LayerType]float64{pkgladder.LayerComputeSP: 0.5}}
	conv, observed, err := ladderConversionRates(testutil.TestContext(t), cap, pkgladder.Scope{}, nil)
	require.NoError(t, err)
	assert.Equal(t, map[pkgladder.LayerType]float64{pkgladder.LayerComputeSP: 0.5}, conv)
	assert.Equal(t, cap.observedRates, observed)
}
