package server

import (
	"context"
	"testing"
	"time"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/testutil"
)

func TestLadderComputeCapRefusal(t *testing.T) {
	t.Parallel()
	computeSP := []pkgladder.LayerSpec{{Type: pkgladder.LayerComputeSP, Roles: []pkgladder.LayerRole{pkgladder.RoleFlex}}}
	noCompute := []pkgladder.LayerSpec{{Type: pkgladder.LayerEC2InstanceSP, Roles: []pkgladder.LayerRole{pkgladder.RoleBase}}}
	for _, tc := range []struct {
		name    string
		layers  []pkgladder.LayerSpec
		cap     *float64
		wantErr bool
	}{
		{"compute sp without a cap", computeSP, nil, true},
		{"compute sp among others without a cap", awsLadderLayers, nil, true},
		{"compute sp with a cap", computeSP, f64(100), false},
		{"no compute sp without a cap", noCompute, nil, false},
		{"empty layers without a cap", nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbCfg := validTestDBConfig("cfg-cap")
			dbCfg.MaxHourlyCommitPerRun = tc.cap
			err := ladderComputeCapRefusal(&dbCfg, tc.layers)
			if tc.wantErr {
				require.ErrorContains(t, err, "max_hourly_commit_per_run")
				return
			}
			require.NoError(t, err)
		})
	}
}

// providerReadCounter counts the read-side provider calls of one run.
type providerReadCounter struct {
	*fakeLadderCapability
	baselines, layerStates, rates int
}

func (c *providerReadCounter) GetUsageBaseline(ctx context.Context, s pkgladder.Scope, days int, pct float64) (pkgladder.UsageBaseline, error) {
	c.baselines++
	return c.fakeLadderCapability.GetUsageBaseline(ctx, s, days, pct)
}

func (c *providerReadCounter) GetLayerStates(ctx context.Context, s pkgladder.Scope) (map[pkgladder.LayerType]pkgladder.LayerState, error) {
	c.layerStates++
	return c.fakeLadderCapability.GetLayerStates(ctx, s)
}

func (c *providerReadCounter) GetCommitmentRates(ctx context.Context, s pkgladder.Scope, term pkgladder.Term, p pkgladder.PaymentOption) (map[pkgladder.LayerType]float64, error) {
	c.rates++
	return c.fakeLadderCapability.GetCommitmentRates(ctx, s, term, p)
}

// persistCountingStore counts PlanAndSaveLadderRun calls.
type persistCountingStore struct {
	*ladderTestStore
	persists int
}

func (s *persistCountingStore) PlanAndSaveLadderRun(ctx context.Context, id string, now time.Time, build config.LadderPlanBuilder) (*config.LadderRunDB, error) {
	s.persists++
	return s.ladderTestStore.PlanAndSaveLadderRun(ctx, id, now, build)
}

func runComputeCapConfig(t *testing.T, cap *float64) (*LadderRunResult, *persistCountingStore, *providerReadCounter) {
	t.Helper()
	states := map[pkgladder.LayerType]pkgladder.LayerState{}
	rates := map[pkgladder.LayerType]float64{}
	for _, spec := range awsPlanningLayers {
		states[spec.Type] = emptyLayerState(spec.Type)
		rates[spec.Type] = 0.5
	}
	reads := &providerReadCounter{fakeLadderCapability: &fakeLadderCapability{
		t: t, baseline: testBaselineLowWater(12.5), layerStates: states, rates: rates, supportedLayers: awsPlanningLayers,
	}}
	store := &persistCountingStore{ladderTestStore: &ladderTestStore{cloudAcct: validTestCloudAccount(computeCapAccount)}}
	app := &Application{
		Config: store,
		LadderCapabilityFactory: func(_ context.Context, _, _ string) (pkgladder.LadderCapability, error) {
			return reads, nil
		},
	}
	dbCfg := validTestDBConfig("cfg-compute-cap")
	dbCfg.MaxHourlyCommitPerRun = cap
	result := app.runLadderConfigs(testutil.TestContext(t), []config.LadderConfigDB{dbCfg}, computeCapAccount, "us-east-1",
		ladderTestGlobal, ratesTestNow)
	return result, store, reads
}

// computeCapAccount is the lambda and cloud account of the cap tests.
const computeCapAccount = "210987654321"

// awsPlanningLayers mirrors the AWS capability's SupportedLayers, one base layer
// only, so a capped run can allocate.
var awsPlanningLayers = []pkgladder.LayerSpec{
	{Type: pkgladder.LayerEC2InstanceSP, Roles: []pkgladder.LayerRole{pkgladder.RoleBase}},
	{Type: pkgladder.LayerComputeSP, Roles: []pkgladder.LayerRole{pkgladder.RoleFlex}},
	{Type: pkgladder.LayerConvertibleRI, Roles: []pkgladder.LayerRole{pkgladder.RoleBuffer}},
}

func TestRunLadderConfigs_ComputeSPWithoutCapIsErroredBeforeAnyRead(t *testing.T) {
	t.Parallel()
	result, store, reads := runComputeCapConfig(t, nil)

	assert.Equal(t, LadderRunResult{Errored: 1}, *result)
	assert.Zero(t, store.persists, "PlanAndSaveLadderRun must not be called")
	assert.Nil(t, store.savedRun)
	assert.Empty(t, store.savedRuns)
	assert.Zero(t, reads.baselines, "no baseline read")
	assert.Zero(t, reads.layerStates, "no layer-state read")
	assert.Zero(t, reads.rates, "no rate read")
}

func TestRunLadderConfigs_ComputeSPWithCapStillPlans(t *testing.T) {
	t.Parallel()
	result, store, reads := runComputeCapConfig(t, f64(100))

	assert.Equal(t, LadderRunResult{Planned: 1}, *result)
	assert.Equal(t, 1, store.persists)
	require.NotNil(t, store.savedRun)
	assert.Equal(t, 1, reads.baselines)
	assert.Equal(t, 1, reads.layerStates)
	assert.Equal(t, 1, reads.rates)
}
