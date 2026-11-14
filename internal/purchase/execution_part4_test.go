package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestExecutePurchase_SingleAccount_AccountNotFound asserts that when a target
// account ID is known but GetCloudAccount returns (nil, nil) (the account does
// not exist), execution surfaces an error rather than silently falling back to
// ambient credentials and purchasing/stamping against the wrong account (#646).
func TestExecutePurchase_SingleAccount_AccountNotFound(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	acctID := "missing-acct"
	exec := &config.PurchaseExecution{
		ExecutionID: "exec-acct-missing",
		PlanID:      "",
		Recommendations: []config.RecommendationRecord{
			{
				Provider:       "azure",
				Service:        "compute",
				ResourceType:   "Standard_B1ls",
				Region:         "eastus",
				Count:          1,
				Selected:       true,
				CloudAccountID: &acctID,
			},
		},
	}

	// GetCloudAccount returns no error but a nil account (not found).
	mockStore.On("GetCloudAccount", ctx, acctID).Return(nil, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.executePurchase(ctx, exec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential resolution failed for account "+acctID)
	assert.Contains(t, err.Error(), "account not found")

	mockStore.AssertExpectations(t)
}

// TestSingleCloudAccountIDFromRecs covers the helper that derives a shared
// cloud_account_id from the SELECTED recommendations of a plan-less
// execution, and the #1902 ambiguous-scope errors it returns for the
// multi-account and mixed attributed/unattributed shapes.
func TestSingleCloudAccountIDFromRecs(t *testing.T) {
	aid1 := "acct-1"
	aid2 := "acct-2"
	aid3 := "acct-3"

	tests := []struct {
		want    *string
		name    string
		wantMsg string
		recs    []config.RecommendationRecord
		wantErr bool
	}{
		{
			name: "empty slice returns nil",
			recs: []config.RecommendationRecord{},
			want: nil,
		},
		{
			name: "all unattributed returns nil",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: nil},
				{Selected: true, CloudAccountID: nil},
			},
			want: nil,
		},
		{
			name: "single attributed rec returns it",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: &aid1},
			},
			want: &aid1,
		},
		{
			name: "all recs share one account",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: &aid1},
				{Selected: true, CloudAccountID: &aid1},
			},
			want: &aid1,
		},
		{
			name: "unselected rec from another account is ignored",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: &aid1},
				{Selected: false, CloudAccountID: &aid2},
			},
			want: &aid1,
		},
		{
			name: "only unselected recs returns nil",
			recs: []config.RecommendationRecord{
				{Selected: false, CloudAccountID: &aid1},
			},
			want: nil,
		},
		{
			name: "two distinct accounts is an error",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: &aid1},
				{Selected: true, CloudAccountID: &aid2},
			},
			wantErr: true,
			wantMsg: "2 cloud accounts (acct-1, acct-2)",
		},
		{
			name: "mixed attributed and unattributed is an error",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: nil},
				{Selected: true, CloudAccountID: &aid1},
			},
			wantErr: true,
			wantMsg: "carry no cloud_account_id",
		},
		{
			name: "three distinct accounts lists all three in order",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: &aid1},
				{Selected: true, CloudAccountID: &aid2},
				{Selected: true, CloudAccountID: &aid3},
			},
			wantErr: true,
			wantMsg: "(acct-1, acct-2, acct-3)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SingleCloudAccountIDFromRecs(tc.recs)
			if tc.wantErr {
				require.ErrorIs(t, err, errAmbiguousAccountScope)
				assert.Contains(t, err.Error(), tc.wantMsg)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if tc.want == nil {
				assert.Nil(t, got)
			} else {
				require.NotNil(t, got)
				assert.Equal(t, *tc.want, *got)
			}
		})
	}
}

