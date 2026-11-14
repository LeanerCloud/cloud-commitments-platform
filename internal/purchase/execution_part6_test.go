package purchase

import (
	"context"
	"sync"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
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
