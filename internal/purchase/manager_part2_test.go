package purchase

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestManager_RecoverStrandedApprovals_AWSRedrive_ExecAndPersistBothFail is the
// regression test for CR #728 round-3: when executePurchase returns a non-nil
// error (e.g. partialPurchaseError or a plain provider error) AND
// SavePurchaseExecution then also fails, the old guard (execErr == nil) silently
// dropped the save failure. The row was left stranded in "running" because no
// terminal status was persisted.
//
// After the fix, any SavePurchaseExecution failure is wrapped as ErrAuditLoss
// regardless of whether executePurchase itself returned an error. The test
// verifies:
//   - RecoverStrandedApprovals returns a non-nil error (not silently dropped)
//   - errors.Is(err, config.ErrAuditLoss) is true (sentinel is present)
//   - the original execution error (plan lookup failure) is still reachable via
//     errors.As / errors.Unwrap so callers can inspect the root cause
func TestManager_RecoverStrandedApprovals_AWSRedrive_ExecAndPersistBothFail(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := allowFailedNotification(new(MockEmailSender))

	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	t.Cleanup(func() { mockEmail.AssertExpectations(t) })

	stranded := config.PurchaseExecution{
		ExecutionID: "exec-aws-both-fail",
		PlanID:      "plan-aws-both-fail",
		Status:      "approved",
		Recommendations: []config.RecommendationRecord{
			{CommitmentCurrency: "USD", Provider: "aws", Service: "rds", ResourceType: "db.r5.large", Region: "us-east-1", Count: 1, UpfrontCost: 200.0, Selected: true, Purchased: false},
		},
	}
	runningRow := stranded
	runningRow.Status = "running"

	// planErr is the error returned by executePurchase (simulates a DB blip
	// during plan lookup that makes the purchase fail).
	planErr := errors.New("plan DB read timeout")
	saveErr := errors.New("terminal save DB connection lost")

	mockStore.On("GetStaleApprovedExecutions", ctx, staleApprovedThreshold).
		Return([]config.PurchaseExecution{stranded}, nil)
	// CAS claim succeeds: the row is now "running" with no terminal state yet.
	mockStore.On("TransitionExecutionStatus", ctx, "exec-aws-both-fail", []string{"approved"}, "running", (*string)(nil)).
		Return(&runningRow, nil)
	// executePurchase calls GetPurchasePlan; make it fail so execErr != nil.
	mockStore.On("GetPurchasePlan", mock.Anything, "plan-aws-both-fail").Return(nil, planErr).Once()
	// SavePurchaseExecution also fails: the row remains "running" in the DB.
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).
		Return(saveErr)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	recovered, err := manager.RecoverStrandedApprovals(ctx)

	// The combined failure must surface -- neither the exec error nor the save
	// error may be silently swallowed.
	require.Error(t, err, "both exec and save failure must not be silently dropped")
	assert.ErrorIs(t, err, config.ErrAuditLoss,
		"error must wrap ErrAuditLoss so claimAndRedrive surfaces it to the sweep")

	// The original execution error must remain reachable so operators can
	// diagnose the root cause without reading logs.
	assert.ErrorIs(t, err, planErr,
		"original execution error (plan lookup failure) must be reachable via errors.Is")

	assert.Equal(t, 0, recovered, "a row with both exec and save failures must not be counted as recovered")
}

