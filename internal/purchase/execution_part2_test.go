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

// TestManager_ExecutePurchase_SingleAccount_StampsTargetAccount is the issue
// #646 regression guard: the single-account path must stamp the resolved
// target account's ExternalID on purchase_history, not the ambient AWS host
// account. Covers a managed AWS account (ExternalID is the target account
// number, distinct from the host STS identity) and a direct-execute Azure
// account (must NOT inherit the AWS host account or "unknown"). The fix uses
// account.ExternalID regardless of auth mode, so access_keys exercises the
// same single-account stamping path the assume-role case would.
func TestManager_ExecutePurchase_SingleAccount_StampsTargetAccount(t *testing.T) {
	const hostAccount = "999999999999" // ambient AWS STS identity — must NOT be stamped

	cases := []struct {
		name         string
		provider     string
		externalID   string
		service      string
		expectedType common.ServiceType
		region       string
		resource     string
	}{
		{
			name:         "assume-role AWS stamps target ExternalID not host",
			provider:     "aws",
			externalID:   "111111111111",
			service:      "ec2",
			expectedType: common.ServiceEC2,
			region:       "us-east-1",
			resource:     "m5.large",
		},
		{
			name:         "direct-execute Azure stamps subscription not AWS host",
			provider:     "azure",
			externalID:   "azure-sub-222",
			service:      "compute",
			expectedType: common.ServiceCompute,
			region:       "eastus",
			resource:     "Standard_D2s_v3",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mockStore := new(MockConfigStore)
			mockEmail := new(MockEmailSender)
			mockSTS := new(MockSTSClient)
			mockFactory := new(MockProviderFactory)
			mockProviderInst := new(MockProvider)
			mockServiceClient := new(MockServiceClient)
			assertMocksOnCleanup(t, mockEmail, mockFactory, mockProviderInst, mockServiceClient)
			mockCredStore := &MockCredentialStore{
				LoadRawFn: func(_ context.Context, _, _ string) ([]byte, error) {
					// AWS access_keys path reads stored key JSON; Azure
					// managed_identity never reaches here.
					if tc.provider == "aws" {
						return []byte(`{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`), nil
					}
					return nil, nil
				},
			}

			acctID := "acct-" + tc.name
			exec := &config.PurchaseExecution{
				ExecutionID: "exec-stamp-" + tc.name,
				PlanID:      "",
				Source:      common.PurchaseSourceWeb,
				Recommendations: []config.RecommendationRecord{
					{
						Provider:       tc.provider,
						Service:        tc.service,
						ResourceType:   tc.resource,
						Region:         tc.region,
						Count:          1,
						UpfrontCost:    100.0,
						Savings:        20.0,
						Selected:       true,
						CloudAccountID: &acctID,
					},
				},
			}

			account := &config.CloudAccount{
				ID:         acctID,
				Provider:   tc.provider,
				ExternalID: tc.externalID,
			}
			switch tc.provider {
			case "aws":
				account.AWSAuthMode = "access_keys"
			case "azure":
				account.AzureAuthMode = "managed_identity"
				account.AzureSubscriptionID = "sub-internal"
			}

			mockStore.On("GetCloudAccount", ctx, acctID).Return(account, nil)

			// Capture the stamped AccountID on the history row.
			var stampedAccount string
			mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).
				Run(func(args mock.Arguments) {
					stampedAccount = args.Get(1).(*config.PurchaseHistoryRecord).AccountID
				}).Return(nil).Once()
			mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil)
			// Ambient STS — if the host account leaks onto history this is where
			// it would come from; it must NOT be the stamped value.
			mockSTS.On("GetCallerIdentity", ctx, mock.AnythingOfType("*sts.GetCallerIdentityInput")).
				Return(&sts.GetCallerIdentityOutput{Account: aws.String(hostAccount)}, nil).Maybe()

			mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), tc.provider, mock.Anything).Return(mockProviderInst, nil)
			mockProviderInst.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), tc.expectedType, tc.region).Return(mockServiceClient, nil)
			mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)),
				mock.AnythingOfType("common.Recommendation"),
				mock.AnythingOfType("common.PurchaseOptions"),
			).Return(common.PurchaseResult{Success: true, CommitmentID: "commit-1"}, nil)

			manager := &Manager{
				config:          mockStore,
				email:           mockEmail,
				stsClient:       mockSTS,
				providerFactory: mockFactory,
				credStore:       mockCredStore,
				dashboardURL:    "https://dashboard.example.com",
			}

			err := manager.executePurchase(ctx, exec)
			require.NoError(t, err)

			assert.Equal(t, tc.externalID, stampedAccount,
				"history must be stamped with the target account ExternalID (#646)")
			assert.NotEqual(t, hostAccount, stampedAccount,
				"history must NOT be stamped with the ambient AWS host account")
			assert.NotEqual(t, "unknown", stampedAccount)

			mockStore.AssertExpectations(t)
		})
	}
}

// TestProcessPurchaseRecommendations_GlobalConfigError_FailsInsteadOfDefaulting is the
// issue #694 regression guard for HOLE 1: when GetGlobalConfig returns an error,
// processPurchaseRecommendations must return an error and must NOT proceed with
// an empty OfferingClass that silently defaults to "convertible".
//
// The pre-fix code logged the error and continued, so a transient DB failure
// would cause a standard-configured RI to be purchased as convertible (more
// expensive, and irreversible). This test asserts the function returns the DB
// error and PurchaseCommitment is never called.
func TestProcessPurchaseRecommendations_GlobalConfigError_FailsInsteadOfDefaulting(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockFactory := new(MockProviderFactory)

	dbErr := errors.New("connection refused: GlobalConfig read failed")
	// Register the GetGlobalConfig expectation so the mock routes to testify
	// (rather than the no-op default that returns an empty config).
	mockStore.On("GetGlobalConfig", ctx).Return(nil, dbErr)

	exec := &config.PurchaseExecution{
		ExecutionID: "exec-globalcfg-err",
		PlanID:      "",
		Recommendations: []config.RecommendationRecord{
			{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, UpfrontCost: 300, Savings: 50, Selected: true},
		},
	}
	plan := &config.PurchasePlan{Name: "Direct purchase"}

	manager := &Manager{
		config:          mockStore,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	_, _, purchaseErrors, _, procErr := manager.processPurchaseRecommendations(ctx, exec, plan, "111111111111", nil, "")

	// The function must return an error derived from the DB failure.
	require.Error(t, procErr, "GlobalConfig load failure must abort processing, not silently default to convertible")
	assert.Contains(t, procErr.Error(), "GlobalConfig", "error must mention GlobalConfig so operators can diagnose the DB issue")

	// purchaseErrors must be nil: no rec-level errors should be emitted
	// because the function must abort before reaching the fan-out.
	assert.Nil(t, purchaseErrors, "no rec-level errors expected — function must abort before the fan-out")

	// PurchaseCommitment must NEVER be called: assert the factory was never
	// invoked, which is the clearest proxy for "no cloud API call happened".
	mockFactory.AssertNotCalled(t, "CreateAndValidateProvider", mock.Anything, mock.Anything, mock.Anything)

	mockStore.AssertExpectations(t)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
}
