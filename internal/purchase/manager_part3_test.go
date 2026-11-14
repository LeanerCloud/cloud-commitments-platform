package purchase

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
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

// TestManager_RecoverStrandedApprovals_AWSOnlyRedrives is the regression test for
// issue #632 Option 5: a stranded AWS-only execution with a durable ExecutionID is
// re-driven via executeAndFinalize rather than failed. All AWS executors honor
// opts.IdempotencyToken via DeriveIdempotencyToken(exec.ExecutionID, i), so the
// second call is a safe no-op on the AWS side and the row transitions directly to
// "completed" without requiring a manual Retry.
//
// The CAS claim (approved -> running) is expected before the re-drive call; only
// the winner of this CAS proceeds to executeAndFinalize, preventing concurrent
// sweeps from double-purchasing.
func TestManager_RecoverStrandedApprovals_AWSOnlyRedrives(t *testing.T) {
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
		ExecutionID: "exec-aws-stranded",
		PlanID:      "plan-aws-456",
		Status:      "approved",
		StepNumber:  1,
		Recommendations: []config.RecommendationRecord{
			{CommitmentCurrency: "USD", Provider: "aws", Service: "rds", ResourceType: "db.r5.large", Region: "us-east-1", Count: 1, UpfrontCost: 200.0, Selected: true, Purchased: false},
		},
	}
	runningRow := stranded
	runningRow.Status = "running"

	plan := &config.PurchasePlan{
		ID:   "plan-aws-456",
		Name: "AWS Test Plan",
		RampSchedule: config.RampSchedule{
			CurrentStep: 0,
			TotalSteps:  4,
		},
	}

	mockStore.On("GetStaleApprovedExecutions", ctx, staleApprovedThreshold).
		Return([]config.PurchaseExecution{stranded}, nil)
	// CAS claim: approved -> running. The re-drive proceeds only after winning this.
	mockStore.On("TransitionExecutionStatus", ctx, "exec-aws-stranded", []string{"approved"}, "running", (*string)(nil)).
		Return(&runningRow, nil)
	mockStore.On("GetPurchasePlan", mock.Anything, "plan-aws-456").Return(plan, nil).Once()
	mockStore.On("SavePurchaseHistory", mock.Anything, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil)
	mockEmail.On("SendPurchaseConfirmation", mock.Anything, mock.AnythingOfType("email.NotificationData")).Return(nil)
	var saved *config.PurchaseExecution
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).
		Run(func(args mock.Arguments) { saved = args.Get(1).(*config.PurchaseExecution) }).
		Return(nil)
	mockStore.On("CompletePlanStep", mock.Anything, "plan-aws-456", 1).Return(nil)
	mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("123456789012"),
	}, nil)

	// executeSinglePurchase wraps ctx in a per-rec WithTimeout before calling
	// CreateAndValidateProvider, so we must match any context, not ctx itself.
	mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(mockProvider, nil)
	mockProvider.On("GetServiceClient", mock.Anything, common.ServiceRDS, "us-east-1").Return(mockServiceClient, nil)
	mockServiceClient.On("PurchaseCommitment", mock.Anything, mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(common.PurchaseResult{
		Success:      true,
		CommitmentID: "ri-idempotent-12345",
	}, nil)

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		stsClient:       mockSTS,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	recovered, err := manager.RecoverStrandedApprovals(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, recovered, "AWS-only strand must be counted as recovered after re-drive")

	require.NotNil(t, saved)
	assert.Equal(t, "completed", saved.Status, "successfully re-driven AWS execution must be completed, not failed")

	// The provider was reached: the re-drive called PurchaseCommitment exactly once.
	mockServiceClient.AssertCalled(t, "PurchaseCommitment", mock.Anything, mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions"))
	// The CAS claim (approved -> running) was called; "failed" transition was not.
	mockStore.AssertCalled(t, "TransitionExecutionStatus", ctx, "exec-aws-stranded", []string{"approved"}, "running", (*string)(nil))
	mockStore.AssertNotCalled(t, "TransitionExecutionStatus", mock.Anything, mock.Anything, mock.Anything, "failed", mock.Anything)
}

// TestManager_RecoverStrandedApprovals_AzureReservationRedrives verifies that a
// stranded Azure reservation execution (compute, database, cache, etc.) is
// re-driven via executeAndFinalize rather than failed. All Azure reservation
// service clients call DoIdempotentPurchaseTwoStep with opts.IdempotencyToken
// (PR #729 / issue #721), so a re-drive with the same ExecutionID is a safe
// no-op on the Azure side (#639).
func TestManager_RecoverStrandedApprovals_AzureReservationRedrives(t *testing.T) {
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

	stranded := config.PurchaseExecution{
		ExecutionID: "exec-azure-res-stranded",
		PlanID:      "plan-azure-res",
		Status:      "approved",
		StepNumber:  1,
		Recommendations: []config.RecommendationRecord{
			{CommitmentCurrency: "USD", ID: "current-azure", Provider: "azure", Service: "compute", ResourceType: "Standard_D4s_v3", Region: "eastus", Count: 1, UpfrontCost: 300.0, Selected: true, Purchased: false},
		},
	}
	mockStore.On("ListStoredRecommendations", mock.Anything, config.RecommendationFilter{Provider: "azure", RequireAzurePricingMigration: true}).Return(stranded.Recommendations, nil).Once()
	runningRow := stranded
	runningRow.Status = "running"

	plan := &config.PurchasePlan{
		ID:   "plan-azure-res",
		Name: "Azure Reservation Plan",
		RampSchedule: config.RampSchedule{
			CurrentStep: 0,
			TotalSteps:  2,
		},
	}

	mockStore.On("GetStaleApprovedExecutions", ctx, staleApprovedThreshold).
		Return([]config.PurchaseExecution{stranded}, nil)
	// CAS claim: approved -> running before re-drive.
	mockStore.On("TransitionExecutionStatus", ctx, "exec-azure-res-stranded", []string{"approved"}, "running", (*string)(nil)).
		Return(&runningRow, nil)
	mockStore.On("GetPurchasePlan", mock.Anything, "plan-azure-res").Return(plan, nil).Once()
	mockStore.On("SavePurchaseHistory", mock.Anything, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil)
	mockEmail.On("SendPurchaseConfirmation", mock.Anything, mock.AnythingOfType("email.NotificationData")).Return(nil)
	var saved *config.PurchaseExecution
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).
		Run(func(args mock.Arguments) { saved = args.Get(1).(*config.PurchaseExecution) }).
		Return(nil)
	mockStore.On("CompletePlanStep", mock.Anything, "plan-azure-res", 1).Return(nil)

	mockFactory.On("CreateAndValidateProvider", mock.Anything, "azure", mock.Anything).Return(mockProvider, nil)
	mockProvider.On("GetServiceClient", mock.Anything, common.ServiceCompute, "eastus").Return(mockServiceClient, nil)
	mockServiceClient.On("PurchaseCommitment", mock.Anything, mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(common.PurchaseResult{
		Success:      true,
		CommitmentID: "azure-res-idempotent-order-id",
	}, nil)

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	recovered, err := manager.RecoverStrandedApprovals(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, recovered, "Azure reservation strand must be counted as recovered after re-drive")

	require.NotNil(t, saved)
	assert.Equal(t, "completed", saved.Status, "successfully re-driven Azure reservation must be completed, not failed")

	// Provider was reached: re-drive called PurchaseCommitment exactly once.
	mockServiceClient.AssertCalled(t, "PurchaseCommitment", mock.Anything, mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions"))
	// CAS claim (approved -> running) was called; "failed" transition was not.
	mockStore.AssertCalled(t, "TransitionExecutionStatus", ctx, "exec-azure-res-stranded", []string{"approved"}, "running", (*string)(nil))
	mockStore.AssertNotCalled(t, "TransitionExecutionStatus", mock.Anything, mock.Anything, mock.Anything, "failed", mock.Anything)
}
