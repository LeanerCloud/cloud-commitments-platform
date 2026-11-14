package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Tests for executeSinglePurchase with Engine field (DatabaseDetails branch).
func TestManager_ExecuteSinglePurchase_WithEngine(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockProvider := new(MockProvider)
	mockServiceClient := new(MockServiceClient)
	mockSTS := new(MockSTSClient)
	assertMocksOnCleanup(t, mockStore, mockEmail, mockFactory, mockProvider, mockServiceClient, mockSTS)

	plan := &config.PurchasePlan{
		ID:   "plan-engine",
		Name: "DB Plan",
	}

	exec := &config.PurchaseExecution{
		ExecutionID: "exec-engine",
		PlanID:      "plan-engine",
		Recommendations: []config.RecommendationRecord{
			{
				CommitmentCurrency: "USD",
				Provider:           "aws",
				Service:            "rds",
				ResourceType:       "db.r5.large",
				Engine:             "mysql",
				Region:             "us-east-1",
				Count:              1,
				Savings:            150.0,
				UpfrontCost:        600.0,
				Selected:           true,
			},
		},
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-engine").Return(plan, nil)
	mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil)
	mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil)
	mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(mockProvider, nil)
	mockProvider.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceRDS, "us-east-1").Return(mockServiceClient, nil)
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(
		common.PurchaseResult{Success: true, CommitmentID: "ri-engine-001"}, nil,
	)
	mockSTS.On("GetCallerIdentity", ctx, mock.Anything).Return(nil, errors.New("sts error"))

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		stsClient:       mockSTS,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	err := manager.executePurchase(ctx, exec)
	require.NoError(t, err)
	assert.True(t, exec.Recommendations[0].Purchased)
	assert.Equal(t, "ri-engine-001", exec.Recommendations[0].PurchaseID)
}

// Tests for savePurchaseHistory error path (just logs, doesn't fail).
func TestManager_SavePurchaseHistory_Error(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockProvider := new(MockProvider)
	mockServiceClient := new(MockServiceClient)
	mockSTS := new(MockSTSClient)
	assertMocksOnCleanup(t, mockStore, mockEmail, mockFactory, mockProvider, mockServiceClient, mockSTS)

	plan := &config.PurchasePlan{
		ID:   "plan-hist-err",
		Name: "History Error Plan",
	}

	exec := &config.PurchaseExecution{
		ExecutionID: "exec-hist-err",
		PlanID:      "plan-hist-err",
		Recommendations: []config.RecommendationRecord{
			{
				CommitmentCurrency: "USD",
				Provider:           "aws",
				Service:            "ec2",
				ResourceType:       "c5.large",
				Region:             "us-east-1",
				Count:              2,
				Savings:            80.0,
				UpfrontCost:        320.0,
				Selected:           true,
			},
		},
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-hist-err").Return(plan, nil)
	// SavePurchaseHistory returns error — should be logged but not fail executePurchase
	mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(errors.New("history write error"))
	mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil)
	mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(mockProvider, nil)
	mockProvider.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil)
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(
		common.PurchaseResult{Success: true, CommitmentID: "ri-hist-001"}, nil,
	)
	mockSTS.On("GetCallerIdentity", ctx, mock.Anything).Return(nil, errors.New("sts error"))

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		stsClient:       mockSTS,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	// Should succeed even though history save failed
	err := manager.executePurchase(ctx, exec)
	require.NoError(t, err)
	assert.True(t, exec.Recommendations[0].Purchased)
}

// TestManager_SavePurchaseHistory_RevocationWindow is the regression guard for
// the issue #290 "dead Revoke button" gap: savePurchaseHistory is the real
// write path for completed purchases, and it must stamp
// RevocationWindowClosesAt for Azure (Timestamp + 7d free-cancel window) so the
// History UI's canRevokeCompletedRow check shows the button. AWS and GCP have
// no in-app direct-cancel window in Phase 1, so the field must stay nil and the
// button must stay hidden.
func TestManager_SavePurchaseHistory_RevocationWindow(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		wantWindow   bool // true => RevocationWindowClosesAt non-nil
		wantDaysFrom int  // days after Timestamp when window is expected
	}{
		{name: "azure stamps 7-day window", provider: "azure", wantWindow: true, wantDaysFrom: config.AzureRevocationWindowDays},
		{name: "aws leaves window nil", provider: "aws", wantWindow: false},
		{name: "gcp leaves window nil", provider: "gcp", wantWindow: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mockStore := new(MockConfigStore)
			t.Cleanup(func() { mockStore.AssertExpectations(t) })

			var captured *config.PurchaseHistoryRecord
			mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).
				Run(func(args mock.Arguments) {
					captured = args.Get(1).(*config.PurchaseHistoryRecord)
				}).Return(nil)

			manager := &Manager{config: mockStore}

			plan := &config.PurchasePlan{ID: "plan-rev", Name: "Revocation Window Plan"}
			exec := &config.PurchaseExecution{ExecutionID: "exec-rev", PlanID: "plan-rev"}
			rec := config.RecommendationRecord{
				Provider:     tc.provider,
				Service:      "ec2",
				ResourceType: "c5.large",
				Region:       "us-east-1",
				Count:        1,
			}
			result := common.PurchaseResult{Success: true, CommitmentID: "commit-rev-001"}

			err := manager.savePurchaseHistory(ctx, exec, plan, rec, result, "acct-1", ec2types.OfferingClassTypeConvertible)
			require.NoError(t, err)
			require.NotNil(t, captured)

			if !tc.wantWindow {
				assert.Nil(t, captured.RevocationWindowClosesAt,
					"%s purchases must not stamp a revocation window", tc.provider)
				return
			}

			require.NotNil(t, captured.RevocationWindowClosesAt,
				"azure purchases must stamp a revocation window so the Revoke button shows")
			wantClose := captured.Timestamp.AddDate(0, 0, tc.wantDaysFrom)
			assert.WithinDuration(t, wantClose, *captured.RevocationWindowClosesAt, time.Second,
				"window must be Timestamp + %d days", tc.wantDaysFrom)
		})
	}
}
