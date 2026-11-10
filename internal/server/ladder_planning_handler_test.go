package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/testutil"
)

const termsTestAccount = "123456789012"

// termsFixture is one handleLadderRun scenario. Rates are dyadic so every
// golden value is exact. The target is 10 on-demand USD/hr (low-water 12.5 at
// 80% coverage) on a single flex Convertible RI layer.
type termsFixture struct {
	globalYears   int
	globalPayment string
	planRI        *float64 // RI rate at the constant pair; nil is absent
	globalRI      *float64 // RI rate at the global pair; nil is absent
	existing      float64
	observed      rateMap
	inFlight      []config.LadderInFlightRow
	globalErr     error
}

func (f termsFixture) run(t *testing.T) (*ladderTestStore, *termRecorder, *LadderRunResult) {
	t.Helper()
	ri := pkgladder.LayerConvertibleRI
	rates := func(r *float64) rateMap {
		if r == nil {
			return rateMap{}
		}
		return rateMap{ri: *r}
	}
	global := ladderTerms{term: pkgladder.Term3Year, payment: pkgladder.PaymentAllUpfront}
	if f.globalYears == 1 {
		global = termsPlan
	}
	rec := &termRecorder{
		fakeLadderCapability: &fakeLadderCapability{
			t: t, baseline: testBaselineLowWater(12.5),
			supportedLayers: []pkgladder.LayerSpec{{Type: ri, Roles: []pkgladder.LayerRole{pkgladder.RoleFlex}}},
			layerStates: map[pkgladder.LayerType]pkgladder.LayerState{
				ri: {Layer: ri, ExistingUSDPerHour: f64(f.existing), ExpiringUSDPerHour: f64(0)},
			},
		},
		byPair: map[ladderTerms]rateMap{termsPlan: rates(f.planRI)},
		errs:   map[ladderTerms]error{global: f.globalErr},
	}
	if global != termsPlan {
		rec.byPair[global] = rates(f.globalRI)
	}
	var capability pkgladder.LadderCapability = rec
	if f.observed != nil {
		capability = &termObservedRecorder{termRecorder: rec, observed: f.observed}
	}
	store := &ladderTestStore{
		globalCfg:     &config.GlobalConfig{LadderingEnabled: true, DefaultTerm: f.globalYears, DefaultPayment: f.globalPayment},
		ladderConfigs: []config.LadderConfigDB{validTestDBConfig("cfg-terms")},
		cloudAcct:     validTestCloudAccount(termsTestAccount),
		inFlightRows:  f.inFlight,
	}
	app := &Application{
		Config: store,
		LadderAccountResolver: func(context.Context) (string, string, error) {
			return termsTestAccount, "us-east-1", nil
		},
		LadderCapabilityFactory: func(context.Context, string, string) (pkgladder.LadderCapability, error) {
			return capability, nil
		},
	}
	result, err := app.handleLadderRun(testutil.TestContext(t))
	require.NoError(t, err)
	return store, rec, result
}

type termObservedRecorder struct {
	*termRecorder
	observed rateMap
}

func (o *termObservedRecorder) GetObservedCommitmentRates(context.Context, pkgladder.Scope) (map[pkgladder.LayerType]float64, error) {
	return o.observed, nil
}

func rp(v float64) *float64 { return &v }

// requireTranche asserts the single persisted tranche, its stored pair and the
// plan JSON action's pair.
func requireTranche(t *testing.T, store *ladderTestStore, amount float64, rate string) {
	t.Helper()
	require.Len(t, store.savedTranches, 1)
	tr := store.savedTranches[0]
	assert.Equal(t, amount, tr.AmountUSDHr)
	require.NotNil(t, tr.CommitmentRate)
	assert.Equal(t, rate, *tr.CommitmentRate)
	assert.Equal(t, config.LadderTerm, tr.Term)
	assert.Equal(t, config.LadderPayment, tr.PaymentOption)

	var plan ladderPlanJSONDTO
	require.NoError(t, json.Unmarshal(store.savedRun.Plan, &plan))
	var purchases int
	for _, a := range plan.Actions {
		if a.Action == string(pkgladder.ActionPurchase) {
			purchases++
			assert.Equal(t, string(config.LadderTerm), a.Term)
			assert.Equal(t, string(config.LadderPayment), a.PaymentOption)
		}
	}
	assert.Equal(t, 1, purchases)
}

func requireHold(t *testing.T, store *ladderTestStore) {
	t.Helper()
	require.NotNil(t, store.savedRun)
	assert.Empty(t, store.savedTranches)
	assert.Nil(t, store.savedRun.ExistingUSDHr)
	assert.Contains(t, string(store.savedRun.Plan), ladderHoldMissingDiscountRate)
}

func TestHandleLadderRun_PlansAtConstantPair(t *testing.T) {
	store, rec, _ := termsFixture{globalYears: 3, globalPayment: "all-upfront", planRI: rp(0.75), globalRI: rp(0.5)}.run(t)

	assert.Equal(t, []ladderTerms{termsPlan, termsGlobal}, rec.calls)
	requireTranche(t, store, 7.5, "3/4")
	assert.Equal(t, 7.5, store.savedRun.TotalHourlyCommit)
	require.NotNil(t, store.savedRun.GapUSDHr)
	assert.Equal(t, 10.0, *store.savedRun.GapUSDHr)
}

