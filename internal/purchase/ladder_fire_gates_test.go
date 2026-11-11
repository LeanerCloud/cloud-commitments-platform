package purchase

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSweep_EveryGateRefusesWithoutClaiming(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*fakeFireStore, *LadderFireDeps)
	}{
		{"laddering off", func(f *fakeFireStore, _ *LadderFireDeps) {
			f.globals = []*config.GlobalConfig{fireGlobal(func(g *config.GlobalConfig) { g.LadderingEnabled = false })}
		}},
		{"execution off", func(f *fakeFireStore, _ *LadderFireDeps) {
			f.globals = []*config.GlobalConfig{fireGlobal(func(g *config.GlobalConfig) { g.LadderExecutionEnabled = false })}
		}},
		{"4-eyes on", func(f *fakeFireStore, _ *LadderFireDeps) {
			f.globals = []*config.GlobalConfig{fireGlobal(func(g *config.GlobalConfig) { g.RequireDifferentApprover = true })}
		}},
		{"delay 23", func(f *fakeFireStore, _ *LadderFireDeps) {
			f.globals = []*config.GlobalConfig{fireGlobal(func(g *config.GlobalConfig) { g.PurchaseDelayHours = 23 })}
		}},
		{"no notifier", func(_ *fakeFireStore, d *LadderFireDeps) { d.Notifier = nil }},
		{"no recipient", func(f *fakeFireStore, _ *LadderFireDeps) {
			f.globals = []*config.GlobalConfig{fireGlobal(func(g *config.GlobalConfig) { g.NotificationEmail = nil })}
		}},
		{"no dashboard", func(_ *fakeFireStore, d *LadderFireDeps) { d.DashboardURL = "" }},
		{"config disabled", func(f *fakeFireStore, _ *LadderFireDeps) { f.configs[0].Enabled = false }},
		{"azure", func(f *fakeFireStore, _ *LadderFireDeps) { f.configs[0].Provider = "azure" }},
		{"email approval", func(f *fakeFireStore, _ *LadderFireDeps) { f.configs[0].Mode = "email_approval" }},
		{"no cap", func(f *fakeFireStore, _ *LadderFireDeps) { f.configs[0].MaxHourlyCommitPerRun = nil }},
		{"other account", func(f *fakeFireStore, _ *LadderFireDeps) { f.acctExt = "other" }},
		{"ceiling unset", func(f *fakeFireStore, _ *LadderFireDeps) { f.ceiling = nil }},
		{"ceiling zero", func(f *fakeFireStore, _ *LadderFireDeps) { f.ceiling = micros(0) }},
		{"ceiling column absent", func(f *fakeFireStore, _ *LadderFireDeps) { f.ceilingErr = config.ErrLadderCeilingUnavailable }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFireStore(fireCfg("c1"))
			d := fireDepsFor(f)
			c.mut(f, &d)
			res, err := RunLadderFire(context.Background(), d)
			require.NoError(t, err)
			assert.Equal(t, 1, res.Skipped)
			assert.Zero(t, f.claimCalls)
			assert.Zero(t, f.saveCalls)
			assert.Zero(t, f.listCalls)
		})
	}
}

func TestSweep_CeilingReadErrorErrorsTheConfig(t *testing.T) {
	f := newFireStore(fireCfg("c1"))
	f.ceilingErr = errors.New("db down")
	res, err := runFire(f)
	require.Error(t, err)
	assert.Equal(t, 1, res.Errored)
	assert.Zero(t, f.claimCalls)
}

func TestSweep_GlobalReadErrorErrorsTheConfig(t *testing.T) {
	f := newFireStore(fireCfg("c1"))
	f.globalErr = errors.New("db down")
	res, err := runFire(f)
	require.Error(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Errored: 1}, res)
	assert.Zero(t, f.claimCalls)
}

func TestSweep_GlobalSwitchIsReReadPerConfig(t *testing.T) {
	f := newFireStore(fireCfg("a"), fireCfg("b"))
	f.globals = []*config.GlobalConfig{fireGlobal(nil), fireGlobal(func(g *config.GlobalConfig) { g.LadderExecutionEnabled = false })}
	res, err := runFire(f)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, f.claimedFor)
	assert.Equal(t, 1, res.Skipped)
}

func TestSweep_RIFilterReachesTheClaim(t *testing.T) {
	// A registry with an RI layer: the claim gets exactly the registry's layers
	// minus RI unless the class is convertible or unset.
	reg := &LadderInstrumentRegistry{builders: map[ladder.LayerType]LadderInstrumentBuilder{
		ladder.LayerComputeSP: buildComputeSPRecs, ladder.LayerConvertibleRI: buildComputeSPRecs,
	}}
	for class, want := range map[string][]ladder.LayerType{
		"standard":    {ladder.LayerComputeSP},
		"convertible": {ladder.LayerComputeSP, ladder.LayerConvertibleRI},
	} {
		f := newFireStore(fireCfg("c1"))
		f.globals = []*config.GlobalConfig{fireGlobal(func(g *config.GlobalConfig) { g.OfferingClass = class })}
		d := fireDepsFor(f)
		d.Registry = reg
		_, err := RunLadderFire(context.Background(), d)
		require.NoError(t, err)
		assert.Equal(t, want, f.filter.Layers, class)
		assert.Equal(t, want, f.listLayers, class)
	}
}

func TestSweep_CanceledContextStopsBeforeAnyConfig(t *testing.T) {
	f := newFireStore(fireCfg("c1"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := RunLadderFire(ctx, fireDepsFor(f))
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, res.Configs)
	assert.Zero(t, f.claimCalls)
}

func TestSweep_UsedUpBudgetIsAWarnNotAnError(t *testing.T) {
	buf := captureLadderLog(t)
	f := newFireStore(fireCfg("c1"))
	f.claimTake = 0 // the head (2.5) fits the ceiling (6.0), the day's budget is spent.
	_, err := runFire(f)
	require.NoError(t, err)
	out := buf.String()
	assert.NotContains(t, out, "ERROR")
	assert.Contains(t, out, "budget is used")
}
