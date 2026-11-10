package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pagingFake serves tranches in order with real keyset semantics (strictly
// after the cursor) and decides each by its execution status.
type pagingFake struct {
	tranches []config.LadderTrancheDB
	status   map[string]string // tranche ID -> chain tail status
	lockErr  map[string]error
	listErr  error
	moved    []string
	cursors  []*config.LadderFiredCursor

	// D5 hooks: the global config served in the tx, the ids passed to
	// DisableLadderConfigTx, and what that call returns.
	global       *config.GlobalConfig
	globalErr    error
	globalReads  int
	disabled     []string
	disableErr   map[string]error // config ID -> error
	alreadyOff   map[string]bool  // config ID -> already disabled
	onGlobalRead func()
	noProof      map[string]bool // tranche ID -> chain tail lacks the no_submission proof
	notMoved     map[string]bool // tranche ID -> the CAS transition loses the race
	txResults    []error         // what each WithTx callback returned
}

func (f *pagingFake) ListFiredLadderTranches(_ context.Context, after *config.LadderFiredCursor, limit int) ([]config.LadderTrancheDB, error) {
	f.cursors = append(f.cursors, after)
	if f.listErr != nil {
		return nil, f.listErr
	}
	start := 0
	if after != nil {
		for start < len(f.tranches) && f.tranches[start].ID != after.ID {
			start++
		}
		start++
	}
	end := min(start+limit, len(f.tranches))
	return f.tranches[min(start, end):end], nil
}

// WithTx undoes the moves of a callback that returned an error, like a rollback.
func (f *pagingFake) WithTx(_ context.Context, fn func(pgx.Tx) error) error {
	before := len(f.moved)
	err := fn(nil)
	f.txResults = append(f.txResults, err)
	if err != nil {
		f.moved = f.moved[:before]
	}
	return err
}
func (f *pagingFake) LockLadderTrancheChainTx(_ context.Context, _ pgx.Tx, id string) ([]config.LadderChainNode, error) {
	if err := f.lockErr[id]; err != nil {
		return nil, err
	}
	return []config.LadderChainNode{{ExecutionID: "e-" + id, Status: f.status[id], NoSubmission: !f.noProof[id], CreatedAt: time.Unix(0, 0)}}, nil
}
func (f *pagingFake) TransitionLadderTrancheTx(_ context.Context, _ pgx.Tx, id string, _, _ ladder.TrancheStatus, _ string) (bool, error) {
	f.moved = append(f.moved, id)
	return !f.notMoved[id], nil
}

func (f *pagingFake) GetGlobalConfigTx(context.Context, pgx.Tx) (*config.GlobalConfig, error) {
	f.globalReads++
	g, err := f.global, f.globalErr
	if f.onGlobalRead != nil {
		f.onGlobalRead()
	}
	return g, err
}
func (f *pagingFake) DisableLadderConfigTx(_ context.Context, _ pgx.Tx, configID string) (bool, error) {
	f.disabled = append(f.disabled, configID)
	if err := f.disableErr[configID]; err != nil {
		return false, err
	}
	if f.alreadyOff[configID] {
		return false, nil
	}
	if f.alreadyOff == nil {
		f.alreadyOff = map[string]bool{}
	}
	f.alreadyOff[configID] = true
	return true, nil
}

func pagingTranches(ids ...string) []config.LadderTrancheDB {
	out := make([]config.LadderTrancheDB, 0, len(ids))
	for i, id := range ids {
		out = append(out, config.LadderTrancheDB{ID: id, ScheduledDate: testNow.Add(time.Duration(i) * time.Minute)})
	}
	return out
}

func TestReconcile_ErroredTrancheDoesNotStopTheSweep(t *testing.T) {
	f := &pagingFake{
		tranches: pagingTranches("t1", "t2"),
		status:   map[string]string{"t2": "failed"},
		lockErr:  map[string]error{"t1": errors.New("boom")},
	}

	res, err := ReconcileLadderTranches(context.Background(), f, stubVis{}, testNow)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 tranche(s) errored")
	assert.Equal(t, []string{"t2"}, f.moved)
	assert.Equal(t, LadderReconcileResult{Checked: 2, Failed: 1, Errored: 1}, res)
}

func TestReconcile_ListErrorStopsTheRun(t *testing.T) {
	boom := errors.New("list failed")
	_, err := ReconcileLadderTranches(context.Background(), &pagingFake{listErr: boom}, stubVis{}, testNow)
	require.ErrorIs(t, err, boom)
}

func TestReconcile_LaterPageIsReachedPastUnresolvablePage(t *testing.T) {
	f := &pagingFake{
		tranches: pagingTranches("t1", "t2", "t3"),
		status:   map[string]string{"t1": "running", "t2": "running", "t3": "failed"},
	}

	res, err := reconcileFiredTranches(context.Background(), f, stubVis{}, testNow, 2)

	require.NoError(t, err)
	assert.Equal(t, []string{"t3"}, f.moved)
	// The fake chains have a zero head time, so the two running tails are also stale.
	assert.Equal(t, LadderReconcileResult{Checked: 3, Unresolved: 2, Stale: 2, Failed: 1}, res)
	require.Len(t, f.cursors, 2)
	assert.Nil(t, f.cursors[0])
	require.NotNil(t, f.cursors[1])
	assert.Equal(t, "t2", f.cursors[1].ID)
	assert.True(t, f.cursors[1].ScheduledDate.Equal(f.tranches[1].ScheduledDate))
}

type stubVis struct{}

func (stubVis) CommitmentVisible(context.Context, config.LadderTrancheDB, string) (bool, error) {
	return true, nil
}

func TestReconcile_CanceledContextStopsWithoutCountingErrored(t *testing.T) {
	f := &pagingFake{
		tranches: pagingTranches("t1", "t2"),
		status:   map[string]string{"t1": "failed", "t2": "failed"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := ReconcileLadderTranches(ctx, f, stubVis{}, testNow)

	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, f.moved)
	assert.Equal(t, LadderReconcileResult{}, res)
}
