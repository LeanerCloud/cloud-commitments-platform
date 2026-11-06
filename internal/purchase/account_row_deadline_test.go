package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const lateTerminalT = 200 * time.Millisecond // T; the account slice T/2 is 100ms

// accountSaveCase triggers the run context ending at one of the three places
// executeForAccount saves its row.
type accountSaveCase struct {
	name    string
	trigger func(f *lateFixtureOneAccount)
	status  string
}

type lateFixtureOneAccount struct {
	cancel  context.CancelFunc
	store   *MockConfigStore
	client  *MockServiceClient
	loadRaw func(context.Context, string, string) ([]byte, error)
}

// Each of executeForAccount's three row saves must run on a live context whose
// deadline is the run's deadline plus T/2, however the run context ended.
func TestExecuteForAccount_RowSavesAreLiveAndAnchoredOnTheRunDeadline(t *testing.T) {
	keys := func(context.Context, string, string) ([]byte, error) {
		return []byte(`{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`), nil
	}
	cases := []accountSaveCase{
		{"processing error", func(f *lateFixtureOneAccount) {
			f.store.On("GetGlobalConfig", mock.Anything).Run(func(mock.Arguments) { f.cancel() }).Return(nil, errors.New("db down"))
		}, "failed"},
		{"outcome", func(f *lateFixtureOneAccount) {
			f.client.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
				Run(func(mock.Arguments) { f.cancel() }).Return(common.PurchaseResult{Success: true, CommitmentID: "ri-1"}, nil)
		}, "completed"},
		{"credential failure", func(f *lateFixtureOneAccount) {
			f.loadRaw = func(context.Context, string, string) ([]byte, error) { f.cancel(); return nil, nil }
		}, "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := new(MockConfigStore)
			mailer := allowFailedNotification(new(MockEmailSender))
			factory := new(MockProviderFactory)
			prov := new(MockProvider)
			client := new(MockServiceClient)
			runCtx, runCancel := context.WithTimeout(context.Background(), time.Hour)
			defer runCancel()
			f := &lateFixtureOneAccount{cancel: runCancel, store: store, client: client, loadRaw: keys}

			var saved []config.PurchaseExecution
			var live []bool
			var deadlines []time.Time
			store.SavePurchaseExecutionFn = func(ctx context.Context, e *config.PurchaseExecution) error {
				saved = append(saved, *e)
				live = append(live, ctx.Err() == nil)
				dl, _ := ctx.Deadline()
				deadlines = append(deadlines, dl)
				return nil
			}
			store.On("SavePurchaseHistory", mock.Anything, mock.Anything).Return(nil).Maybe()
			mailer.On("SendPurchaseConfirmation", mock.Anything, mock.Anything).Return(nil).Maybe()
			factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(prov, nil).Maybe()
			prov.On("GetServiceClient", mock.Anything, common.ServiceEC2, "us-east-1").Return(client, nil).Maybe()
			tc.trigger(f)

			m := &Manager{config: store, email: mailer, providerFactory: factory,
				budgets: purchaseBudgets{terminalSave: lateTerminalT},
				credStore: &MockCredentialStore{LoadRawFn: func(c context.Context, a, k string) ([]byte, error) {
					return f.loadRaw(c, a, k)
				}}}
			account := config.CloudAccount{ID: skipAcctA, Name: "A", Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys"}
			base := &config.PurchaseExecution{ExecutionID: "exec-pin", PlanID: "plan-pin", IdempotencyKey: "lineage-pin",
				Recommendations: []config.RecommendationRecord{withAccount(config.RecommendationRecord{Provider: "aws", Service: "ec2",
					ResourceType: "m5.large", Region: "us-east-1", Count: 1, Selected: true}, skipAcctA)}}
			runDeadline, _ := runCtx.Deadline()

			_, _ = m.executeForAccount(runCtx, base, &config.PurchasePlan{ID: "plan-pin", Name: "pin"}, account)

			require.Len(t, saved, 1)
			assert.Equal(t, tc.status, saved[0].Status)
			assert.True(t, live[0], "the row is saved on a context the run's end did not cancel")
			assert.True(t, deadlines[0].Equal(runDeadline.Add(lateTerminalT/2)), "deadline is E+T/2, got %v want %v", deadlines[0], runDeadline.Add(lateTerminalT/2))
		})
	}
}

// Without a run deadline the account row falls back to a fresh full budget.
func TestSaveAccountRow_NoRunDeadlineFallsBackToFullBudget(t *testing.T) {
	store := new(MockConfigStore)
	var deadline time.Time
	store.SavePurchaseExecutionFn = func(ctx context.Context, _ *config.PurchaseExecution) error {
		deadline, _ = ctx.Deadline()
		return nil
	}
	m := &Manager{config: store, budgets: purchaseBudgets{terminalSave: time.Second}}
	start := time.Now()
	require.NoError(t, m.saveAccountRow(context.Background(), &config.PurchaseExecution{}))
	assert.WithinDuration(t, start.Add(time.Second), deadline, 100*time.Millisecond)
}
