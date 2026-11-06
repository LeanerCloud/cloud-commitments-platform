package purchase

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// skipFixture runs a three-account fan-out one account at a time. Account A
// (valid credentials) buys; when buyCancels is set, its purchase call cancels
// the run context, so B and C never start (issue #1672). B and C have no
// stored credentials, so if either did start it would leave a credential
// error instead of the skip.
type skipFixture struct {
	saved    []config.PurchaseExecution
	mu       sync.Mutex
	saveHook func(context.Context)
	// cancelAtB moves the cancel from A's purchase to B's credential load, so B
	// starts and fails on its own (a started account, not a skip).
	cancelAtB bool
}

const (
	skipAcctA = "aaaaaaaa-0000-0000-0000-000000000001"
	skipAcctB = "bbbbbbbb-0000-0000-0000-000000000002"
	skipAcctC = "cccccccc-0000-0000-0000-000000000003"
)

func (f *skipFixture) run(t *testing.T, ctx context.Context, cancel context.CancelFunc, aBuys bool) error {
	t.Helper()
	t.Setenv("CUDLY_MAX_ACCOUNT_PARALLELISM", "1")
	store := new(MockConfigStore)
	mailer := allowFailedNotification(new(MockEmailSender))
	factory := new(MockProviderFactory)
	prov := new(MockProvider)
	client := new(MockServiceClient)

	accounts := []config.CloudAccount{
		{ID: skipAcctA, Name: "A", Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys"},
		{ID: skipAcctB, Name: "B", Provider: "aws", ExternalID: "222222222222", AWSAuthMode: "access_keys"},
		{ID: skipAcctC, Name: "C", Provider: "aws", ExternalID: "333333333333", AWSAuthMode: "access_keys"},
	}
	rec := config.RecommendationRecord{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1",
		Count: 1, Savings: 75, UpfrontCost: 300, Selected: true}
	exec := &config.PurchaseExecution{
		ExecutionID: "exec-skip", PlanID: "plan-skip", Status: "pending", IdempotencyKey: "lineage-skip",
		Recommendations: []config.RecommendationRecord{withAccount(rec, skipAcctA), withAccount(rec, skipAcctB), withAccount(rec, skipAcctC)},
	}
	plan := &config.PurchasePlan{ID: "plan-skip", Name: "Skip", RampSchedule: config.RampSchedule{TotalSteps: 1}}

	store.SavePurchaseExecutionFn = func(saveCtx context.Context, e *config.PurchaseExecution) error {
		if f.saveHook != nil {
			f.saveHook(saveCtx)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.saved = append(f.saved, *e)
		return nil
	}
	store.On("SavePurchaseHistory", mock.Anything, mock.Anything).Return(nil).Maybe()
	mailer.On("SendPurchaseConfirmation", mock.Anything, mock.Anything).Return(nil).Maybe()
	factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(prov, nil).Maybe()
	prov.On("GetServiceClient", mock.Anything, common.ServiceEC2, "us-east-1").Return(client, nil).Maybe()
	result := common.PurchaseResult{Success: true, CommitmentID: "ri-a"}
	var buyErr error
	if !aBuys {
		result, buyErr = common.PurchaseResult{}, errors.New("insufficient capacity")
	}
	client.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) {
			if !f.cancelAtB {
				cancel()
			}
		}).Return(result, buyErr).Once()

	m := &Manager{config: store, email: mailer, providerFactory: factory, credStore: &MockCredentialStore{
		LoadRawFn: func(_ context.Context, accountID, _ string) ([]byte, error) {
			if accountID == skipAcctA {
				return []byte(`{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`), nil
			}
			if accountID == skipAcctB && f.cancelAtB {
				cancel()
			}
			return nil, nil
		},
	}}
	return m.executeMultiAccount(ctx, exec, plan, accounts)
}

func (f *skipFixture) rowsFor(id string) []config.PurchaseExecution {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []config.PurchaseExecution
	for _, e := range f.saved {
		if e.CloudAccountID != nil && *e.CloudAccountID == id {
			out = append(out, e)
		}
	}
	return out
}

