//go:build integration

package purchase

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Negative and gate cases of the expired-root re-adoption (#1785). Nothing is
// adopted on the old code either, so these pass before and after; each is the
// second net behind the named layer test of its mutation probe. The
// fail-before cases are in expired_root_readopt_integration_test.go.

// snapshot is every column a re-adoption would touch, for all rows of the plan.
func (f *readoptFixture) snapshot(ctx context.Context, t *testing.T, planID string) string {
	t.Helper()
	rows, err := f.pool.Query(ctx, `SELECT execution_id || '|' || status || '|' || scheduled_date::text || '|' || recommendations::text || '|' ||
		COALESCE(approval_token_hash, '') || '|' || COALESCE(approval_readopted_at::text, '') || '|' || updated_at::text
		FROM purchase_executions WHERE plan_id = $1 ORDER BY execution_id`, planID)
	require.NoError(t, err)
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		lines = append(lines, line)
	}
	require.NoError(t, rows.Err())
	return strings.Join(lines, "\n")
}

// saveRow stores one more row at the step of the plan.
func (f *readoptFixture) saveRow(ctx context.Context, t *testing.T, plan *config.PurchasePlan, status string, mutate func(*config.PurchaseExecution)) {
	t.Helper()
	stale := rampStepRecommendation()
	stale[0].CloudAccountID = &f.accounts[0].ID
	row := &config.PurchaseExecution{
		PlanID: plan.ID, ExecutionID: uuid.NewString(), IdempotencyKey: uuid.NewString(), Status: status, StepNumber: 1,
		ScheduledDate: plan.NextExecutionDate.Truncate(time.Microsecond), Recommendations: stale, CreatedByUserID: &f.userA,
	}
	if mutate != nil {
		mutate(row)
	}
	require.NoError(t, f.store.SavePurchaseExecution(ctx, row))
}

// assertUntouched runs a tick and requires: no email, no marker, every plan's rows byte-identical.
func (f *readoptFixture) assertUntouched(ctx context.Context, t *testing.T, plans map[string]*config.PurchasePlan) {
	t.Helper()
	before := map[string]string{}
	for name, p := range plans {
		before[name] = f.snapshot(ctx, t, p.ID)
	}
	assert.Zero(t, f.tick(ctx, t))
	assert.Empty(t, sentTokens(f.sender))
	for name, p := range plans {
		assert.Equal(t, before[name], f.snapshot(ctx, t, p.ID), name)
	}
}

// E3: a step that may already have been bought, or that holds anything besides
// the one expired root, is never re-armed.
func TestReadoptExpiredRoot_NonReadoptableStepsAreLeftByteIdentical(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	now := time.Now()
	acct := f.accounts[0].ID
	plans := map[string]*config.PurchasePlan{}
	for _, status := range []string{"canceled", "failed", "partially_completed", "completed", "running", "approved", "notified"} {
		plan := f.addPlan(ctx, t, "e3-root-"+status, nil)
		f.saveRow(ctx, t, plan, status, func(e *config.PurchaseExecution) {
			if status == "completed" || status == "partially_completed" {
				e.CompletedAt = &now
			}
		})
		plans["root "+status] = plan
	}
	siblings := map[string]func(*config.PurchaseExecution){
		"failed child with an account": func(e *config.PurchaseExecution) { e.CloudAccountID = &acct; e.Error = "boom" },
		"canceled root":                func(e *config.PurchaseExecution) { e.Status = "canceled" },
		"retry row":                    func(e *config.PurchaseExecution) { e.RetryAttemptN = 1 },
	}
	for name, mutate := range siblings {
		plan := f.addPlan(ctx, t, "e3-expired-plus-"+strings.ReplaceAll(name, " ", "-"), nil)
		f.seedExpiredRoot(ctx, t, plan, 0)
		status := "failed"
		if name == "retry row" {
			status = "pending"
		}
		f.saveRow(ctx, t, plan, status, mutate)
		plans["expired plus "+name] = plan
	}
	f.assertUntouched(ctx, t, plans)
}

// E4: parallel ticks and a stale history sweep over one adoptable root adopt it
// once, email once and leave one suppression set.
func TestReadoptExpiredRoot_ParallelTicksAndAStaleSweepAdoptOnce(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	plan := f.addPlan(ctx, t, "readopt-e4", nil)
	rootID := f.seedExpiredRoot(ctx, t, plan, 0)
	observed := plan.NextExecutionDate.Truncate(time.Microsecond)

	f.exec(ctx, t, `UPDATE purchase_plans SET last_notification_sent = NULL`)
	var wg sync.WaitGroup
	tickErrs := make([]error, 4)
	for i := range tickErrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, tickErrs[i] = f.manager.SendUpcomingPurchaseNotifications(ctx)
		}()
	}
	sweepErrs := make([]error, 20)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range sweepErrs {
			sweepErrs[i] = f.store.WithTx(ctx, func(tx pgx.Tx) error {
				_, err := f.store.ExpireExecutionAtomic(ctx, tx, rootID, observed)
				return err
			})
		}
	}()
	wg.Wait()
	for _, err := range append(tickErrs, sweepErrs...) {
		require.NoError(t, err)
	}

	assert.Len(t, sentTokens(f.sender), 1)
	assert.Equal(t, "notified", rowStatus(ctx, t, f.rampStepFixture, rootID).Status)
	assert.Equal(t, 20, f.suppressed(ctx, t, rootID))
	assert.NotNil(t, f.marker(ctx, t, rootID))
}