// TestExecutePurchase_AzureCanonicalServiceTypes is the integration-style
// regression guard for issue #626. Before the fix, mapServiceType collapsed
// the canonical service slugs ("compute", "cache", "relational-db") onto
// the AWS-legacy constants (ServiceEC2, ServiceElastiCache, ServiceRDS),
// which Azure and GCP providers' GetServiceClient switches reject because
// they are canonical-only. AWS provider accepts both forms so AWS approves
// kept working and CI stayed green while Azure approves failed in
// production with "unsupported service: ec2" (etc.).
//
// Each sub-case drives executePurchase end-to-end with an Azure rec
// carrying a canonical Service slug and asserts the mock GetServiceClient
// is called with the matching canonical ServiceType constant. testify
// mock.On's argument matcher is exact-equal by default, so a regression
// to the AWS-legacy mapping would surface as an unmatched-expectation
// failure on AssertExpectations rather than a silent green.
func TestExecutePurchase_AzureCanonicalServiceTypes(t *testing.T) {
	cases := []struct {
		name         string
		service      string
		resource     string
		expectedType common.ServiceType
	}{
		{
			name:         "compute_canonical",
			service:      "compute",
			resource:     "Standard_B1ls",
			expectedType: common.ServiceCompute,
		},
		{
			name:     "cache_canonical",
			service:  "cache",
			resource: "Standard_C1",
			// Azure Redis is bought by the managedredis client; the cache
			// client refuses purchases (cloud-commitments-go#694, #1801).
			expectedType: common.ServiceMemoryDB,
		},
		{
			name:         "relational_db_canonical",
			service:      "relational-db",
			resource:     "Standard_D2s_v3",
			expectedType: common.ServiceRelationalDB,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			// Fresh mocks per sub-case so Once()/AssertExpectations
			// counts don't smear between iterations.
			mockStore := new(MockConfigStore)
			mockEmail := new(MockEmailSender)
			mockSTS := new(MockSTSClient)
			mockFactory := new(MockProviderFactory)
			mockProviderInst := new(MockProvider)
			mockServiceClient := new(MockServiceClient)
			assertMocksOnCleanup(t, mockEmail, mockSTS, mockServiceClient)
			mockCredStore := &MockCredentialStore{}

			acctID := "azure-acct-" + tc.name
			exec := &config.PurchaseExecution{
				ExecutionID: "exec-azure-canonical-" + tc.name,
				PlanID:      "",
				StepNumber:  0,
				Source:      common.PurchaseSourceWeb,
				Recommendations: []config.RecommendationRecord{
					{
						Provider:       "azure",
						Service:        tc.service,
						ResourceType:   tc.resource,
						Region:         "eastus",
						Count:          1,
						Savings:        10.0,
						UpfrontCost:    50.0,
						Selected:       true,
						CloudAccountID: &acctID,
					},
				},
			}

			azureAccount := &config.CloudAccount{
				ID:                  acctID,
				Provider:            "azure",
				AzureAuthMode:       "managed_identity",
				AzureSubscriptionID: "sub-111",
			}

			mockStore.On("GetCloudAccount", ctx, acctID).Return(azureAccount, nil)
			mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil)
			mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil)
			mockSTS.On("GetCallerIdentity", ctx, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
				Account: aws.String("123456789012"),
			}, nil)

			mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), "azure",
				mock.MatchedBy(func(cfg *provider.Config) bool {
					return cfg != nil && cfg.ProviderOverride != nil
				}),
			).Return(mockProviderInst, nil)
			// The core assertion: GetServiceClient must be invoked with
			// the canonical ServiceType — not an AWS-legacy constant.
			mockProviderInst.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), tc.expectedType, "eastus").Return(mockServiceClient, nil)
			mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)),
				mock.AnythingOfType("common.Recommendation"),
				mock.AnythingOfType("common.PurchaseOptions"),
			).Return(common.PurchaseResult{Success: true, CommitmentID: "azure-res-" + tc.name}, nil)

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

			mockStore.AssertExpectations(t)
			mockFactory.AssertExpectations(t)
			mockProviderInst.AssertExpectations(t)
		})
	}
}

// TestExecutePurchase_SingleAccount_CredResolutionError asserts that a failure
// to resolve credentials for a single-account path surfaces as an error rather
// than silently falling back to ambient credentials (nil provCfg).
func TestExecutePurchase_SingleAccount_CredResolutionError(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	acctID := "bad-acct"
	exec := &config.PurchaseExecution{
		ExecutionID: "exec-cred-err",
		PlanID:      "",
		Recommendations: []config.RecommendationRecord{
			{
				Provider:       "azure",
				Service:        "compute",
				ResourceType:   "Standard_B1ls",
				Region:         "eastus",
				Count:          1,
				Selected:       true,
				CloudAccountID: &acctID,
			},
		},
	}

	// GetCloudAccount returns a DB error.
	dbErr := errors.New("connection refused")
	mockStore.On("GetCloudAccount", ctx, acctID).Return(nil, dbErr)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.executePurchase(ctx, exec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential resolution failed for account "+acctID)
	assert.Contains(t, err.Error(), "connection refused")

	mockStore.AssertExpectations(t)
}
