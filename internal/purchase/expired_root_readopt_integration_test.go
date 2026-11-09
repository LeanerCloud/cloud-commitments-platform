//go:build integration

package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Re-adopting an expired, unapproved plan-step root once (#1785). Real
// PostgreSQL, the real resolver, tick, claim, approval and executor; the
// recommendation read, the email sender and the cloud are fixtures.

const readoptOldToken = "readopt-old-token"

type readoptFixture struct {
	*rampStepFixture
	sender       *countingSender
	userA, userB string
}

// newReadoptFixture runs the ramp-step fixture with 4-eyes on, the kill switch
// on, a notification address and a resolver that always shows one 100-unit rec
// of the fixture's resolvable account.
func newReadoptFixture(ctx context.Context, t *testing.T) *readoptFixture {
	t.Helper()
	f := &readoptFixture{rampStepFixture: newRampStepFixture(ctx, t)}
	attachGlobal(ctx, t, f.store)
	setFourEyes(ctx, t, f.rampStepFixture, true)
	f.userA, f.userB = fourEyesUser(ctx, t, f.rampStepFixture, "a"), fourEyesUser(ctx, t, f.rampStepFixture, "b")
	rec := rampStepRecommendation()[0]
	rec.ID, rec.Count, rec.Term, rec.Payment, rec.Savings = "readopt-rec", 100, 1, "no-upfront", 10
	rec.CloudAccountID = &f.accounts[0].ID
	rows := []config.RecommendationRecord{rec}
	f.sender = newCountingSender(ctx)
	f.sender.On("SendPurchaseConfirmation", mock.Anything, mock.Anything).Return(nil).Maybe()
	f.manager.email = f.sender
	f.manager.resolver = planstepResolver{src: &cumSource{shown: func() []config.RecommendationRecord { return rows }}, store: cumStore{rows: rows}}
	return f
}

// addPlan stores a manual plan whose step 1 date was 20 days ago (a stall
// longer than one weekly interval) and whose only account is the resolvable one.
func (f *readoptFixture) addPlan(ctx context.Context, t *testing.T, name string, mutate func(*config.PurchasePlan)) *config.PurchasePlan {
	t.Helper()
	next := time.Now().Add(-20 * 24 * time.Hour).Truncate(time.Microsecond)
	plan := &config.PurchasePlan{
		Name: name, Enabled: true, NotificationDaysBefore: 3, MaxStepCommitment: capOf(1e9),
		Services:          map[string]config.ServiceConfig{"rds": {Provider: "aws", Service: "rds", Enabled: true, Term: 1, Payment: "no-upfront", Coverage: 80}},
		RampSchedule:      config.RampSchedule{Type: "weekly", PercentPerStep: 25, StepIntervalDays: 7, TotalSteps: 4, StartDate: next},
		NextExecutionDate: &next,
	}
	if mutate != nil {
		mutate(plan)
	}
	require.NoError(t, plan.Validate())
	require.NoError(t, f.store.CreatePurchasePlan(ctx, plan))
	require.NoError(t, f.store.SetPlanAccounts(ctx, plan.ID, []string{f.accounts[0].ID}))
	return plan
}

// seedExpiredRoot stores a root of the plan that was emailed (old token, stale
// recommendations) at step 1 and then expired by the real sweep body, aged by age.
func (f *readoptFixture) seedExpiredRoot(ctx context.Context, t *testing.T, plan *config.PurchasePlan, age time.Duration) string {
	t.Helper()
	expires := time.Now().Add(24 * time.Hour)
	stale := rampStepRecommendation()
	stale[0].CloudAccountID = &f.accounts[0].ID
	row := &config.PurchaseExecution{
		PlanID: plan.ID, ExecutionID: uuid.NewString(), IdempotencyKey: uuid.NewString(), Status: "notified", StepNumber: 1,
		ScheduledDate: plan.NextExecutionDate.Truncate(time.Microsecond), Recommendations: stale,
		ApprovalToken: config.HashApprovalToken(readoptOldToken), ApprovalTokenExpiresAt: &expires, CreatedByUserID: &f.userA,
	}
	require.NoError(t, f.store.SavePurchaseExecution(ctx, row))
	require.True(t, expireObserved(ctx, t, f.store, row.ExecutionID, row.ScheduledDate))
	// The updated_at trigger stamps every UPDATE, so it is off while aging the row.
	f.exec(ctx, t, `ALTER TABLE purchase_executions DISABLE TRIGGER update_purchase_executions_updated_at`)
	f.exec(ctx, t, `UPDATE purchase_executions SET updated_at = $2 WHERE execution_id = $1`, row.ExecutionID, time.Now().Add(-age))
	f.exec(ctx, t, `ALTER TABLE purchase_executions ENABLE TRIGGER update_purchase_executions_updated_at`)
	return row.ExecutionID
}

// addEmptyRoot stores the pre-created empty root of a later step.
func (f *readoptFixture) addEmptyRoot(ctx context.Context, t *testing.T, plan *config.PurchasePlan, step int, date time.Time) {
	t.Helper()
	require.NoError(t, f.store.SavePurchaseExecution(ctx, &config.PurchaseExecution{
		PlanID: plan.ID, ExecutionID: uuid.NewString(), IdempotencyKey: uuid.NewString(), Status: "pending", StepNumber: step,
		ScheduledDate: date.Truncate(time.Microsecond), CreatedByUserID: &f.userA,
	}))
}

func (f *readoptFixture) exec(ctx context.Context, t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := f.pool.Exec(ctx, sql, args...)
	require.NoError(t, err)
}

// tick runs one notification tick, first lifting the 24h plan notification
// throttle that the previous tick set.
func (f *readoptFixture) tick(ctx context.Context, t *testing.T) int {
	t.Helper()
	f.exec(ctx, t, `UPDATE purchase_plans SET last_notification_sent = NULL`)
	result, err := f.manager.SendUpcomingPurchaseNotifications(ctx)
	require.NoError(t, err)
	return result.Notified
}

func (f *readoptFixture) marker(ctx context.Context, t *testing.T, id string) *time.Time {
	t.Helper()
	var at *time.Time
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT approval_readopted_at FROM purchase_executions WHERE execution_id = $1`, id).Scan(&at))
	return at
}

func (f *readoptFixture) suppressed(ctx context.Context, t *testing.T, id string) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT COALESCE(SUM(suppressed_count), 0) FROM purchase_suppressions WHERE execution_id = $1`, id).Scan(&n))
	return n
}

