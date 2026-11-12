package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func expiredFailPlan(mutate func(*config.PurchasePlan)) *config.PurchasePlan {
	p := &config.PurchasePlan{ID: "plan-x", Enabled: true, AutoPurchase: true, NotificationDaysBefore: 3}
	if mutate != nil {
		mutate(p)
	}
	return p
}

func TestExpiredAutoRootFailable(t *testing.T) {
	noFourEyes := &config.GlobalConfig{AutoPurchaseEnabled: true}
	for _, tc := range []struct {
		name   string
		plan   *config.PurchasePlan
		global *config.GlobalConfig
		auto   bool
		want   bool
	}{
		{"auto plan, 4-eyes off", expiredFailPlan(nil), noFourEyes, true, true},
		{"4-eyes on keeps the one-shot re-arm", expiredFailPlan(nil), &config.GlobalConfig{RequireDifferentApprover: true}, true, false},
		{"manual plan", expiredFailPlan(func(p *config.PurchasePlan) { p.AutoPurchase = false }), noFourEyes, true, false},
		{"kill switch off stays quiet", expiredFailPlan(nil), noFourEyes, false, false},
		{"disabled plan", expiredFailPlan(func(p *config.PurchasePlan) { p.Enabled = false }), noFourEyes, true, false},
		{"dates managed plan is unchanged", expiredFailPlan(func(p *config.PurchasePlan) { p.RampSchedule.DatesManaged = true }), noFourEyes, true, false},
		{"nil global fails closed", expiredFailPlan(nil), nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, expiredAutoRootFailable(tc.plan, tc.global, tc.auto))
		})
	}
}

type expiredFailFixture struct {
	store  *MockConfigStore
	sender *MockEmailSender
	m      *Manager
	plan   *config.PurchasePlan
	global *config.GlobalConfig
}

func newExpiredFailFixture(t *testing.T, plan *config.PurchasePlan, rows ...config.PurchaseExecution) *expiredFailFixture {
	t.Helper()
	f := &expiredFailFixture{store: new(MockConfigStore), sender: new(MockEmailSender), plan: plan, global: &config.GlobalConfig{AutoPurchaseEnabled: true}}
	f.m = &Manager{config: f.store, email: f.sender}
	t.Cleanup(func() { f.store.AssertExpectations(t) })
	locked := *plan
	f.store.On("LockPurchasePlanTx", mock.Anything, mock.Anything, plan.ID).Return(&locked, nil).Maybe()
	f.store.On("ListExecutionsForPlanStepTx", mock.Anything, mock.Anything, plan.ID, plan.RampSchedule.CurrentStep+1).Return(rows, nil)
	f.store.On("GetGlobalConfig", mock.Anything).Return(&config.GlobalConfig{}, nil).Maybe()
	return f
}

func expiredRootRow(mutate func(*config.PurchaseExecution)) config.PurchaseExecution {
	return stepRow(config.StatusExpired, func(e *config.PurchaseExecution) {
		e.StepNumber = 1
		if mutate != nil {
			mutate(e)
		}
	})
}

func TestFailExpiredAutoRoot_FailsTheLoneExpiredRootOnceWithoutBuying(t *testing.T) {
	ctx := context.Background()
	root := expiredRootRow(nil)
	f := newExpiredFailFixture(t, expiredFailPlan(nil), root)
	store, sender := f.store, f.sender
	failed := root
	failed.Status = config.StatusFailed
	store.On("TransitionExecutionStatusPinnedTx", mock.Anything, mock.Anything, root.ExecutionID, []string{config.StatusExpired}, config.StatusFailed, (*string)(nil), false).Return(&failed, nil).Once()
	store.On("SaveExecutionIfStatusTx", mock.Anything, mock.Anything, &failed, []string{config.StatusFailed}).Return(nil).Once()
	store.On("DeleteSuppressionsByExecutionTx", mock.Anything, mock.Anything, root.ExecutionID).Return(nil).Once()
	sender.On("SendPurchaseFailedNotification", mock.Anything, mock.Anything).Return(nil).Once()

	require.NoError(t, f.m.failExpiredAutoRoot(ctx, f.plan, f.global, true))

	assert.Equal(t, config.StatusFailed, failed.Status)
	assert.True(t, failed.NoSubmission, "an expired root never reached the executor")
	assert.Contains(t, failed.Error, "expired approval root on an auto-purchase plan without four-eyes: operator must re-arm the plan")
	sender.AssertNumberOfCalls(t, "SendPurchaseFailedNotification", 1)
	store.AssertNotCalled(t, "SavePurchaseExecution")
}

// Idempotent: once the root is failed, later ticks find no expired root and
// write and notify nothing.
func TestFailExpiredAutoRoot_AlreadyFailedRootIsLeftAlone(t *testing.T) {
	f := newExpiredFailFixture(t, expiredFailPlan(nil), stepRow(config.StatusFailed, func(e *config.PurchaseExecution) { e.StepNumber = 1 }))
	store, sender := f.store, f.sender
	for range 3 {
		require.NoError(t, f.m.failExpiredAutoRoot(context.Background(), f.plan, f.global, true))
	}
	store.AssertNotCalled(t, "TransitionExecutionStatusPinnedTx")
	store.AssertNotCalled(t, "LockPurchasePlanTx")
	sender.AssertNumberOfCalls(t, "SendPurchaseFailedNotification", 0)
}