// E5: plan gates. Each plan has an adoptable root; none is adopted and no tick errors.
func TestReadoptExpiredRoot_PlanGatesBlockAdoption(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	global, err := f.store.GetGlobalConfig(ctx)
	require.NoError(t, err)
	global.AutoPurchaseEnabled = false
	require.NoError(t, f.store.SaveGlobalConfig(ctx, global))

	plans := map[string]*config.PurchasePlan{
		"disabled":                   f.addPlan(ctx, t, "e5-disabled", func(p *config.PurchasePlan) { p.Enabled = false }),
		"auto plan, kill switch off": f.addPlan(ctx, t, "e5-auto", func(p *config.PurchasePlan) { p.AutoPurchase = true }),
		"no veto window":             f.addPlan(ctx, t, "e5-noveto", nil),
		"dates managed":              f.addPlan(ctx, t, "e5-managed", nil),
	}
	f.exec(ctx, t, `UPDATE purchase_plans SET notification_days_before = 0 WHERE id = $1`, plans["no veto window"].ID)
	f.exec(ctx, t, `UPDATE purchase_plans SET ramp_schedule = ramp_schedule || '{"dates_managed":true}' WHERE id = $1`, plans["dates managed"].ID)
	for _, p := range plans {
		f.seedExpiredRoot(ctx, t, p, 0)
	}
	f.assertUntouched(ctx, t, plans)
}

// E6: an auto plan with 4-eyes off would auto-buy the re-armed row with the
// email as the only veto, so it is not adopted (#1785). It is failed visibly
// instead of staying expired and silent (#1904). E6a: with 4-eyes on it is
// adopted and the scheduler still does not buy it.
func TestReadoptExpiredRoot_AutoPlanWithFourEyesOnIsAdoptedNotFailed(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	plan := f.addPlan(ctx, t, "readopt-e6a", func(p *config.PurchasePlan) { p.AutoPurchase = true })
	rootID := f.seedExpiredRoot(ctx, t, plan, 0)

	require.Equal(t, 1, f.tick(ctx, t))
	row := rowStatus(ctx, t, f.rampStepFixture, rootID)
	assert.Equal(t, "notified", row.Status)
	assert.Empty(t, row.Error)
	eligible, err := f.manager.executableByScheduler(ctx, row)
	require.NoError(t, err)
	assert.False(t, eligible)
	assert.Zero(t, f.purchaseCount())
}

// failedNoticeCount counts the purchase-failed emails the sender received.
func failedNoticeCount(s *countingSender) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.Calls {
		if c.Method == "SendPurchaseFailedNotification" {
			n++
		}
	}
	return n
}

// E6: #1904 fail-before: on the old code the expired root of an auto plan with
// 4-eyes off stays expired forever (no failure, no notice). The plan's date is
// 20 days old, past the grace window, so the claim path is never reached: the
// failure has to come from the tick's own expired-root pass.
func TestFailExpiredAutoRoot_AutoPlanWithoutFourEyesFailsVisiblyOnceAndNeverBuys(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	f.sender.On("SendPurchaseFailedNotification", mock.Anything, mock.Anything).Return(nil).Maybe()
	plan := f.addPlan(ctx, t, "failexp-e6", func(p *config.PurchasePlan) { p.AutoPurchase = true })
	rootID := f.seedExpiredRoot(ctx, t, plan, 0)
	setFourEyes(ctx, t, f.rampStepFixture, false)

	assert.Zero(t, f.tick(ctx, t))

	row := rowStatus(ctx, t, f.rampStepFixture, rootID)
	assert.Equal(t, "failed", row.Status)
	assert.Equal(t, errExpiredAutoRootNeedsRearm.Error(), row.Error)
	assert.True(t, row.NoSubmission)
	assert.Nil(t, f.marker(ctx, t, rootID), "never re-armed")
	assert.Zero(t, f.suppressed(ctx, t, rootID))
	assert.Equal(t, 1, failedNoticeCount(f.sender))
	assert.Empty(t, sentTokens(f.sender), "no approval email")
	assert.Zero(t, f.purchaseCount())

	// Idempotent: later ticks write and send nothing, including after 4-eyes
	// is turned on (a failed root is never adopted).
	before := f.snapshot(ctx, t, plan.ID)
	for range 3 {
		assert.Zero(t, f.tick(ctx, t))
	}
	setFourEyes(ctx, t, f.rampStepFixture, true)
	assert.Zero(t, f.tick(ctx, t))
	assert.Equal(t, before, f.snapshot(ctx, t, plan.ID))
	assert.Equal(t, 1, failedNoticeCount(f.sender))
	assert.Empty(t, sentTokens(f.sender))
	assert.Zero(t, f.purchaseCount())
}