func sentTokens(s *countingSender) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.Calls {
		if c.Method == "SendScheduledPurchaseNotification" {
			out = append(out, c.Arguments.Get(1).(email.NotificationData).ApprovalToken)
		}
	}
	return out
}

// E1: the happy path, and the honest limit. Fail-before: on the old code the
// expired root is never adopted, so no email goes out and the step stays expired.
func TestReadoptExpiredRoot_HappyPathThenTheRampStallsAgainAtTheNextStep(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	plan := f.addPlan(ctx, t, "readopt-e1", nil)
	rootID := f.seedExpiredRoot(ctx, t, plan, 0)
	f.addEmptyRoot(ctx, t, plan, 2, plan.NextExecutionDate.AddDate(0, 0, 7)) // 13 days ago: due once step 1 is bought

	require.Equal(t, 1, f.tick(ctx, t))

	tokens := sentTokens(f.sender)
	require.Len(t, tokens, 1)
	assert.NotEqual(t, readoptOldToken, tokens[0])
	row, err := f.store.GetExecutionByID(ctx, rootID)
	require.NoError(t, err)
	assert.Equal(t, "notified", row.Status)
	assert.True(t, config.ApprovalTokenMatches(row.ApprovalToken, tokens[0]))
	assert.WithinDuration(t, time.Now().Add(3*24*time.Hour), row.ScheduledDate, time.Minute, "a full veto window from the re-arm")
	require.Len(t, row.Recommendations, 1)
	assert.Equal(t, "readopt-rec", row.Recommendations[0].ID, "fresh recommendations replace the stale ones")
	assert.Equal(t, 20, row.Recommendations[0].Count)
	assert.Equal(t, 20, f.suppressed(ctx, t, rootID))
	assert.NotNil(t, f.marker(ctx, t, rootID))
	require.NotNil(t, row.CreatedByUserID)
	assert.Equal(t, f.userA, *row.CreatedByUserID, "4-eyes identity is untouched")

	_, _, err = f.manager.ApproveExecution(ctx, rootID, readoptOldToken, "a@example.invalid", nil)
	require.ErrorIs(t, err, ErrInvalidApprovalToken)
	_, _, err = f.manager.ApproveAndExecute(ctx, rootID, "a@example.invalid", &f.userA, nil)
	require.ErrorIs(t, err, ErrFourEyesDenied)
	_, _, err = f.manager.ApproveAndExecute(ctx, rootID, "b@example.invalid", &f.userB, nil)
	require.NoError(t, err)
	assert.Equal(t, "completed", rowStatus(ctx, t, f.rampStepFixture, rootID).Status)
	assert.Equal(t, 1, f.purchaseCount())
	stored, err := f.store.GetPurchasePlan(ctx, plan.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, stored.RampSchedule.CurrentStep)

	// Limit (RF5): dates are absolute, so step 2 is already overdue and the next
	// tick fails its empty root; the ramp does not necessarily recover.
	require.NotNil(t, stored.NextExecutionDate)
	assert.True(t, stored.NextExecutionDate.Before(time.Now()))
	assert.Zero(t, f.tick(ctx, t))
	var step2Status string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT status FROM purchase_executions WHERE plan_id = $1 AND step_number = 2`, plan.ID).Scan(&step2Status))
	assert.Equal(t, "failed", step2Status)
	assert.Equal(t, 1, f.purchaseCount(), "no second purchase")
}

// E1b: the adoption commits but the email fails. Fail-before: nothing is adopted
// on the old code. The old token is already dead, a session approval still works
// and the next tick sends nothing more.
func TestReadoptExpiredRoot_EmailFailureAfterAdoptionLeavesAnApprovablePendingRow(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	failing := new(MockEmailSender)
	failing.On("SendScheduledPurchaseNotification", mock.Anything, mock.Anything).Return(errors.New("smtp down"))
	failing.On("SendPurchaseConfirmation", mock.Anything, mock.Anything).Return(nil).Maybe()
	f.manager.email = failing
	plan := f.addPlan(ctx, t, "readopt-e1b", nil)
	rootID := f.seedExpiredRoot(ctx, t, plan, 0)

	assert.Zero(t, f.tick(ctx, t))
	assert.Zero(t, f.tick(ctx, t))

	failing.AssertNumberOfCalls(t, "SendScheduledPurchaseNotification", 1)
	row, err := f.store.GetExecutionByID(ctx, rootID)
	require.NoError(t, err)
	assert.Equal(t, "pending", row.Status)
	assert.Empty(t, row.ApprovalToken, "the old token hash was cleared and no new one stored")
	require.Len(t, row.Recommendations, 1)
	assert.NotNil(t, f.marker(ctx, t, rootID))
	_, _, err = f.manager.ApproveExecution(ctx, rootID, readoptOldToken, "a@example.invalid", nil)
	require.ErrorIs(t, err, ErrInvalidApprovalToken)
	_, _, err = f.manager.ApproveAndExecute(ctx, rootID, "b@example.invalid", &f.userB, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, f.purchaseCount())
}

// E2: the once. A second expiry of the re-armed root is final.
func TestReadoptExpiredRoot_SecondExpiryIsNotReadopted(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	plan := f.addPlan(ctx, t, "readopt-e2", nil)
	rootID := f.seedExpiredRoot(ctx, t, plan, 0)
	require.Equal(t, 1, f.tick(ctx, t))
	marked := f.marker(ctx, t, rootID)
	require.NotNil(t, marked)

	f.exec(ctx, t, `UPDATE purchase_executions SET scheduled_date = $2 WHERE execution_id = $1`, rootID, time.Now().Add(-8*24*time.Hour))
	row, err := f.store.GetExecutionByID(ctx, rootID)
	require.NoError(t, err)
	require.True(t, expireObserved(ctx, t, f.store, rootID, row.ScheduledDate))

	assert.Zero(t, f.tick(ctx, t))
	assert.Len(t, sentTokens(f.sender), 1, "no second email")
	assert.Equal(t, "expired", rowStatus(ctx, t, f.rampStepFixture, rootID).Status)
	assert.True(t, marked.Equal(*f.marker(ctx, t, rootID)), "the marker is unchanged")
	listed, err := f.store.ListPlansWithReadoptableExpiredRoot(ctx, time.Now(), true)
	require.NoError(t, err)
	assert.NotContains(t, listed, plan.ID)
}

// E7: characterization, true before and after the change. An expired root still
// occupies its ramp step, so a new root for the step is refused.
func TestReadoptExpiredRoot_ExpiredRootStillOccupiesItsStep(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	plan := f.addPlan(ctx, t, "readopt-e7", nil)
	f.seedExpiredRoot(ctx, t, plan, 0)
	var occupied []int
	require.NoError(t, f.store.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		occupied, err = f.store.OccupiedRampStepsInRangeTx(ctx, tx, plan.ID, 1, 4)
		return err
	}))
	assert.Equal(t, []int{1}, occupied)
}