// TestManager_RecoverStrandedApprovals_AWSClaimLost_NoRedrive verifies that when
// the CAS claim (approved -> running) fails with ErrExecutionNotInExpectedStatus
// the AWS re-drive path does NOT call executeAndFinalize. Only the sweep that
// wins the CAS should ever re-drive; the loser must skip silently. This prevents
// two overlapping sweeps from both calling executeAndFinalize and potentially
// double-purchasing.
func TestManager_RecoverStrandedApprovals_AWSClaimLost_NoRedrive(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockFactory := new(MockProviderFactory)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	t.Cleanup(func() { mockFactory.AssertExpectations(t) })

	stranded := config.PurchaseExecution{
		ExecutionID: "exec-aws-claimed",
		PlanID:      "plan-aws-claimed",
		Status:      "approved",
		Recommendations: []config.RecommendationRecord{
			{CommitmentCurrency: "USD", Provider: "aws", Service: "rds", ResourceType: "db.r5.large", Region: "us-east-1", Count: 1, UpfrontCost: 300.0, Selected: true},
		},
	}

	mockStore.On("GetStaleApprovedExecutions", ctx, staleApprovedThreshold).
		Return([]config.PurchaseExecution{stranded}, nil)
	// A concurrent sweep has already claimed the row (status changed to "running"),
	// so our CAS (approved -> running) is rejected as ErrExecutionNotInExpectedStatus.
	mockStore.On("TransitionExecutionStatus", ctx, "exec-aws-claimed", []string{"approved"}, "running", (*string)(nil)).
		Return(nil, fmt.Errorf("%w: execution exec-aws-claimed cannot transition from \"running\" to \"running\"", config.ErrExecutionNotInExpectedStatus))

	manager := &Manager{
		config:          mockStore,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	recovered, err := manager.RecoverStrandedApprovals(ctx)
	require.NoError(t, err, "losing the CAS claim must not propagate as a sweep error")
	assert.Equal(t, 0, recovered, "a row claimed by a concurrent sweep must not be counted by the loser")

	// The loser must never reach the provider - that would be a double-purchase.
	mockFactory.AssertNotCalled(t, "CreateAndValidateProvider", mock.Anything, mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "SavePurchaseExecution", mock.Anything, mock.Anything)
}

// #1771: with 4-eyes on, an armed auto plan's rec-bearing notified row is not
// auto-executed; it waits for a human approval in the approval queue.
func TestManager_ProcessScheduledPurchases_FourEyesOnSkipsAutoRow(t *testing.T) {
	cfg := autoPurchaseOnCfg()
	cfg.RequireDifferentApprover = true
	plan := &config.PurchasePlan{ID: "plan-gate", AutoPurchase: true, MaxStepCommitment: f64(1000)}
	result := gateHarness(t, plan, dueRecExec("notified", upfrontRec(10)), cfg, nil)
	assert.Equal(t, 0, result.Processed)
	assert.Equal(t, 0, result.Executed)
	assert.Equal(t, 0, result.Failed, "waiting for approval is a skip, not a failure")
}

// With 4-eyes off the gate is unchanged.
func TestExecutableByScheduler_FourEyesOffUnchanged(t *testing.T) {
	ctx := context.Background()
	store := new(MockConfigStore)
	assertMocksOnCleanup(t, store)
	plan := &config.PurchasePlan{ID: "plan-gate", AutoPurchase: true, MaxStepCommitment: f64(1000)}
	store.On("GetPurchasePlan", ctx, "plan-gate").Return(plan, nil)
	store.On("GetGlobalConfig", ctx).Return(autoPurchaseOnCfg(), nil)
	manager := &Manager{config: store}
	exec := dueRecExec("notified", upfrontRec(10))
	ok, err := manager.executableByScheduler(ctx, &exec)
	require.NoError(t, err)
	assert.True(t, ok)
}

// An empty step buys nothing, so 4-eyes does not park it in limbo.
func TestExecutableByScheduler_FourEyesOnEmptyStepStillRoutes(t *testing.T) {
	ctx := context.Background()
	store := new(MockConfigStore)
	assertMocksOnCleanup(t, store)
	cfg := autoPurchaseOnCfg()
	cfg.RequireDifferentApprover = true
	store.On("GetPurchasePlan", ctx, "plan-gate").Return(&config.PurchasePlan{ID: "plan-gate", AutoPurchase: true}, nil)
	store.On("GetGlobalConfig", ctx).Return(cfg, nil)
	manager := &Manager{config: store}
	exec := dueRecExec("notified")
	ok, err := manager.executableByScheduler(ctx, &exec)
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestManager_RecoverStrandedApprovals_SafeFail_ErrNotFoundIsBenign verifies
// that when TransitionExecutionStatus returns config.ErrNotFound (row deleted
// between the stale SELECT and the CAS) the safe-fail path returns (false, nil)
// without calling GetExecutionByID. This avoids a pointless read on a row that
// no longer exists and treats the disappearance as a benign race-loss.
func TestManager_RecoverStrandedApprovals_SafeFail_ErrNotFoundIsBenign(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })

	stranded := config.PurchaseExecution{
		ExecutionID: "exec-vanished",
		Status:      "approved",
		Recommendations: []config.RecommendationRecord{
			// Azure Savings Plans falls through to safe-fail path (no idempotency key).
			{CommitmentCurrency: "USD", Provider: "azure", Service: "savingsplans", ResourceType: "Compute", Region: "eastus", Count: 1, UpfrontCost: 400.0, Selected: true},
		},
	}

	mockStore.On("GetStaleApprovedExecutions", ctx, staleApprovedThreshold).
		Return([]config.PurchaseExecution{stranded}, nil)
	// TransitionExecutionStatus wraps "row vanished" as config.ErrNotFound.
	mockStore.On("TransitionExecutionStatus", ctx, "exec-vanished", []string{"approved"}, "failed", (*string)(nil)).
		Return(nil, fmt.Errorf("%w: execution exec-vanished", config.ErrNotFound))

	manager := &Manager{config: mockStore, dashboardURL: "https://dashboard.example.com"}

	recovered, err := manager.RecoverStrandedApprovals(ctx)
	require.NoError(t, err, "ErrNotFound from CAS must not propagate as a sweep error")
	assert.Equal(t, 0, recovered, "a vanished row contributes nothing to the recovery count")

	// GetExecutionByID must NOT be called: ErrNotFound already tells us the row
	// is gone; the probe would be a redundant round-trip and the early branch
	// avoids it.
	mockStore.AssertNotCalled(t, "GetExecutionByID", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "SavePurchaseExecution", mock.Anything, mock.Anything)
}

