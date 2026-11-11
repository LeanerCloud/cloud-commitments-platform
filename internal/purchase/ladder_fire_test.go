package purchase

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFireStore is a LadderFireStore whose claim runs the create callback on
// the first claimTake due rows (all when negative).
type fakeFireStore struct {
	globals     []*config.GlobalConfig // popped per GetGlobalConfig call, the last one sticks.
	globalErr   error
	configs     []config.LadderConfigDB
	acctExt     string
	ceiling     *int64
	ceilingErr  error
	rows        map[string][]config.LadderDueRow
	claimTake   int
	claimErrFor map[string]error

	listCalls  int
	claimCalls int
	saveCalls  int
	listLayers []ladder.LayerType
	filter     config.LadderClaimFilter
	claimLimit int
	claimedFor []string
	saved      []*config.PurchaseExecution
}

func (f *fakeFireStore) GetGlobalConfig(context.Context) (*config.GlobalConfig, error) {
	if f.globalErr != nil {
		return nil, f.globalErr
	}
	g := f.globals[0]
	if len(f.globals) > 1 {
		f.globals = f.globals[1:]
	}
	return g, nil
}

func (f *fakeFireStore) GetLadderConfigs(context.Context) ([]config.LadderConfigDB, error) {
	return f.configs, nil
}

func (f *fakeFireStore) GetCloudAccount(context.Context, string) (*config.CloudAccount, error) {
	return &config.CloudAccount{ExternalID: f.acctExt}, nil
}

func (f *fakeFireStore) GetLadderSweepCeiling(context.Context, string) (*int64, error) {
	return f.ceiling, f.ceilingErr
}

func (f *fakeFireStore) ListDueLadderTranches(_ context.Context, configID string, _ time.Time, _ int, layers []ladder.LayerType) ([]config.LadderDueRow, error) {
	f.listCalls++
	f.listLayers = layers
	return f.rows[configID], nil
}

func (f *fakeFireStore) ClaimDueLadderTranches(ctx context.Context, configID string, _ time.Time, limit int, filter config.LadderClaimFilter,
	create func(context.Context, pgx.Tx, config.LadderTrancheDB) (string, error),
) ([]config.LadderTrancheDB, error) {
	f.claimCalls++
	f.filter, f.claimLimit = filter, limit
	if err := f.claimErrFor[configID]; err != nil {
		return nil, err
	}
	var out []config.LadderTrancheDB
	for _, r := range f.rows[configID] {
		if !slices.Contains(filter.TrancheIDs, r.Tranche.ID) {
			continue
		}
		if f.claimTake >= 0 && len(out) >= f.claimTake {
			break
		}
		id, err := create(ctx, nil, r.Tranche)
		if err != nil {
			return nil, err
		}
		tr := r.Tranche
		tr.ExecutionID = &id
		out = append(out, tr)
	}
	f.claimedFor = append(f.claimedFor, configID)
	return out, nil
}

func (f *fakeFireStore) SavePurchaseExecutionTx(_ context.Context, _ pgx.Tx, e *config.PurchaseExecution) error {
	f.saveCalls++
	f.saved = append(f.saved, e)
	return nil
}

func fireCfg(id string) config.LadderConfigDB {
	capUSD := 10.0
	return config.LadderConfigDB{ID: id, Enabled: true, Provider: "aws", Mode: "auto_approve", MaxHourlyCommitPerRun: &capUSD, CloudAccountID: "acct-a"}
}

func micros(v int64) *int64 { return &v }

func newFireStore(cfgs ...config.LadderConfigDB) *fakeFireStore {
	f := &fakeFireStore{
		globals: []*config.GlobalConfig{fireGlobal(nil)}, configs: cfgs, acctExt: "own-acct",
		ceiling: micros(6_000_000), claimTake: -1, rows: map[string][]config.LadderDueRow{},
	}
	for _, c := range cfgs {
		f.rows[c.ID] = []config.LadderDueRow{dueRow("T1-"+c.ID, 2.5, true), dueRow("T2-"+c.ID, 3.0, true)}
	}
	return f
}

