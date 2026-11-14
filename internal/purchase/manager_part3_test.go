package purchase

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestManager_RecoverStrandedApprovals_GCPRedrives verifies that a stranded GCP
// compute execution is re-driven via executeAndFinalize rather than failed. The GCP
// compute client uses server-side RequestId + deterministic name from the
// IdempotencyToken (#654), so a re-drive with the same ExecutionID is a safe no-op
// on the GCP side (#639).
func TestManager_RecoverStrandedApprovals_GCPRedrives(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockProvider := new(MockProvider)
	mockServiceClient := new(MockServiceClient)

	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	t.Cleanup(func() { mockEmail.AssertExpectations(t) })
	t.Cleanup(func() { mockFactory.AssertExpectations(t) })
	t.Cleanup(func() { mockProvider.AssertExpectations(t) })
	t.Cleanup(func() { mockServiceClient.AssertExpectations(t) })
	// The GCP recent-commitment check (#682) lists live commitments first;
	// none exist, so the re-drive proceeds to the purchase.
	mockServiceClient.On("GetExistingCommitments", mock.Anything).Return([]common.Commitment(nil), nil)

	stranded := config.PurchaseExecution{
		ExecutionID: "exec-gcp-stranded",
		PlanID:      "plan-gcp",
		Status:      "approved",
		StepNumber:  1,
		Recommendations: []config.RecommendationRecord{
			{CommitmentCurrency: "USD", Provider: "gcp", Service: "compute", ResourceType: "n2-standard-4", Region: "us-central1", Count: 2, UpfrontCost: 150.0, Selected: true, Purchased: false},
		},
	}
	runningRow := stranded
	runningRow.Status = "running"

	plan := &config.PurchasePlan{
		ID:   "plan-gcp",
		Name: "GCP CUD Plan",
		RampSchedule: config.RampSchedule{
			CurrentStep: 0,
			TotalSteps:  2,
		},
	}

	mockStore.On("GetStaleApprovedExecutions", ctx, staleApprovedThreshold).
		Return([]config.PurchaseExecution{stranded}, nil)
	// CAS claim: approved -> running before re-drive.
	mockStore.On("TransitionExecutionStatus", ctx, "exec-gcp-stranded", []string{"approved"}, "running", (*string)(nil)).
		Return(&runningRow, nil)
	mockStore.On("GetPurchasePlan", mock.Anything, "plan-gcp").Return(plan, nil).Once()
	mockStore.On("SavePurchaseHistory", mock.Anything, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil)
	mockEmail.On("SendPurchaseConfirmation", mock.Anything, mock.AnythingOfType("email.NotificationData")).Return(nil)
	var saved *config.PurchaseExecution
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).
		Run(func(args mock.Arguments) { saved = args.Get(1).(*config.PurchaseExecution) }).
		Return(nil)
	mockStore.On("CompletePlanStep", mock.Anything, "plan-gcp", 1).Return(nil)

	mockFactory.On("CreateAndValidateProvider", mock.Anything, "gcp", mock.Anything).Return(mockProvider, nil)
	mockProvider.On("GetServiceClient", mock.Anything, common.ServiceCompute, "us-central1").Return(mockServiceClient, nil)
	mockServiceClient.On("PurchaseCommitment", mock.Anything, mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(common.PurchaseResult{
		Success:      true,
		CommitmentID: "cud-idempotent-gcp",
	}, nil)

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	recovered, err := manager.RecoverStrandedApprovals(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, recovered, "GCP strand must be counted as recovered after re-drive")

	require.NotNil(t, saved)
	assert.Equal(t, "completed", saved.Status, "successfully re-driven GCP execution must be completed, not failed")

	// Provider was reached: re-drive called PurchaseCommitment exactly once.
	mockServiceClient.AssertCalled(t, "PurchaseCommitment", mock.Anything, mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions"))
	// CAS claim (approved -> running) was called; "failed" transition was not.
	mockStore.AssertCalled(t, "TransitionExecutionStatus", ctx, "exec-gcp-stranded", []string{"approved"}, "running", (*string)(nil))
	mockStore.AssertNotCalled(t, "TransitionExecutionStatus", mock.Anything, mock.Anything, mock.Anything, "failed", mock.Anything)
}

// TestManager_RecoverStrandedApprovals_MixedAWSAzureSPSafeFails verifies that a
// stranded execution containing AWS and Azure Savings Plans recommendations falls
// through to the safe-fail path rather than being re-driven. Azure Savings Plans
// uses a timestamp-based alias name with no idempotency key, so a mixed execution
// that includes at least one savings-plans rec must never be auto-re-driven.
func TestManager_RecoverStrandedApprovals_MixedAWSAzureSPSafeFails(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockFactory := new(MockProviderFactory)

	stranded := config.PurchaseExecution{
		ExecutionID: "exec-mixed-sp",
		PlanID:      "plan-mixed-sp",
		Status:      "approved",
		Recommendations: []config.RecommendationRecord{
			{CommitmentCurrency: "USD", Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, UpfrontCost: 100.0, Selected: true},
			// Azure Savings Plans: not safe for re-drive; entire execution falls to safe-fail.
			{CommitmentCurrency: "USD", Provider: "azure", Service: "savingsplans", ResourceType: "Compute", Region: "eastus", Count: 1, UpfrontCost: 100.0, Selected: true},
		},
	}
	failedRow := stranded
	failedRow.Status = "failed"

	mockStore.On("GetStaleApprovedExecutions", ctx, staleApprovedThreshold).
		Return([]config.PurchaseExecution{stranded}, nil)
	mockStore.On("TransitionExecutionStatus", ctx, "exec-mixed-sp", []string{"approved"}, "failed", (*string)(nil)).
		Return(&failedRow, nil)
	mockStore.On("SavePurchaseExecution", ctx, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)

	manager := &Manager{
		config:          mockStore,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	recovered, err := manager.RecoverStrandedApprovals(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, recovered)

	// No provider call: execution with savings-plans rec falls through to safe-fail.
	mockFactory.AssertNotCalled(t, "CreateAndValidateProvider", mock.Anything, mock.Anything, mock.Anything)
	mockStore.AssertExpectations(t)
}