// TestManager_RecoverStrandedApprovals_SafeFail_ErrExecutionNotInExpectedStatusIsBenign
// verifies that when TransitionExecutionStatus returns
// config.ErrExecutionNotInExpectedStatus (the row exists but its status has
// already moved out of "approved" -- a concurrent sweep or the original run
// won the CAS) safeFail returns (false, nil) WITHOUT calling GetExecutionByID.
// Re-reading in this case is unnecessary: the sentinel already tells us the row
// is in some non-approved state. Probing can also turn the benign race into a
// hard sweep error if the second read flakes or the row disappears between the
// CAS rejection and the probe. This matches how claimAndRedrive and reaper.go
// already treat ErrExecutionNotInExpectedStatus as terminally benign.
func TestManager_RecoverStrandedApprovals_SafeFail_ErrExecutionNotInExpectedStatusIsBenign(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })

	stranded := config.PurchaseExecution{
		ExecutionID: "exec-already-transitioned",
		Status:      "approved",
		Recommendations: []config.RecommendationRecord{
			// Azure Savings Plans routes to safe-fail (no idempotency key for re-drive).
			{CommitmentCurrency: "USD", Provider: "azure", Service: "savingsplans", ResourceType: "Compute", Region: "eastus", Count: 1, UpfrontCost: 400.0, Selected: true},
		},
	}

	mockStore.On("GetStaleApprovedExecutions", ctx, staleApprovedThreshold).
		Return([]config.PurchaseExecution{stranded}, nil)
	// TransitionExecutionStatus signals the row is in a non-approved state.
	mockStore.On("TransitionExecutionStatus", ctx, "exec-already-transitioned", []string{"approved"}, "failed", (*string)(nil)).
		Return(nil, fmt.Errorf("%w: execution exec-already-transitioned status is completed", config.ErrExecutionNotInExpectedStatus))

	manager := &Manager{config: mockStore, dashboardURL: "https://dashboard.example.com"}

	recovered, err := manager.RecoverStrandedApprovals(ctx)
	require.NoError(t, err, "ErrExecutionNotInExpectedStatus from CAS must not propagate as a sweep error")
	assert.Equal(t, 0, recovered, "a row already out of approved contributes nothing to the recovery count")

	// GetExecutionByID must NOT be called: ErrExecutionNotInExpectedStatus already
	// tells us the row has moved on; the probe is a redundant round-trip and the
	// early branch avoids it (mirroring the ErrNotFound short-circuit above it).
	mockStore.AssertNotCalled(t, "GetExecutionByID", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "SavePurchaseExecution", mock.Anything, mock.Anything)
}

