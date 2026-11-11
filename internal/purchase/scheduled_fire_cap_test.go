package purchase

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// #1933: a delayed-approval row that fires after its plan's cap was lowered is
// refused at fire time, as a pending or notified row is after its claim.

// fireCapRow is a due scheduled step of plan-1 whose one recommendation totals
// 300 (upfront only). The id is the one staleRefusalStore expects.
func fireCapRow() config.PurchaseExecution {
	row := dueExec("exec-age")
	row.StepNumber = 1
	row.Recommendations = []config.RecommendationRecord{{
		Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1",
		Count: 1, UpfrontCost: 300, Savings: 60, Selected: true, CloudAccountID: aws.String("acct-fire"),
	}}
	return row
}

func capPlan(cap *float64) *config.PurchasePlan {
	return &config.PurchasePlan{ID: "plan-1", Name: "Plan 1", MaxStepCommitment: cap, RampSchedule: config.RampSchedule{TotalSteps: 1}}
}

func TestFireScheduledDelayedPurchases_RefusesRowOverALoweredPlanCap(t *testing.T) {
	ctx := context.Background()
	store := new(MockConfigStore)
	factory := new(MockProviderFactory)
	failOnProviderCall(factory)
	mailer := new(MockEmailSender)
	mailer.On("SendPurchaseFailedNotification", mock.Anything, mock.Anything).Return(nil).Once()

	lowered := 100.0 // below the 300 the row was approved for
	row := fireCapRow()
	claimed := row
	claimed.Status = "running"
	store.On("GetScheduledExecutionsDue", ctx).Return([]config.PurchaseExecution{row}, nil)
	store.On("TransitionExecutionStatus", ctx, "exec-age", []string{"scheduled"}, "running", (*string)(nil)).Return(&claimed, nil).Once()
	store.On("GetPurchasePlan", mock.Anything, "plan-1").Return(capPlan(&lowered), nil)
	var saved *config.PurchaseExecution
	staleRefusalStore(store, &saved)

	m := &Manager{config: store, email: mailer, providerFactory: factory}
	result, err := m.FireScheduledDelayedPurchases(ctx)
	require.NoError(t, err)

	assert.Equal(t, FireResult{Found: 1, Stale: 1}, *result, "an over-cap row must not buy and is counted with the other claimed-but-refused rows")
	require.NotNil(t, saved, "the refused row must be saved as failed, not left running")
	assert.Equal(t, config.StatusFailed, saved.Status)
	assert.True(t, saved.NoSubmission, "the refusal is proven before submission")
	assert.Contains(t, saved.Error, ErrStepCommitmentCap.Error(), "History must say why the row was refused")
	assert.Contains(t, saved.Error, "exceeds max_step_commitment")
	factory.AssertNotCalled(t, "CreateAndValidateProvider", mock.Anything, mock.Anything, mock.Anything)
	store.AssertExpectations(t)
	mailer.AssertExpectations(t) // operators notified once
}

// A plan that cannot be read refuses the row rather than firing it unchecked.
func TestFireScheduledDelayedPurchases_RefusesRowWhenThePlanCannotBeRead(t *testing.T) {
	ctx := context.Background()
	store := new(MockConfigStore)
	factory := new(MockProviderFactory)
	failOnProviderCall(factory)
	mailer := new(MockEmailSender)
	mailer.On("SendPurchaseFailedNotification", mock.Anything, mock.Anything).Return(nil).Once()

	row := fireCapRow()
	claimed := row
	claimed.Status = "running"
	store.On("GetScheduledExecutionsDue", ctx).Return([]config.PurchaseExecution{row}, nil)
	store.On("TransitionExecutionStatus", ctx, "exec-age", []string{"scheduled"}, "running", (*string)(nil)).Return(&claimed, nil).Once()
	store.On("GetPurchasePlan", mock.Anything, "plan-1").Return((*config.PurchasePlan)(nil), assert.AnError)
	var saved *config.PurchaseExecution
	staleRefusalStore(store, &saved)

	m := &Manager{config: store, email: mailer, providerFactory: factory}
	result, err := m.FireScheduledDelayedPurchases(ctx)
	require.NoError(t, err)

	assert.Equal(t, FireResult{Found: 1, Stale: 1}, *result)
	require.NotNil(t, saved)
	assert.Contains(t, saved.Error, "failed to re-read plan plan-1")
	factory.AssertNotCalled(t, "CreateAndValidateProvider", mock.Anything, mock.Anything, mock.Anything)
}

