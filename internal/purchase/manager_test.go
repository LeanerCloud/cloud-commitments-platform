package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestNewManager(t *testing.T) {
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	cfg := ManagerConfig{
		ConfigStore:            mockStore,
		EmailSender:            mockEmail,
		NotificationDaysBefore: 7,
		DashboardURL:           "https://dashboard.example.com",
	}

	manager := NewManager(cfg)

	assert.NotNil(t, manager)
	assert.Equal(t, 7, manager.notifyDays)
	assert.Equal(t, "https://dashboard.example.com", manager.dashboardURL)
}

func TestManager_ProcessScheduledPurchases_NoExecutions(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	mockStore.On("GetStaleApprovedExecutions", ctx, mock.Anything).Return([]config.PurchaseExecution{}, nil)
	mockStore.On("GetPendingExecutions", ctx).Return([]config.PurchaseExecution{}, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.ProcessScheduledPurchases(ctx)
	require.NoError(t, err)

	assert.Equal(t, 0, result.Processed)
	assert.Equal(t, 0, result.Executed)

	mockStore.AssertExpectations(t)
}

func TestManager_ProcessScheduledPurchases_FutureExecution(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	futureDate := time.Now().Add(24 * time.Hour)
	executions := []config.PurchaseExecution{
		{
			ExecutionID:   "exec-123",
			PlanID:        "plan-456",
			Status:        "pending",
			ScheduledDate: futureDate,
		},
	}

	mockStore.On("GetStaleApprovedExecutions", ctx, mock.Anything).Return([]config.PurchaseExecution{}, nil)
	mockStore.On("GetPendingExecutions", ctx).Return(executions, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.ProcessScheduledPurchases(ctx)
	require.NoError(t, err)

	assert.Equal(t, 0, result.Processed)
	assert.Equal(t, 0, result.Executed)

	mockStore.AssertExpectations(t)
}

func TestManager_ProcessScheduledPurchases_CompletedExecution(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	pastDate := time.Now().Add(-1 * time.Hour)
	executions := []config.PurchaseExecution{
		{
			ExecutionID:   "exec-123",
			PlanID:        "plan-456",
			Status:        "completed",
			ScheduledDate: pastDate,
		},
	}

	mockStore.On("GetStaleApprovedExecutions", ctx, mock.Anything).Return([]config.PurchaseExecution{}, nil)
	mockStore.On("GetPendingExecutions", ctx).Return(executions, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.ProcessScheduledPurchases(ctx)
	require.NoError(t, err)

	// Completed executions are skipped without being re-executed; processed counter
	// reflects only actually-attempted executions, not skipped ones.
	assert.Equal(t, 0, result.Processed)
	assert.Equal(t, 0, result.Executed)

	mockStore.AssertExpectations(t)
}

func TestManager_ProcessScheduledPurchases_Error(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	mockStore.On("GetStaleApprovedExecutions", ctx, mock.Anything).Return([]config.PurchaseExecution{}, nil)
	mockStore.On("GetPendingExecutions", ctx).Return(nil, errors.New("database error"))

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.ProcessScheduledPurchases(ctx)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to get pending executions")

	mockStore.AssertExpectations(t)
}

func TestManager_ProcessScheduledPurchases_DuePurchase(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockSTS := new(MockSTSClient)

	pastDate := time.Now().Add(-1 * time.Hour)
	executions := []config.PurchaseExecution{
		{
			ExecutionID:   "exec-123",
			PlanID:        "plan-456",
			Status:        "notified",
			StepNumber:    1,
			ScheduledDate: pastDate,
			Recommendations: []config.RecommendationRecord{
				{
					CommitmentCurrency: "USD",
					Service:            "ec2",
					ResourceType:       "m5.large",
					Region:             "us-east-1",
					Count:              1,
					Savings:            50.0,
					UpfrontCost:        200.0,
					Selected:           true,
				},
			},
		},
	}

	plan := &config.PurchasePlan{
		ID:                "plan-456",
		Name:              "Test Plan",
		AutoPurchase:      true, // required by executableByScheduler gate (F1 fix)
		MaxStepCommitment: capOf(1e9),
		RampSchedule: config.RampSchedule{
			CurrentStep: 0,
			TotalSteps:  4,
		},
	}

	// claimAndExecuteWithin CASes the due row to "running" before executing (issue #1013).
	claimedExec := executions[0]
	claimedExec.Status = "running"
	mockStore.On("GetStaleApprovedExecutions", ctx, mock.Anything).Return([]config.PurchaseExecution{}, nil)
	mockStore.On("GetPendingExecutions", ctx).Return(executions, nil)
	mockStore.On("GetGlobalConfig", ctx).Return(autoPurchaseOnCfg(), nil).Once()
	mockStore.On("GetGlobalConfig", mock.Anything).Return(autoPurchaseOnCfg(), nil).Maybe()
	// executableByScheduler calls GetPurchasePlan first (AutoPurchase gate), then
	// executePurchase calls it again to build purchase options — two calls total.
	mockStore.On("GetPurchasePlan", mock.Anything, "plan-456").Return(plan, nil)
	mockStore.On("TransitionExecutionStatus", ctx, "exec-123",
		[]string{"pending", "notified"}, "running", (*string)(nil)).Return(&claimedExec, nil)
	mockStore.On("SavePurchaseHistory", mock.Anything, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil)
	mockEmail.On("SendPurchaseConfirmation", mock.Anything, mock.AnythingOfType("email.NotificationData")).Return(nil)
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)
	mockStore.On("CompletePlanStep", mock.Anything, "plan-456", 1).Return(nil)
	mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("123456789012"),
	}, nil)

	// Set up mock provider factory
	mockFactory := new(MockProviderFactory)
	mockProvider := new(MockProvider)
	mockServiceClient := new(MockServiceClient)
	assertMocksOnCleanup(t, mockFactory, mockProvider, mockServiceClient)

	mockFactory.On("CreateAndValidateProvider", mock.Anything, "", mock.Anything).Return(mockProvider, nil)
	mockProvider.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil)
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(common.PurchaseResult{
		Success:      true,
		CommitmentID: "ri-12345",
	}, nil)

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		stsClient:       mockSTS,
		dashboardURL:    "https://dashboard.example.com",
		providerFactory: mockFactory,
	}

	result, err := manager.ProcessScheduledPurchases(ctx)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Processed)
	assert.Equal(t, 1, result.Executed)

	mockStore.AssertExpectations(t)
	mockEmail.AssertExpectations(t)
	mockSTS.AssertExpectations(t)
}