// TestManager_RecoverStrandedApprovals_AWSRedrive_PersistenceFailurePropagates is
// the regression test for the CR #728 round-2 finding: when executeAndFinalize's
// SavePurchaseExecution fails AFTER the CAS-to-running succeeded, claimAndRedrive
// must NOT return (false, nil). The row is now "running" in the DB with no
// terminal state persisted -- silently dropping the error would strand it there
// indefinitely. The sweep must surface the error (ErrAuditLoss) so the caller
// can stop processing and the next tick retries the recovery.
func TestManager_RecoverStrandedApprovals_AWSRedrive_PersistenceFailurePropagates(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockSTS := new(MockSTSClient)
	mockFactory := new(MockProviderFactory)
	mockProvider := new(MockProvider)
	mockServiceClient := new(MockServiceClient)

	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	t.Cleanup(func() { mockEmail.AssertExpectations(t) })
	t.Cleanup(func() { mockSTS.AssertExpectations(t) })
	t.Cleanup(func() { mockFactory.AssertExpectations(t) })
	t.Cleanup(func() { mockProvider.AssertExpectations(t) })
	t.Cleanup(func() { mockServiceClient.AssertExpectations(t) })

	stranded := config.PurchaseExecution{
		ExecutionID: "exec-aws-persist-fail",
		PlanID:      "plan-aws-persist-456",
		Status:      "approved",
		Recommendations: []config.RecommendationRecord{
			{CommitmentCurrency: "USD", Provider: "aws", Service: "rds", ResourceType: "db.r5.large", Region: "us-east-1", Count: 1, UpfrontCost: 200.0, Selected: true, Purchased: false},
		},
	}
	runningRow := stranded
	runningRow.Status = "running"

	plan := &config.PurchasePlan{
		ID:   "plan-aws-persist-456",
		Name: "AWS Persist-Fail Test Plan",
		RampSchedule: config.RampSchedule{
			CurrentStep: 0,
			TotalSteps:  4,
		},
	}

	saveErr := errors.New("DB connection lost")

	mockStore.On("GetStaleApprovedExecutions", ctx, staleApprovedThreshold).
		Return([]config.PurchaseExecution{stranded}, nil)
	// CAS claim: approved -> running. This succeeds -- the row is now "running".
	mockStore.On("TransitionExecutionStatus", ctx, "exec-aws-persist-fail", []string{"approved"}, "running", (*string)(nil)).
		Return(&runningRow, nil)
	mockStore.On("GetPurchasePlan", mock.Anything, "plan-aws-persist-456").Return(plan, nil).Once()
	mockStore.On("SavePurchaseHistory", mock.Anything, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil)
	mockEmail.On("SendPurchaseConfirmation", mock.Anything, mock.AnythingOfType("email.NotificationData")).Return(nil)
	// SavePurchaseExecution fails AFTER the CAS-to-running succeeded.
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).
		Return(saveErr)

	mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("123456789012"),
	}, nil)
	mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(mockProvider, nil)
	mockProvider.On("GetServiceClient", mock.Anything, common.ServiceRDS, "us-east-1").Return(mockServiceClient, nil)
	mockServiceClient.On("PurchaseCommitment", mock.Anything, mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(common.PurchaseResult{
		Success:      true,
		CommitmentID: "ri-persist-fail-12345",
	}, nil)

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		stsClient:       mockSTS,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	recovered, err := manager.RecoverStrandedApprovals(ctx)
	// The persistence failure must surface as an error, not be silently dropped.
	require.Error(t, err, "SavePurchaseExecution failure after CAS-to-running must not be silently dropped")
	assert.ErrorIs(t, err, config.ErrAuditLoss, "error must wrap ErrAuditLoss so callers can classify it")
	assert.Equal(t, 0, recovered, "a row that failed to persist must not be counted as recovered")
}
