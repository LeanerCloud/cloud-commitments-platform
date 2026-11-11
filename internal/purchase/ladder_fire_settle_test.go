package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settleFake models the transaction of settleNoticeFailed: writes made inside
// WithTx are applied only when the callback returns nil, so a callback that
// returns nil after a failed write, or writes outside WithTx, fails a test.
type settleFake struct {
	chain       []config.LadderChainNode
	lockErr     error
	cancelFails bool
	moveFails   bool

	inTx      bool
	pending   []string
	committed []string
	outside   []string
	withTx    int
	reason    string
}

type fakeTx struct{ pgx.Tx }

func (f *fakeFireStore) WithTx(ctx context.Context, fn func(pgx.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.settle.withTx++
	f.settle.inTx, f.settle.pending = true, nil
	err := fn(fakeTx{})
	f.settle.inTx = false
	if err == nil {
		f.settle.committed = append(f.settle.committed, f.settle.pending...)
	}
	return err
}

func (f *fakeFireStore) record(op string) {
	if !f.settle.inTx {
		f.settle.outside = append(f.settle.outside, op)
		return
	}
	f.settle.pending = append(f.settle.pending, op)
}

func (f *fakeFireStore) LockLadderTrancheChainTx(_ context.Context, _ pgx.Tx, id string) ([]config.LadderChainNode, error) {
	f.record("lock:" + id)
	return f.settle.chain, f.settle.lockErr
}

func (f *fakeFireStore) CancelScheduledExecutionAtomic(_ context.Context, _ pgx.Tx, id string, _ *string) (bool, string, error) {
	f.record("cancel:" + id)
	return !f.settle.cancelFails, "scheduled", nil
}

func (f *fakeFireStore) TransitionLadderTrancheTx(_ context.Context, _ pgx.Tx, id string, from, to ladder.TrancheStatus, reason string) (bool, error) {
	f.record("transition:" + id + ":" + string(from) + ">" + string(to))
	f.settle.reason = reason
	return !f.settle.moveFails, nil
}

func settleSubject() (*fakeFireStore, *config.LadderTrancheDB, *config.PurchaseExecution) {
	f := newFireStore(fireCfg("c1"))
	f.settle.chain = []config.LadderChainNode{{ExecutionID: "e1", Status: "scheduled"}}
	return f, &config.LadderTrancheDB{ID: "t1"}, &config.PurchaseExecution{ExecutionID: "e1"}
}

func TestSettle_CancelsThenFailsTheTrancheInOneTransaction(t *testing.T) {
	f, tr, exec := settleSubject()
	require.NoError(t, settleNoticeFailed(context.Background(), f, tr, exec))
	assert.Equal(t, []string{"lock:t1", "cancel:e1", "transition:t1:fired>failed"}, f.settle.committed)
	assert.Equal(t, "notice_send_failed", f.settle.reason)
	assert.Empty(t, f.settle.outside, "every write must be inside WithTx")
	assert.Equal(t, 1, f.settle.withTx)
}

func TestSettle_LeavesWhatIsNotThePlainScheduledTailToTheReconciler(t *testing.T) {
	retry := "e2"
	cases := map[string][]config.LadderChainNode{
		"revoked":      {{ExecutionID: "e1", Status: "canceled"}},
		"legacy spell": {{ExecutionID: "e1", Status: "cancelled"}}, //nolint:misspell // legacy stored spelling
		"retry linked": {{ExecutionID: "e1", Status: "scheduled", RetryExecutionID: &retry}},
		"longer chain": {{ExecutionID: "e1", Status: "scheduled", RetryExecutionID: &retry}, {ExecutionID: "e2", Status: "scheduled"}},
		"other head":   {{ExecutionID: "other", Status: "scheduled"}},
		"empty":        nil,
	}
	for name, chain := range cases {
		t.Run(name, func(t *testing.T) {
			f, tr, exec := settleSubject()
			f.settle.chain = chain
			require.NoError(t, settleNoticeFailed(context.Background(), f, tr, exec))
			assert.Equal(t, []string{"lock:t1"}, f.settle.committed)
		})
	}
}

func TestSettle_TrancheNoLongerFiredIsSkipped(t *testing.T) {
	f, tr, exec := settleSubject()
	f.settle.lockErr = config.ErrLadderTrancheNotFired
	require.NoError(t, settleNoticeFailed(context.Background(), f, tr, exec))
	assert.Equal(t, []string{"lock:t1"}, f.settle.committed)
}

func TestSettle_AFailedCancelOrTransitionRollsEverythingBack(t *testing.T) {
	f, tr, exec := settleSubject()
	f.settle.cancelFails = true
	require.Error(t, settleNoticeFailed(context.Background(), f, tr, exec))
	assert.Empty(t, f.settle.committed)

	f, tr, exec = settleSubject()
	f.settle.moveFails = true
	require.Error(t, settleNoticeFailed(context.Background(), f, tr, exec))
	assert.Empty(t, f.settle.committed, "the cancel must roll back with the failed transition")

	f, tr, exec = settleSubject()
	f.settle.lockErr = errors.New("db down")
	require.Error(t, settleNoticeFailed(context.Background(), f, tr, exec))
}

func TestAnnounce_AFailingSendSettlesTheTrancheWithoutDisablingAnything(t *testing.T) {
	n := &captureNotifier{err: errors.New("ses down")}
	f, res, err := noticeSweep(t, n, func(f *fakeFireStore) {
		f.settle.chain = []config.LadderChainNode{{ExecutionID: "any", Status: "scheduled"}}
	})
	require.NoError(t, err)
	assert.Equal(t, 2, res.NoticeFailed)
	assert.Equal(t, 2, f.settle.withTx, "each failed notice is settled in its own transaction")
	assert.Empty(t, f.stamped)
}

func TestAnnounce_AFailedSettleIsCountedAndLoud(t *testing.T) {
	buf := captureLadderLog(t)
	n := &captureNotifier{err: errors.New("ses down")}
	_, res, err := noticeSweep(t, n, func(f *fakeFireStore) { f.settle.lockErr = errors.New("db down") })
	require.NoError(t, err)
	assert.Equal(t, 2, res.NoticeFailed)
	assert.Equal(t, 2, res.SettleFailed)
	assert.Contains(t, buf.String(), "could not be settled")
}

func TestAnnounce_SettleSurvivesADeadParentContext(t *testing.T) {
	f := newFireStore(fireCfg("c1"))
	f.settle.chain = []config.LadderChainNode{{ExecutionID: "e1", Status: "scheduled"}}
	d := fireDepsFor(f)
	d.Notifier = &captureNotifier{err: errors.New("ses down")}
	exec := newLadderExecution(dueRow("t1", 2.5, true).Tranche, mustBuild(t, 2.5), &config.LadderConfigDB{CloudAccountID: "acct-a"}, 48*time.Hour, fireNow)
	exec.ExecutionID = "e1"
	plan := &ladderFirePlan{global: fireGlobal(nil), acct: &config.CloudAccount{ExternalID: "own-acct"}}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	var res LadderFireResult
	announceClaimed(ctx, d, plan, []config.LadderTrancheDB{{ID: "t1"}}, map[string]*config.PurchaseExecution{"t1": exec}, &res)
	assert.Equal(t, []string{"lock:t1", "cancel:e1", "transition:t1:fired>failed"}, f.settle.committed)
	assert.Zero(t, res.SettleFailed)
}

func TestAnnounce_ASpentSendBudgetStillLeavesTimeToSettle(t *testing.T) {
	prev := ladderNoticeBudget
	ladderNoticeBudget = 30 * time.Millisecond
	t.Cleanup(func() { ladderNoticeBudget = prev })
	f := newFireStore(fireCfg("c1"))
	f.settle.chain = []config.LadderChainNode{{ExecutionID: "e1", Status: "scheduled"}}
	// The send runs to its deadline and fails with it.
	n := &captureNotifier{}
	n.hook = func(email.NotificationData) { time.Sleep(60 * time.Millisecond) }
	n.err = context.DeadlineExceeded
	d := fireDepsFor(f)
	d.Notifier = n
	exec := newLadderExecution(dueRow("t1", 2.5, true).Tranche, mustBuild(t, 2.5), &config.LadderConfigDB{CloudAccountID: "acct-a"}, 48*time.Hour, fireNow)
	exec.ExecutionID = "e1"
	plan := &ladderFirePlan{global: fireGlobal(nil), acct: &config.CloudAccount{ExternalID: "own-acct"}}
	var res LadderFireResult
	announceClaimed(context.Background(), d, plan, []config.LadderTrancheDB{{ID: "t1"}}, map[string]*config.PurchaseExecution{"t1": exec}, &res)
	assert.Zero(t, res.SettleFailed)
	assert.Equal(t, []string{"lock:t1", "cancel:e1", "transition:t1:fired>failed"}, f.settle.committed)
}
