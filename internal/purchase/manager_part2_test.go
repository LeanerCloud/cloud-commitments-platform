package purchase

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
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