// fireWithPlan sweeps one due row of plan through fireFixture's real fire path
// and returns the result and the number of provider purchase calls.
func fireWithPlan(t *testing.T, plan *config.PurchasePlan) (FireResult, int) {
	t.Helper()
	mgr, store, svc, _ := fireFixture(t, 0, nil)
	store.ExpectedCalls = nil // replace fireFixture's rows and plan with this case's
	row := fireCapRow()
	row.ExecutionID = "exec-cap-fire"
	claimed := row
	claimed.Status = "running"
	store.On("GetScheduledExecutionsDue", mock.Anything).Return([]config.PurchaseExecution{row}, nil)
	store.On("TransitionExecutionStatus", mock.Anything, row.ExecutionID, []string{"scheduled"}, "running", (*string)(nil)).Return(&claimed, nil)
	store.On("GetPurchasePlan", mock.Anything, "plan-1").Return(plan, nil)
	store.GetPlanAccountsFn = func(_ context.Context, _ string) ([]config.CloudAccount, error) { return nil, nil }
	store.On("GetCloudAccount", mock.Anything, "acct-fire").Return(&config.CloudAccount{
		ID: "acct-fire", Name: "Fire Account", Provider: "aws", ExternalID: "333333333333", AWSAuthMode: "access_keys",
	}, nil)
	store.On("CompletePlanStep", mock.Anything, "plan-1", 1).Return(nil)
	store.On("SavePurchaseHistory", mock.Anything, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil)
	store.On("SaveExecutionIfStatus", mock.Anything, mock.Anything, []string{"running"}).Return(nil)

	result, err := mgr.FireScheduledDelayedPurchases(context.Background())
	require.NoError(t, err)
	purchases := 0
	for _, c := range svc.Calls {
		if c.Method == "PurchaseCommitment" {
			purchases++
		}
	}
	return *result, purchases
}

func TestFireScheduledDelayedPurchases_FiresRowUnderThePlanCap(t *testing.T) {
	roomy := 1000.0

	result, purchases := fireWithPlan(t, capPlan(&roomy))

	assert.Equal(t, 1, result.Fired)
	assert.Equal(t, 1, purchases)
}

// A manual plan with no cap keeps firing at the approved amount: the cap check
// applies only where StepCapApplies does.
func TestFireScheduledDelayedPurchases_FiresRowOfAnUncappedManualPlan(t *testing.T) {
	result, purchases := fireWithPlan(t, capPlan(nil))

	assert.Equal(t, 1, result.Fired)
	assert.Equal(t, 1, purchases)
}

// A direct-execute row (no plan) and a row without recommendations are not this
// check's concern: it must not read a plan for them (the Postgres plan_id column
// rejects "" and a store error would otherwise fail the row, issue #243).
func TestScheduledRowCapRefusal_SkipsRowsWithoutPlanOrRecommendations(t *testing.T) {
	store := new(MockConfigStore) // any plan read fails the test: no expectation is set
	m := &Manager{config: store}

	direct := fireCapRow()
	direct.PlanID = ""
	require.NoError(t, m.scheduledRowCapRefusal(context.Background(), &direct))

	empty := fireCapRow()
	empty.Recommendations = nil
	require.NoError(t, m.scheduledRowCapRefusal(context.Background(), &empty))
	store.AssertNotCalled(t, "GetPurchasePlan", mock.Anything, mock.Anything)
}
