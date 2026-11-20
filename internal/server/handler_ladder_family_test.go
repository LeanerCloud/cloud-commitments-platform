package server

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/testutil"
)

// Compile-time anchor: a pin that drops family support fails the build.
var (
	_ = pkgladder.Tranche{}.Family
	_ = pkgladder.InFlightTranche{}.Family
)

var familyTestNow = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

// familyLadderCapability is the AWS-shaped capability: EC2 Instance SP is the
// only base layer and the baseline carries a per-family stable split.
func familyLadderCapability(t *testing.T, byFamily map[string]float64) *fakeLadderCapability {
	t.Helper()
	lowWater, stable := 0.0, 0.0
	for _, v := range byFamily {
		lowWater += v
		stable += v
	}
	baseline := testBaselineLowWater(lowWater)
	baseline.StableUSDPerHour = nonZeroFloat64(stable)
	baseline.StableUSDPerHourByFamily = byFamily
	zero := 0.0
	return &fakeLadderCapability{
		t:        t,
		baseline: baseline,
		supportedLayers: []pkgladder.LayerSpec{
			{Type: pkgladder.LayerEC2InstanceSP, Roles: []pkgladder.LayerRole{pkgladder.RoleBase}},
			{Type: pkgladder.LayerComputeSP, Roles: []pkgladder.LayerRole{pkgladder.RoleFlex}},
		},
		rates: map[pkgladder.LayerType]float64{pkgladder.LayerEC2InstanceSP: 1, pkgladder.LayerComputeSP: 1},
		layerStates: map[pkgladder.LayerType]pkgladder.LayerState{
			pkgladder.LayerEC2InstanceSP: {Layer: pkgladder.LayerEC2InstanceSP, ExistingUSDPerHour: &zero, ExpiringUSDPerHour: &zero},
			pkgladder.LayerComputeSP:     {Layer: pkgladder.LayerComputeSP, ExistingUSDPerHour: &zero, ExpiringUSDPerHour: &zero},
		},
	}
}

func runFamilyLadder(t *testing.T, byFamily map[string]float64, inFlight []config.LadderInFlightRow) *ladderTestStore {
	t.Helper()
	dbCfg := validTestDBConfig("cfg-family")
	capUSD := 1000.0
	dbCfg.MaxHourlyCommitPerRun = &capUSD
	store := &ladderTestStore{inFlightRows: inFlight}
	app := &Application{Config: store}
	cap := familyLadderCapability(t, byFamily)
	require.NoError(t, app.executeLadderRun(testutil.TestContext(t), &dbCfg, cap, "123456789012", ladderTestGlobal, familyTestNow))
	return store
}

func trancheAmountsByFamily(rows []config.LadderTrancheDB) map[string]float64 {
	out := map[string]float64{}
	for _, r := range rows {
		out[r.Family] += r.AmountUSDHr
	}
	return out
}

func TestBuildTrancheDBRows_CopiesFamily(t *testing.T) {
	cfgID := "cfg-1"
	fam := flooringTestTranche("tr-fam", pkgladder.LayerEC2InstanceSP, "1", familyTestNow)
	fam.Family = "m5"
	plain := flooringTestTranche("tr-plain", pkgladder.LayerComputeSP, "1", familyTestNow)

	rows, err := buildTrancheDBRows([]pkgladder.Tranche{fam, plain}, "run-1", &cfgID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "m5", rows[0].Family)
	assert.Empty(t, rows[1].Family, "a non-EC2-SP tranche has no family")
}

func TestImmediateTrancheAmounts_KeyedByFamily(t *testing.T) {
	m5 := flooringTestTranche("tr-m5", pkgladder.LayerEC2InstanceSP, "3", familyTestNow)
	m5.Family = "m5"
	c5 := flooringTestTranche("tr-c5", pkgladder.LayerEC2InstanceSP, "1", familyTestNow)
	c5.Family = "c5"

	sums, err := immediateTrancheAmounts([]pkgladder.Tranche{m5, c5}, familyTestNow)
	require.NoError(t, err)
	require.Len(t, sums, 2)
	assert.Equal(t, "3", sums[ladderAmountKey{layer: pkgladder.LayerEC2InstanceSP, family: "m5"}].FloatString(0))
	assert.Equal(t, "1", sums[ladderAmountKey{layer: pkgladder.LayerEC2InstanceSP, family: "c5"}].FloatString(0))
}

// Two family allocations on one layer each show their own immediate amount and
// family in the plan JSON; the plan still validates.
func TestExecuteLadderRun_TwoFamilies_PlanShowsOwnAmounts(t *testing.T) {
	store := runFamilyLadder(t, map[string]float64{"m5": 6, "c5": 2}, nil)

	rowAmounts := trancheAmountsByFamily(store.savedTranches)
	require.Len(t, rowAmounts, 2)
	assert.InDelta(t, 4.8, rowAmounts["m5"], 1e-6)
	assert.InDelta(t, 1.6, rowAmounts["c5"], 1e-6)

	var dto ladderPlanJSONDTO
	require.NoError(t, json.Unmarshal(store.savedRun.Plan, &dto))
	planAmounts := map[string]float64{}
	for _, a := range dto.Actions {
		if a.Action == string(pkgladder.ActionPurchase) {
			require.NotNil(t, a.AmountUSDHr)
			planAmounts[a.Family] = *a.AmountUSDHr
		}
	}
	require.Len(t, planAmounts, 2, "one purchase action per family")
	assert.InDelta(t, rowAmounts["m5"], planAmounts["m5"], 1e-6, "not the summed layer total")
	assert.InDelta(t, rowAmounts["c5"], planAmounts["c5"], 1e-6, "not the summed layer total")
}

// plan() hands each in-flight row's family to Allocate: a covered m5 gets no
// top-up and c5 gets only its own headroom, instead of the 75/25 share split.
func TestExecuteLadderRun_InFlightFamilyNetsPerFamily(t *testing.T) {
	byFamily := map[string]float64{"m5": 6, "c5": 2}
	withFamily := runFamilyLadder(t, byFamily, []config.LadderInFlightRow{
		{Layer: pkgladder.LayerEC2InstanceSP, Family: "m5", CommitmentUSDPerHour: 6, Rate: 1},
	})
	got := trancheAmountsByFamily(withFamily.savedTranches)
	assert.Zero(t, got["m5"], "m5 is covered in flight")
	assert.InDelta(t, 0.4, got["c5"], 1e-6, "c5 gets its own gap, not a share of the total")

	noFamily := runFamilyLadder(t, byFamily, []config.LadderInFlightRow{
		{Layer: pkgladder.LayerEC2InstanceSP, CommitmentUSDPerHour: 6, Rate: 1},
	})
	legacy := trancheAmountsByFamily(noFamily.savedTranches)
	families := make([]string, 0, len(legacy))
	for f := range legacy {
		families = append(families, f)
	}
	sort.Strings(families)
	assert.NotEqual(t, got, legacy, "a family-less in-flight row nets by share, so the split differs (families %v)", families)
}