func TestManager_ProcessScheduledPurchases_CancelledExecution(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	pastDate := time.Now().Add(-1 * time.Hour)
	executions := []config.PurchaseExecution{
		{
			ExecutionID:   "exec-123",
			PlanID:        "plan-456",
			Status:        "canceled",
			ScheduledDate: pastDate,
		},
	}

	mockStore.On("GetStaleApprovedExecutions", ctx, mock.Anything).Return([]config.PurchaseExecution{}, nil)
	mockStore.On("GetPendingExecutions", ctx).Return(executions, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.ProcessScheduledPurchases(ctx)
	require.NoError(t, err)

	// Canceled executions are skipped without being re-executed; processed counter
	// reflects only actually-attempted executions, not skipped ones.
	assert.Equal(t, 0, result.Processed)
	assert.Equal(t, 0, result.Executed)

	mockStore.AssertExpectations(t)
}

func TestManager_ProcessScheduledPurchases_ExecutionFails(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := allowFailedNotification(new(MockEmailSender))

	pastDate := time.Now().Add(-1 * time.Hour)
	executions := []config.PurchaseExecution{
		{
			ExecutionID:   "exec-123",
			PlanID:        "plan-456",
			Status:        "pending",
			ScheduledDate: pastDate,
		},
	}

	autoPurchasePlan := &config.PurchasePlan{
		ID:           "plan-456",
		AutoPurchase: true,
	}
	claimedExec := executions[0]
	claimedExec.Status = "running"
	mockStore.On("GetStaleApprovedExecutions", ctx, mock.Anything).Return([]config.PurchaseExecution{}, nil)
	mockStore.On("GetPendingExecutions", ctx).Return(executions, nil)
	mockStore.On("GetGlobalConfig", ctx).Return(autoPurchaseOnCfg(), nil).Once()
	mockStore.On("GetGlobalConfig", mock.Anything).Return(autoPurchaseOnCfg(), nil).Maybe()
	// First call: executableByScheduler gate check (AutoPurchase=true -> proceed).
	// Second call: executePurchase fetches the plan config and returns error to
	// simulate a purchase failure — this is what drives the row to "failed".
	mockStore.On("GetPurchasePlan", mock.Anything, "plan-456").Return(autoPurchasePlan, nil).Once()
	mockStore.On("GetPurchasePlan", mock.Anything, "plan-456").Return(nil, errors.New("plan not found")).Once()
	mockStore.On("TransitionExecutionStatus", ctx, "exec-123",
		[]string{"pending", "notified"}, "running", (*string)(nil)).Return(&claimedExec, nil)
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)
	// updatePlanProgress is NOT called when execution fails

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.ProcessScheduledPurchases(ctx)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Processed)
	assert.Equal(t, 0, result.Executed)

	mockStore.AssertExpectations(t)
}

