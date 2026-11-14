package purchase

import (
	"context"
	"errors"
	"fmt"
	"sync"
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

// TestExecuteForAccount_CredentialFailure_MarksFailed locks down the invariant
// that a credential-resolution error is a hard failure: the per-account
// execution record must be saved with Status="failed", the provider factory
// must NEVER be invoked (no ambient fallback), and executePurchase must
// surface the error. The production bug this guards was fixed in 9531681a4
// across AWS/Azure/GCP; this test ensures a future refactor cannot silently
// reintroduce the ambient fallback.
func TestExecuteForAccount_CredentialFailure_MarksFailed(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		account  config.CloudAccount
	}{
		{
			name:     "aws access_keys credentials missing",
			provider: "aws",
			account: config.CloudAccount{
				ID: "aaaaaaaa-0000-0000-0000-000000000001", Name: "aws-failing",
				Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys",
			},
		},
		{
			name:     "azure client_secret credentials missing",
			provider: "azure",
			account: config.CloudAccount{
				ID: "bbbbbbbb-0000-0000-0000-000000000002", Name: "azure-failing",
				Provider: "azure", ExternalID: "sub-222", AzureAuthMode: "client_secret",
				AzureTenantID: "tenant-222", AzureClientID: "client-222",
			},
		},
		{
			name:     "gcp service_account credentials missing",
			provider: "gcp",
			account: config.CloudAccount{
				ID: "cccccccc-0000-0000-0000-000000000003", Name: "gcp-failing",
				Provider: "gcp", ExternalID: "proj-333", GCPAuthMode: "service_account_key",
				GCPProjectID: "proj-333",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mockStore := new(MockConfigStore)
			assertMocksOnCleanup(t, mockStore)
			mockEmail := allowFailedNotification(new(MockEmailSender))
			mockFactory := new(MockProviderFactory)
			// Credential store returns nothing for every load — this triggers
			// the "no credentials stored" error path in the resolver for all
			// three providers.
			credStore := &MockCredentialStore{
				LoadRawFn: func(_ context.Context, _, _ string) ([]byte, error) {
					return nil, nil
				},
			}

			plan := &config.PurchasePlan{
				ID:           "plan-credfail",
				Name:         "Credential Failure Plan",
				RampSchedule: config.RampSchedule{CurrentStep: 0, TotalSteps: 1},
			}
			rec := config.RecommendationRecord{
				Provider: tt.provider, Service: "ec2", ResourceType: "m5.large",
				Region: "us-east-1", Count: 1, Savings: 75.0, UpfrontCost: 300.0, Selected: true,
			}
			exec := &config.PurchaseExecution{
				ExecutionID:     "exec-credfail",
				PlanID:          "plan-credfail",
				Status:          "pending",
				Recommendations: []config.RecommendationRecord{withAccount(rec, tt.account.ID)},
			}

			mockStore.On("GetPurchasePlan", ctx, "plan-credfail").Return(plan, nil)
			mockStore.GetPlanAccountsFn = func(_ context.Context, _ string) ([]config.CloudAccount, error) {
				return []config.CloudAccount{tt.account}, nil
			}
			var saved []*config.PurchaseExecution
			var mu sync.Mutex
			mockStore.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
				mu.Lock()
				saved = append(saved, e)
				mu.Unlock()
				return nil
			}

			manager := &Manager{
				config:          mockStore,
				email:           mockEmail,
				providerFactory: mockFactory,
				credStore:       credStore,
			}

			err := manager.executePurchase(ctx, exec)

			// Must surface the credential failure — no ambient fallback.
			require.Error(t, err)
			assert.Contains(t, err.Error(), "credential resolution failed")

			// Per-account execution record must be persisted with Status=failed
			// so the audit trail reflects the failure.
			require.NotEmpty(t, saved, "SavePurchaseExecution should be called for the failed account")
			assert.Equal(t, "failed", saved[0].Status)
			assert.NotEmpty(t, saved[0].Error, "failed execution record must carry the error message")

			// The provider factory must NEVER be invoked when credentials
			// fail to resolve. If this fires, something is attempting an
			// ambient-credential fallback — exactly the bug 9531681a4 closed.
			mockFactory.AssertNotCalled(t, "CreateAndValidateProvider", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// TestExecuteAndFinalize_PassedStepIsStampedOnTheRow is the positive control for
// the two above: the discrimination has to keep recording the case issue #1861
// actually asks for. A row completing a step the ramp moved PAST bought
// commitment the plan counted earlier and will never count again, and that is
// invisible everywhere else once the Lambda log ages out.
func TestExecuteAndFinalize_PassedStepIsStampedOnTheRow(t *testing.T) {
	final := runRampAdvanceOutcome(t, "exec-passed-step",
		fmt.Errorf("%w: plan plan-multi is on ramp step 5, past the completing step 3", config.ErrRampStepAlreadyCounted))

	assert.Equal(t, "completed", final.Status,
		"the purchase completed; only the plan's progress accounting did not")
	assert.Contains(t, final.Error, "ramp not advanced",
		"a step the ramp has passed must survive on the row, not only in the log")
	assert.Contains(t, final.Error, "past the completing step 3")
}

func TestManager_GetAWSAccountID_Success(t *testing.T) {
	ctx := context.Background()
	mockSTS := new(MockSTSClient)

	mockSTS.On("GetCallerIdentity", ctx, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("987654321098"),
	}, nil)

	manager := &Manager{
		stsClient: mockSTS,
	}

	accountID, err := manager.getAWSAccountID(ctx)
	require.NoError(t, err)
	assert.Equal(t, "987654321098", accountID)

	mockSTS.AssertExpectations(t)
}

func TestManager_GetAWSAccountID_NoClient(t *testing.T) {
	ctx := context.Background()

	manager := &Manager{
		stsClient: nil, // No STS client configured
	}

	_, err := manager.getAWSAccountID(ctx)
	require.Error(t, err, "nil STS client must return an error (not the 'unknown' sentinel)")
}

func TestManager_GetAWSAccountID_Error(t *testing.T) {
	ctx := context.Background()
	mockSTS := new(MockSTSClient)

	mockSTS.On("GetCallerIdentity", ctx, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(nil, errors.New("STS error"))

	manager := &Manager{
		stsClient: mockSTS,
	}

	_, err := manager.getAWSAccountID(ctx)
	require.Error(t, err, "STS failure must return an error")

	mockSTS.AssertExpectations(t)
}

func TestManager_GetAWSAccountID_NilAccount(t *testing.T) {
	ctx := context.Background()
	mockSTS := new(MockSTSClient)

	mockSTS.On("GetCallerIdentity", ctx, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
		Account: nil, // Nil account in response
	}, nil)

	manager := &Manager{
		stsClient: mockSTS,
	}

	_, err := manager.getAWSAccountID(ctx)
	require.Error(t, err, "nil Account in STS response must return an error")

	mockSTS.AssertExpectations(t)
}

func TestManager_ExecutePurchase_MultiAccount(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockProvider := new(MockProvider)
	mockServiceClient := new(MockServiceClient)
	assertMocksOnCleanup(t, mockProvider, mockServiceClient)
	credStore := &MockCredentialStore{
		LoadRawFn: func(_ context.Context, _, _ string) ([]byte, error) {
			return []byte(`{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`), nil
		},
	}

	plan := &config.PurchasePlan{
		ID:           "plan-multi",
		Name:         "Multi-Account Plan",
		RampSchedule: config.RampSchedule{CurrentStep: 0, TotalSteps: 1},
	}

	rec := config.RecommendationRecord{
		Provider:     "aws",
		Service:      "ec2",
		ResourceType: "m5.large",
		Region:       "us-east-1",
		Count:        1,
		Savings:      75.0,
		UpfrontCost:  300.0,
		Selected:     true,
	}

	exec := &config.PurchaseExecution{
		ExecutionID:     "exec-multi",
		PlanID:          "plan-multi",
		Status:          "pending",
		Recommendations: []config.RecommendationRecord{withAccount(rec, "aaaaaaaa-0000-0000-0000-000000000001"), withAccount(rec, "aaaaaaaa-0000-0000-0000-000000000002")},
	}

	accounts := []config.CloudAccount{
		{ID: "aaaaaaaa-0000-0000-0000-000000000001", Name: "Prod", Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys"},
		{ID: "aaaaaaaa-0000-0000-0000-000000000002", Name: "Staging", Provider: "aws", ExternalID: "222222222222", AWSAuthMode: "access_keys"},
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-multi").Return(plan, nil)
	// GetPlanAccounts and SavePurchaseExecution use override fns (not .On) because the
	// goroutine-based fan-out bypasses the testify mock tracker.
	mockStore.GetPlanAccountsFn = func(_ context.Context, _ string) ([]config.CloudAccount, error) {
		return accounts, nil
	}
	var savedExecs []*config.PurchaseExecution
	var mu sync.Mutex
	mockStore.SavePurchaseExecutionFn = func(_ context.Context, exec *config.PurchaseExecution) error {
		mu.Lock()
		savedExecs = append(savedExecs, exec)
		mu.Unlock()
		return nil
	}
	// SavePurchaseHistory and SendPurchaseConfirmation are called once per account.
	mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil).Times(2)
	mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil).Times(2)

	mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), "aws", mock.Anything).Return(mockProvider, nil)
	mockProvider.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil)
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(common.PurchaseResult{
		Success: true, CommitmentID: "ri-99999",
	}, nil)

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		credStore:       credStore,
		// assumeRoleSTS is nil → access_keys path, no role assumption needed
	}

	err := manager.executePurchase(ctx, exec)
	require.NoError(t, err)

	// The original exec record should be unchanged (fan-out creates per-account copies).
	assert.Nil(t, exec.CloudAccountID)

	// Two per-account execution records should have been saved, each with a distinct account ID.
	require.Len(t, savedExecs, 2)
	accountIDs := []string{*savedExecs[0].CloudAccountID, *savedExecs[1].CloudAccountID}
	assert.ElementsMatch(t, []string{
		"aaaaaaaa-0000-0000-0000-000000000001",
		"aaaaaaaa-0000-0000-0000-000000000002",
	}, accountIDs)

	// platform#631: each account buys only its own rec (2 calls, not 4) and
	// its row carries its own totals, not the whole step's.
	mockServiceClient.AssertNumberOfCalls(t, "PurchaseCommitment", 2)
	for _, e := range savedExecs {
		require.Len(t, e.Recommendations, 1)
		assert.Equal(t, *e.CloudAccountID, *e.Recommendations[0].CloudAccountID)
	}

	mockStore.AssertExpectations(t)
	mockEmail.AssertExpectations(t)
	mockFactory.AssertExpectations(t)
}