func TestFailExpiredAutoRoot_RowsThatMayHaveBeenBoughtOrActedOnAreLeftAlone(t *testing.T) {
	acct, now := "acct-1", time.Now()
	for name, rows := range map[string][]config.PurchaseExecution{
		"failed sibling": {expiredRootRow(nil), stepRow("failed", func(e *config.PurchaseExecution) { e.ExecutionID = "row-2" })},
		"approved":       {expiredRootRow(func(e *config.PurchaseExecution) { e.ApprovedBy = &acct })},
		"executed":       {expiredRootRow(func(e *config.PurchaseExecution) { e.ExecutedAt = &now })},
		"purchased rec": {expiredRootRow(func(e *config.PurchaseExecution) {
			e.Recommendations = []config.RecommendationRecord{{ID: "r", PurchaseID: "p"}}
		})},
		"account child":   {expiredRootRow(func(e *config.PurchaseExecution) { e.CloudAccountID = &acct })},
		"live pending":    {stepRow("pending", func(e *config.PurchaseExecution) { e.StepNumber = 1 })},
		"no rows at all":  nil,
		"completed":       {stepRow("completed", func(e *config.PurchaseExecution) { e.StepNumber = 1 })},
		"expired + retry": {expiredRootRow(func(e *config.PurchaseExecution) { e.RetryAttemptN = 1 })},
	} {
		t.Run(name, func(t *testing.T) {
			f := newExpiredFailFixture(t, expiredFailPlan(nil), rows...)
			store, sender := f.store, f.sender
			require.NoError(t, f.m.failExpiredAutoRoot(context.Background(), f.plan, f.global, true))
			store.AssertNotCalled(t, "TransitionExecutionStatusPinnedTx")
			store.AssertNotCalled(t, "LockPurchasePlanTx")
			sender.AssertNumberOfCalls(t, "SendPurchaseFailedNotification", 0)
		})
	}
}

func TestFailExpiredAutoRoot_UngatedPlansDoNotEvenOpenATransaction(t *testing.T) {
	ctx := context.Background()
	store := new(MockConfigStore)
	m := &Manager{config: store}
	fourEyes := &config.GlobalConfig{RequireDifferentApprover: true}
	require.NoError(t, m.failExpiredAutoRoot(ctx, expiredFailPlan(nil), fourEyes, true))
	require.NoError(t, m.failExpiredAutoRoot(ctx, expiredFailPlan(func(p *config.PurchasePlan) { p.RampSchedule.DatesManaged = true }), &config.GlobalConfig{}, true))
	require.NoError(t, m.failExpiredAutoRoot(ctx, expiredFailPlan(func(p *config.PurchasePlan) { p.AutoPurchase = false }), &config.GlobalConfig{}, true))
	store.AssertNotCalled(t, "WithTx")
}

// The plan can change between the tick's list and the lock: the locked plan wins.
func TestFailExpiredAutoRoot_LockedPlanGatesAreRechecked(t *testing.T) {
	for name, mutate := range map[string]func(*config.PurchasePlan){
		"disabled meanwhile":      func(p *config.PurchasePlan) { p.Enabled = false },
		"manual meanwhile":        func(p *config.PurchasePlan) { p.AutoPurchase = false },
		"dates managed meanwhile": func(p *config.PurchasePlan) { p.RampSchedule.DatesManaged = true },
	} {
		t.Run(name, func(t *testing.T) {
			listed := expiredFailPlan(nil)
			store := new(MockConfigStore)
			locked := *listed
			mutate(&locked)
			store.On("LockPurchasePlanTx", mock.Anything, mock.Anything, listed.ID).Return(&locked, nil)
			store.On("ListExecutionsForPlanStepTx", mock.Anything, mock.Anything, listed.ID, 1).Return([]config.PurchaseExecution{expiredRootRow(nil)}, nil).Once()
			m := &Manager{config: store}
			require.NoError(t, m.failExpiredAutoRoot(context.Background(), listed, &config.GlobalConfig{}, true))
			store.AssertExpectations(t) // the pre-check read ran once, the locked re-check stopped before a second read
			store.AssertNotCalled(t, "TransitionExecutionStatusPinnedTx")
		})
	}
}

// A human cannot touch an expired row, but a lost CAS is still the quiet skip.
func TestFailExpiredAutoRoot_LostCASIsQuiet(t *testing.T) {
	root := expiredRootRow(nil)
	f := newExpiredFailFixture(t, expiredFailPlan(nil), root)
	store, sender := f.store, f.sender
	store.On("TransitionExecutionStatusPinnedTx", mock.Anything, mock.Anything, root.ExecutionID, []string{config.StatusExpired}, config.StatusFailed, (*string)(nil), false).Return(nil, config.ErrExecutionNotInExpectedStatus)
	require.NoError(t, f.m.failExpiredAutoRoot(context.Background(), f.plan, f.global, true))
	sender.AssertNumberOfCalls(t, "SendPurchaseFailedNotification", 0)
	store.AssertNotCalled(t, "SaveExecutionIfStatusTx")
}

// A failed write rolls the transaction back: the tick reports it and nothing is notified.
func TestFailExpiredAutoRoot_WriteErrorIsReportedAndNotNotified(t *testing.T) {
	root := expiredRootRow(nil)
	f := newExpiredFailFixture(t, expiredFailPlan(nil), root)
	store, sender := f.store, f.sender
	failed := root
	failed.Status = config.StatusFailed
	store.On("TransitionExecutionStatusPinnedTx", mock.Anything, mock.Anything, root.ExecutionID, []string{config.StatusExpired}, config.StatusFailed, (*string)(nil), false).Return(&failed, nil)
	store.On("SaveExecutionIfStatusTx", mock.Anything, mock.Anything, &failed, []string{config.StatusFailed}).Return(errors.New("boom"))
	require.Error(t, f.m.failExpiredAutoRoot(context.Background(), f.plan, f.global, true))
	sender.AssertNumberOfCalls(t, "SendPurchaseFailedNotification", 0)
	store.AssertNotCalled(t, "DeleteSuppressionsByExecutionTx")
}
