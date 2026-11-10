package server

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	awsladder "github.com/LeanerCloud/cloud-commitments-go/providers/aws/ladder"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/testutil"
)

// bufferFractionSpy records the order of WithBufferFraction relative to
// GetUsageBaseline.
type bufferFractionSpy struct {
	*fakeLadderCapability
	calls       []string
	fractions   []float64
	setErr      error
	baselineHit bool
}

func (s *bufferFractionSpy) WithBufferFraction(f float64) (*awsladder.AWSLadder, error) {
	s.calls = append(s.calls, "WithBufferFraction")
	s.fractions = append(s.fractions, f)
	return nil, s.setErr
}

func (s *bufferFractionSpy) GetUsageBaseline(ctx context.Context, scope pkgladder.Scope, days int, pct float64) (pkgladder.UsageBaseline, error) {
	s.calls = append(s.calls, "GetUsageBaseline")
	s.baselineHit = true
	return s.fakeLadderCapability.GetUsageBaseline(ctx, scope, days, pct)
}

func runBufferFractionSpy(t *testing.T, fraction float64, setErr error) (*bufferFractionSpy, *ladderTestStore, error) {
	t.Helper()
	ctx := testutil.TestContext(t)
	dbCfg := validTestDBConfig("cfg-buffer")
	dbCfg.BufferFraction = fraction
	// A positive fraction needs a buffer-role layer; the AWS layer set that has
	// one also sizes Compute SP, which requires a per-run cap.
	dbCfg.MaxHourlyCommitPerRun = f64(100)
	store := &ladderTestStore{}
	app := &Application{Config: store}
	states := map[pkgladder.LayerType]pkgladder.LayerState{}
	rates := map[pkgladder.LayerType]float64{}
	for _, spec := range awsPlanningLayers {
		states[spec.Type] = emptyLayerState(spec.Type)
		rates[spec.Type] = 0.5
	}
	spy := &bufferFractionSpy{
		fakeLadderCapability: &fakeLadderCapability{t: t, baseline: testBaseline(), layerStates: states, rates: rates, supportedLayers: awsPlanningLayers},
		setErr:               setErr,
	}
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	err := app.executeLadderRun(ctx, &dbCfg, spy, "123456789012", ladderTestGlobal, now)
	return spy, store, err
}

func TestExecuteLadderRun_SetsBufferFractionOnAWSLadder(t *testing.T) {
	t.Parallel()

	t.Run("config fraction is passed before GetUsageBaseline", func(t *testing.T) {
		t.Parallel()
		spy, _, err := runBufferFractionSpy(t, 0.1, nil)
		require.NoError(t, err)
		fractions := spy.fractions
		assert.Equal(t, []float64{0.1}, fractions)
		calls := spy.calls
		assert.Equal(t, []string{"WithBufferFraction", "GetUsageBaseline"}, calls)
	})

	t.Run("explicit zero is still passed", func(t *testing.T) {
		t.Parallel()
		spy, _, err := runBufferFractionSpy(t, 0, nil)
		require.NoError(t, err)
		fractions := spy.fractions
		assert.Equal(t, []float64{0}, fractions)
	})

	t.Run("setter error fails the run before any baseline or persist", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("fraction out of range")
		spy, store, err := runBufferFractionSpy(t, 0.1, boom)
		require.Error(t, err)
		assert.ErrorIs(t, err, boom)
		assert.False(t, spy.baselineHit, "GetUsageBaseline must not run after a failed setter")
		assert.Nil(t, store.savedRun, "no run may be persisted")
		assert.Empty(t, store.savedTranches)
	})
}

func TestSetLadderBufferFraction_NonSetterCapabilityIsUnchanged(t *testing.T) {
	t.Parallel()
	plain := &fakeLadderCapability{t: t}
	assert.NoError(t, setLadderBufferFraction(plain, 0.5))
	assert.NoError(t, setLadderBufferFraction(plain, math.NaN()), "a fake without the setter is never validated")
}

// A real AWSLadder is built offline (the SDK creates clients lazily); the
// ladder's own range check is what the run relies on to fail loudly.
func TestSetLadderBufferFraction_RealAWSLadder(t *testing.T) {
	t.Parallel()
	capRaw, err := awsladder.NewFromAWSConfig(context.Background(), "us-east-1", "123456789012")
	require.NoError(t, err)

	require.NoError(t, setLadderBufferFraction(capRaw, 0.1))
	require.NoError(t, setLadderBufferFraction(capRaw, 0))
	assert.Error(t, setLadderBufferFraction(capRaw, 1))
	assert.Error(t, setLadderBufferFraction(capRaw, -0.1))
	assert.Error(t, setLadderBufferFraction(capRaw, math.NaN()))
}

// The capability the planner actually uses (factory plus disabled write-side
// wiring) must still expose the setter, or the wiring would silently no-op.
func TestPlanningCapability_ExposesBufferFractionSetter(t *testing.T) {
	t.Parallel()
	app := &Application{LadderCapabilityFactory: func(ctx context.Context, region, accountID string) (pkgladder.LadderCapability, error) {
		return awsladder.NewFromAWSConfig(ctx, region, accountID)
	}}
	c, err := app.planningCapability(context.Background(), "us-east-1", "123456789012")
	require.NoError(t, err)
	_, ok := c.(bufferFractionSetter)
	require.True(t, ok, "planning capability must implement bufferFractionSetter")
	assert.Error(t, setLadderBufferFraction(c, 1))
	assert.NoError(t, setLadderBufferFraction(c, 0.1))
}