// ─── F1 regression: AutoPurchase gate (adversarial review follow-up) ─────────

// TestManager_ProcessScheduledPurchases_PendingAutoFalseSkipped is the
// regression test for F1: a pending execution whose owning plan has
// AutoPurchase=false must NOT be executed by the cron sweep. Before the fix
// the sweep executed all pending/notified rows regardless of the plan setting.
func TestManager_ProcessScheduledPurchases_PendingAutoFalseSkipped(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	pastDate := time.Now().Add(-1 * time.Hour)
	executions := []config.PurchaseExecution{
		{
			ExecutionID:   "exec-noauto",
			PlanID:        "plan-noauto",
			Status:        "pending",
			ScheduledDate: pastDate,
		},
	}
	plan := &config.PurchasePlan{
		ID:           "plan-noauto",
		AutoPurchase: false, // gate must block execution
	}

	mockStore.On("GetStaleApprovedExecutions", ctx, mock.Anything).Return([]config.PurchaseExecution{}, nil)
	mockStore.On("GetPendingExecutions", ctx).Return(executions, nil)
	mockStore.On("GetPurchasePlan", ctx, "plan-noauto").Return(plan, nil)
	// TransitionExecutionStatus must NOT be called — the row must be skipped.

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.ProcessScheduledPurchases(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Processed, "AutoPurchase=false rows must not be processed")
	assert.Equal(t, 0, result.Executed)
	assert.Equal(t, 0, result.Failed)

	mockStore.AssertExpectations(t)
	mockStore.AssertNotCalled(t, "TransitionExecutionStatus",
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestManager_ProcessScheduledPurchases_WebSourceSkipped verifies that a
// pending execution with the canonical web source (common.PurchaseSourceWeb,
// "cudly-web") is never auto-executed by the cron sweep, even when the plan
// has AutoPurchase=true. Web-submitted rows must wait for the token-link
// approval path. Uses the real persisted value, not a bare "web" literal, so
// the test reproduces the production data shape (regression guard for the
// stringly-typed gate that let "cudly-web" rows through).
func TestManager_ProcessScheduledPurchases_WebSourceSkipped(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	pastDate := time.Now().Add(-1 * time.Hour)
	executions := []config.PurchaseExecution{
		{
			ExecutionID:   "exec-web",
			PlanID:        "plan-web",
			Status:        "pending",
			Source:        common.PurchaseSourceWeb,
			ScheduledDate: pastDate,
		},
	}

	mockStore.On("GetStaleApprovedExecutions", ctx, mock.Anything).Return([]config.PurchaseExecution{}, nil)
	mockStore.On("GetPendingExecutions", ctx).Return(executions, nil)

	// Sentinel: web rows must short-circuit in executableByScheduler BEFORE the
	// AutoPurchase plan fetch. GetPurchasePlanFn returns AutoPurchase=true (the
	// dangerous case: if the gate is bypassed the row would execute), and flips
	// planFetched. The mock's default GetPurchasePlan stub does not record the
	// call, so AssertNotCalled alone cannot detect the bypass — hence this flag.
	planFetched := false
	mockStore.GetPurchasePlanFn = func(_ context.Context, planID string) (*config.PurchasePlan, error) {
		planFetched = true
		return &config.PurchasePlan{ID: planID, AutoPurchase: true}, nil
	}

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.ProcessScheduledPurchases(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Processed, "web-sourced rows must not be auto-executed")
	assert.Equal(t, 0, result.Executed)
	assert.Equal(t, 0, result.Failed)
	assert.False(t, planFetched,
		"web rows must short-circuit before the AutoPurchase plan fetch; a bare \"web\" "+
			"literal gate lets the persisted \"cudly-web\" value through to execution")

	mockStore.AssertExpectations(t)
	mockStore.AssertNotCalled(t, "TransitionExecutionStatus",
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}