func fireDepsFor(f *fakeFireStore) LadderFireDeps {
	d := fireDeps()
	d.Store, d.Registry, d.Region = f, NewLadderInstrumentRegistry(), "us-east-1"
	d.Now = func() time.Time { return fireNow }
	return d
}

func runFire(f *fakeFireStore) (LadderFireResult, error) {
	return RunLadderFire(context.Background(), fireDepsFor(f))
}

func TestSweep_ClaimsWithTheResolvedFilter(t *testing.T) {
	f := newFireStore(fireCfg("c1"))
	f.rows["c1"] = append(f.rows["c1"], dueRow("X", 1, false))
	res, err := runFire(f)
	require.NoError(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 3, Unresolved: 1, Claimed: 2}, res)
	assert.Equal(t, 1, f.claimCalls)
	assert.Equal(t, NewLadderInstrumentRegistry().Layers(), f.filter.Layers)
	assert.Equal(t, NewLadderInstrumentRegistry().Layers(), f.listLayers)
	assert.Equal(t, []string{"T1-c1", "T2-c1"}, f.filter.TrancheIDs)
	assert.Equal(t, int64(6_000_000), f.filter.CeilingMicros)
	assert.Equal(t, ladderFireClaimLimit, f.claimLimit)
	require.Len(t, f.saved, 2)
	assert.Equal(t, fireNow.Add(48*time.Hour), *f.saved[0].ScheduledExecutionAt)
}

func TestSweep_DelayIsTheGatedAccessorValue(t *testing.T) {
	f := newFireStore(fireCfg("c1"))
	f.globals = []*config.GlobalConfig{fireGlobal(func(g *config.GlobalConfig) { g.PurchaseDelayHours = 200 })}
	_, err := runFire(f)
	require.NoError(t, err)
	require.NotEmpty(t, f.saved)
	assert.Equal(t, fireNow.Add(168*time.Hour), *f.saved[0].ScheduledExecutionAt)
}

func TestSweep_NothingResolvedSkipsTheClaim(t *testing.T) {
	f := newFireStore(fireCfg("c1"))
	f.rows["c1"] = []config.LadderDueRow{dueRow("a", 1, false), dueRow("b", 1, false)}
	res, err := runFire(f)
	require.NoError(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 2, Unresolved: 2}, res)
	assert.Zero(t, f.claimCalls)
}

func TestSweep_PrefixTrimmedLogsWarn(t *testing.T) {
	buf := captureLadderLog(t)
	f := newFireStore(fireCfg("c1"))
	f.claimTake = 1
	res, err := runFire(f)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Claimed)
	assert.Equal(t, 1, res.Trimmed)
	out := buf.String()
	assert.Contains(t, out, "WARN")
	assert.NotContains(t, out, "ERROR")
	assert.Contains(t, out, "1 held back")
}

func TestSweep_HeadTrancheLargerThanCeilingLogsError(t *testing.T) {
	buf := captureLadderLog(t)
	f := newFireStore(fireCfg("c1"))
	f.ceiling = micros(5_000_000)
	f.rows["c1"] = []config.LadderDueRow{dueRow("big", 6, true)}
	f.claimTake = 0
	res, err := runFire(f)
	require.NoError(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 1, Trimmed: 1}, res)
	out := buf.String()
	assert.Contains(t, out, "ERROR")
	assert.Contains(t, out, "5000000")
	assert.Contains(t, out, "6.000000")
}

func TestSweep_ConfigErrorsAreIsolated(t *testing.T) {
	f := newFireStore(fireCfg("a"), fireCfg("b"))
	f.claimErrFor = map[string]error{"a": errors.New("claim failed")}
	res, err := runFire(f)
	require.Error(t, err)
	assert.Equal(t, 2, res.Configs)
	assert.Equal(t, 1, res.Errored)
	assert.Equal(t, 2, res.Claimed)
	assert.Equal(t, []string{"b"}, f.claimedFor)
}

func TestRunLadderFire_RequiresItsDeps(t *testing.T) {
	_, err := RunLadderFire(context.Background(), LadderFireDeps{})
	require.Error(t, err)
}