// E6b: the gates around the failure. Each plan has a lone expired root and
// 4-eyes is off; only the enabled auto plan with the kill switch on is failed,
// and a paused plan is failed on the first tick after the switch is back on.
func TestFailExpiredAutoRoot_OnlyTheEnabledAutoPlanWithTheKillSwitchOnFails(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	f.sender.On("SendPurchaseFailedNotification", mock.Anything, mock.Anything).Return(nil).Maybe()
	setFourEyes(ctx, t, f.rampStepFixture, false)
	untouched := map[string]*config.PurchasePlan{
		"disabled auto": f.addPlan(ctx, t, "failexp-disabled", func(p *config.PurchasePlan) { p.AutoPurchase, p.Enabled = true, false }),
		"managed auto":  f.addPlan(ctx, t, "failexp-managed", func(p *config.PurchasePlan) { p.AutoPurchase = true }),
	}
	f.exec(ctx, t, `UPDATE purchase_plans SET ramp_schedule = ramp_schedule || '{"dates_managed":true}' WHERE id = $1`, untouched["managed auto"].ID)
	paused := f.addPlan(ctx, t, "failexp-paused", func(p *config.PurchasePlan) { p.AutoPurchase = true })
	pausedRoot := f.seedExpiredRoot(ctx, t, paused, 0)
	for _, p := range untouched {
		f.seedExpiredRoot(ctx, t, p, 0)
	}
	global, err := f.store.GetGlobalConfig(ctx)
	require.NoError(t, err)
	global.AutoPurchaseEnabled = false
	require.NoError(t, f.store.SaveGlobalConfig(ctx, global))
	f.assertUntouched(ctx, t, map[string]*config.PurchasePlan{"disabled auto": untouched["disabled auto"],
		"managed auto": untouched["managed auto"], "auto, kill switch off": paused})
	assert.Zero(t, failedNoticeCount(f.sender))

	global.AutoPurchaseEnabled = true
	require.NoError(t, f.store.SaveGlobalConfig(ctx, global))
	f.tick(ctx, t)
	assert.Equal(t, "failed", rowStatus(ctx, t, f.rampStepFixture, pausedRoot).Status)
	assert.Equal(t, 1, failedNoticeCount(f.sender))
	for name, p := range untouched {
		for _, r := range stepRowsInDB(ctx, t, f.store, p.ID) {
			assert.Equal(t, "expired", r.Status, name)
		}
	}
}

// E6c: a step that may already have been bought is never failed by this pass.
func TestFailExpiredAutoRoot_StepWithASiblingIsLeftByteIdentical(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	setFourEyes(ctx, t, f.rampStepFixture, false)
	plan := f.addPlan(ctx, t, "failexp-sibling", func(p *config.PurchasePlan) { p.AutoPurchase = true })
	f.seedExpiredRoot(ctx, t, plan, 0)
	acct := f.accounts[0].ID
	f.saveRow(ctx, t, plan, "failed", func(e *config.PurchaseExecution) { e.CloudAccountID = &acct; e.Error = "boom" })
	f.assertUntouched(ctx, t, map[string]*config.PurchasePlan{"expired plus failed child": plan})
	assert.Zero(t, failedNoticeCount(f.sender))
}

// E6d: a root that was already re-armed once (while four-eyes was on) and
// expired again is failed too, once four-eyes is off: the same stall.
func TestFailExpiredAutoRoot_ReadoptedThenExpiredAgainIsFailedToo(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	f.sender.On("SendPurchaseFailedNotification", mock.Anything, mock.Anything).Return(nil).Maybe()
	plan := f.addPlan(ctx, t, "failexp-readopted", func(p *config.PurchasePlan) { p.AutoPurchase = true })
	rootID := f.seedExpiredRoot(ctx, t, plan, 0)
	f.exec(ctx, t, `UPDATE purchase_executions SET approval_readopted_at = NOW() WHERE execution_id = $1`, rootID)
	setFourEyes(ctx, t, f.rampStepFixture, false)

	f.tick(ctx, t)

	assert.Equal(t, "failed", rowStatus(ctx, t, f.rampStepFixture, rootID).Status)
	assert.Equal(t, 1, failedNoticeCount(f.sender))
	assert.Zero(t, f.purchaseCount())
}

// E8: an expired root older than the 30-day bound is not adopted.
func TestReadoptExpiredRoot_RootExpiredLongAgoIsNotAdopted(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	plan := f.addPlan(ctx, t, "readopt-e8", nil)
	f.seedExpiredRoot(ctx, t, plan, config.ReadoptMaxExpiredAge+24*time.Hour)
	f.assertUntouched(ctx, t, map[string]*config.PurchasePlan{"90 days old": plan})
}