func TestHandleLadderRun_ExistingCoverageConvertsAtLowerRate(t *testing.T) {
	tests := []struct {
		name           string
		planRI, global float64
		amount         float64
		rate           string
	}{
		{"global lower", 0.75, 0.5, 3.0, "3/4"},
		{"plan lower", 0.5, 0.75, 2.0, "1/2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _, _ := termsFixture{globalYears: 3, globalPayment: "all-upfront", planRI: rp(tt.planRI), globalRI: rp(tt.global), existing: 3}.run(t)
			require.NotNil(t, store.savedRun.ExistingUSDHr)
			assert.Equal(t, 6.0, *store.savedRun.ExistingUSDHr)
			require.NotNil(t, store.savedRun.GapUSDHr)
			assert.Equal(t, 4.0, *store.savedRun.GapUSDHr)
			requireTranche(t, store, tt.amount, tt.rate)
		})
	}
}

func TestHandleLadderRun_HoldsWhenEitherRateMissingForHeldLayer(t *testing.T) {
	tests := []struct {
		name           string
		planRI, global *float64
	}{
		{"global absent", rp(0.75), nil},
		{"plan absent", nil, rp(0.5)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _, result := termsFixture{globalYears: 3, globalPayment: "all-upfront", planRI: tt.planRI, globalRI: tt.global, existing: 3}.run(t)
			assert.Equal(t, 1, result.Planned)
			requireHold(t, store)
			plan := string(store.savedRun.Plan)
			assert.Contains(t, plan, "1yr no-upfront")
			assert.Contains(t, plan, "3yr all-upfront")
		})
	}
}

func TestHandleLadderRun_ObservedRateBeatsLowerOfTwo(t *testing.T) {
	store, _, _ := termsFixture{
		globalYears: 3, globalPayment: "all-upfront", planRI: rp(0.75), globalRI: rp(0.5),
		existing: 2.5, observed: rateMap{pkgladder.LayerConvertibleRI: 0.625},
	}.run(t)

	require.NotNil(t, store.savedRun.ExistingUSDHr)
	assert.Equal(t, 4.0, *store.savedRun.ExistingUSDHr)
	require.NotNil(t, store.savedRun.GapUSDHr)
	assert.Equal(t, 6.0, *store.savedRun.GapUSDHr)
	requireTranche(t, store, 4.5, "3/4")
}

func TestHandleLadderRun_MissingOrInvalidReferenceRateWithoutHoldingsStillPlans(t *testing.T) {
	for name, global := range map[string]*float64{"absent": nil, "invalid": rp(1.3)} {
		t.Run(name, func(t *testing.T) {
			store, _, _ := termsFixture{globalYears: 3, globalPayment: "all-upfront", planRI: rp(0.75), globalRI: global}.run(t)
			requireTranche(t, store, 7.5, "3/4")
		})
	}
}

func TestHandleLadderRun_SamePairFetchesOnce(t *testing.T) {
	store, rec, _ := termsFixture{globalYears: 1, globalPayment: "no-upfront", planRI: rp(0.5), existing: 3}.run(t)

	assert.Equal(t, []ladderTerms{termsPlan}, rec.calls)
	require.NotNil(t, store.savedRun.ExistingUSDHr)
	assert.Equal(t, 6.0, *store.savedRun.ExistingUSDHr)
	requireTranche(t, store, 2.0, "1/2")
}

func TestHandleLadderRun_ReferenceFetchErrorPersistsNothing(t *testing.T) {
	store, _, result := termsFixture{
		globalYears: 3, globalPayment: "all-upfront", planRI: rp(0.75), globalRI: rp(0.5),
		globalErr: errors.New("ce unavailable"),
	}.run(t)

	assert.Equal(t, 1, result.Errored)
	assert.Nil(t, store.savedRun)
}

func TestHandleLadderRun_InvalidReferenceRateOnHeldLayerFailsRun(t *testing.T) {
	store, _, result := termsFixture{globalYears: 3, globalPayment: "all-upfront", planRI: rp(0.75), globalRI: rp(1.3), existing: 3}.run(t)

	assert.Equal(t, 1, result.Errored)
	assert.Nil(t, store.savedRun)
}

func TestHandleLadderRun_OldPairRowsKeepNetting(t *testing.T) {
	old := config.LadderInFlightRow{Layer: pkgladder.LayerConvertibleRI, CommitmentUSDPerHour: 4.0, Rate: 0.5}
	store, _, _ := termsFixture{
		globalYears: 3, globalPayment: "all-upfront", planRI: rp(0.75), globalRI: rp(0.5),
		inFlight: []config.LadderInFlightRow{old},
	}.run(t)

	require.NotNil(t, store.savedRun.GapUSDHr)
	assert.Equal(t, 2.0, *store.savedRun.GapUSDHr)
	requireTranche(t, store, 1.5, "3/4")
}