// A cancel that lands mid-fan-out, after one account committed: the run is
// acked (#1014), so every account that never started must get its own failed
// row proving nothing was submitted, or its purchases silently never happen.
func TestExecuteMultiAccount_RecordsAccountsSkippedAfterACommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &skipFixture{}
	err := f.run(t, ctx, cancel, true)

	var partial *multiAccountPartialError
	require.ErrorAs(t, err, &partial, "a committed account keeps the partial ack")
	require.Len(t, f.rowsFor(skipAcctA), 1)
	assert.Equal(t, "completed", f.rowsFor(skipAcctA)[0].Status)
	for _, id := range []string{skipAcctB, skipAcctC} {
		rows := f.rowsFor(id)
		require.Len(t, rows, 1, "account %s must get exactly one row", id)
		r := rows[0]
		assert.Equal(t, "failed", r.Status, id)
		assert.True(t, r.NoSubmission, "%s: the skip proves nothing was submitted", id)
		assert.Equal(t, skippedAccountNoRetryNote, r.Error, "%s: Retry refuses EC2, so the row must not promise one", id)
		assert.Equal(t, "lineage-skip:"+id, r.IdempotencyKey, "%s: the stable per-account key", id)
		assert.NotEqual(t, "exec-skip", r.ExecutionID, id)
		require.Len(t, r.Recommendations, 1, "%s: the row keeps only its own account's recs, so a retry buys exactly them", id)
		require.NotNil(t, r.Recommendations[0].CloudAccountID)
		assert.Equal(t, id, *r.Recommendations[0].CloudAccountID)
		assert.Nil(t, r.CompletedAt, id)
	}
}

// The skip row promises a retry only where Retry accepts the row.
func TestSkippedAccountNote_MatchesRetryEligibility(t *testing.T) {
	cases := []struct {
		provider, service string
		want              string
	}{
		{"aws", "ec2", skippedAccountNoRetryNote},
		{"aws", "redshift", skippedAccountNoRetryNote},
		{"azure", "savingsplans", skippedAccountNoRetryNote},
		{"aws", "rds", skippedAccountRetryNote},
		{"gcp", "compute", skippedAccountRetryNote},
	}
	for _, c := range cases {
		row := &config.PurchaseExecution{Recommendations: []config.RecommendationRecord{{Provider: c.provider, Service: c.service}}}
		assert.Equal(t, c.want, skippedAccountNote(row), c.provider+"/"+c.service)
		assert.Equal(t, RedriveRefusalReason(row) == "", c.want == skippedAccountRetryNote, c.provider+"/"+c.service)
	}
}

// Without a commit the run is a flat failure: the root is finalized failed and
// a root Retry re-runs every account, so no per-account skip rows are written.
func TestExecuteMultiAccount_NoSkipRowsWhenNothingCommitted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &skipFixture{}
	err := f.run(t, ctx, cancel, false)

	require.ErrorIs(t, err, errAllAccountsFailed)
	assert.Len(t, f.rowsFor(skipAcctA), 1)
	assert.Empty(t, f.rowsFor(skipAcctB))
	assert.Empty(t, f.rowsFor(skipAcctC))
}

// The skip rows are saved after the run context ended, so the save must not
// use it.
func TestExecuteMultiAccount_SkipRowSaveOutlivesTheRunContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &skipFixture{}
	var live []bool
	var mu sync.Mutex
	f.saveHook = func(saveCtx context.Context) {
		mu.Lock()
		defer mu.Unlock()
		live = append(live, saveCtx.Err() == nil)
		if dl, ok := saveCtx.Deadline(); ok {
			assert.WithinDuration(t, time.Now(), dl, 10*time.Minute, "a bounded save budget")
		}
	}
	err := f.run(t, ctx, cancel, true)
	require.Error(t, err)
	require.Len(t, f.rowsFor(skipAcctB), 1)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []bool{true, true}, live[len(live)-2:], "the two skip-row saves run on a live context")
}

// An account that started and failed already wrote its own row; only the
// accounts that never started get a skip row.
func TestExecuteMultiAccount_StartedAccountGetsNoSkipRow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &skipFixture{cancelAtB: true}
	err := f.run(t, ctx, cancel, true)

	var partial *multiAccountPartialError
	require.ErrorAs(t, err, &partial)
	b := f.rowsFor(skipAcctB)
	require.Len(t, b, 1, "B started: its own credential-failure row, no skip row beside it")
	assert.NotContains(t, b[0].Error, "nothing was submitted")
	c := f.rowsFor(skipAcctC)
	require.Len(t, c, 1)
	assert.Contains(t, c[0].Error, "nothing was submitted")
}
