package purchase

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestExecuteMultiAccount_RunsAccountsInParallel pins the spec E-2 parallelism
// promise: a multi-account plan must fan out concurrently, not iterate serially.
// The companion TestExecuteMultiAccount_PartialFailure_IsolatesAccounts above
// proves per-account error isolation but would still pass if executeMultiAccount
// were refactored to a serial for-loop. This test fails on a serial loop.
//
// Mechanics: two accounts, each with valid credentials. Parallelism is
// asserted structurally, not by elapsed wall time (issue #38): each
// PurchaseCommitment call bumps an atomic in-flight counter, records the
// peak, then waits at a barrier until both accounts' calls have arrived.
// Under a parallel fan-out both calls are in flight together, so the barrier
// opens at once and the peak is 2 regardless of runner load. Under a serial
// loop the second call cannot start until the first returns, so the first
// waits out barrierTimeout alone and the peak stays 1, failing the assertion.
// barrierTimeout only bounds that failure path; a passing run never waits on it.
func TestExecuteMultiAccount_RunsAccountsInParallel(t *testing.T) {
	const (
		accountCount   = 2
		barrierTimeout = 2 * time.Second
	)

	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockProviderInst := new(MockProvider)
	mockServiceClient := new(MockServiceClient)

	const (
		accountAID = "aaaaaaaa-0000-0000-0000-000000000001"
		accountBID = "bbbbbbbb-0000-0000-0000-000000000002"
	)

	accounts := []config.CloudAccount{
		{ID: accountAID, Name: "A", Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys"},
		{ID: accountBID, Name: "B", Provider: "aws", ExternalID: "222222222222", AWSAuthMode: "access_keys"},
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
		ExecutionID:     "exec-parallel",
		PlanID:          "plan-parallel",
		Status:          "pending",
		Recommendations: []config.RecommendationRecord{withAccount(rec, accountAID), withAccount(rec, accountBID)},
	}

	plan := &config.PurchasePlan{
		ID:           "plan-parallel",
		Name:         "Parallel Fan-Out Plan",
		RampSchedule: config.RampSchedule{CurrentStep: 0, TotalSteps: 1},
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-parallel").Return(plan, nil)
	mockStore.GetPlanAccountsFn = func(_ context.Context, _ string) ([]config.CloudAccount, error) {
		return accounts, nil
	}

	var savedMu sync.Mutex
	var savedExecs []*config.PurchaseExecution
	mockStore.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
		savedMu.Lock()
		saved := *e
		savedExecs = append(savedExecs, &saved)
		savedMu.Unlock()
		return nil
	}

	mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), "aws", mock.Anything).Return(mockProviderInst, nil).Twice()
	mockProviderInst.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil).Twice()

	// Each PurchaseCommitment call counts itself in flight, then waits at the
	// barrier for the other account's call. The barrier makes the overlap
	// deterministic instead of relying on two sleeps happening to coincide.
	var inflight, peak, arrived atomic.Int32
	var barrierTimedOut atomic.Bool
	allArrived := make(chan struct{})
	updatePeak := func(cur int32) {
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				return
			}
		}
	}
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)),
		mock.AnythingOfType("common.Recommendation"),
		mock.AnythingOfType("common.PurchaseOptions"),
	).Return(common.PurchaseResult{Success: true, CommitmentID: "ri-parallel"}, nil).
		Run(func(_ mock.Arguments) {
			updatePeak(inflight.Add(1))
			defer inflight.Add(-1)
			if arrived.Add(1) == accountCount {
				close(allArrived)
			}
			select {
			case <-allArrived:
			case <-time.After(barrierTimeout):
				barrierTimedOut.Store(true)
			}
		}).Twice()

	mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil).Twice()
	mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil).Twice()

	credStore := &MockCredentialStore{
		LoadRawFn: func(_ context.Context, _, _ string) ([]byte, error) {
			return []byte(`{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`), nil
		},
	}

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		credStore:       credStore,
	}

	err := manager.executePurchase(ctx, exec)

	require.NoError(t, err, "both accounts have valid credentials and should succeed")
	require.Len(t, savedExecs, 2, "exactly two SavePurchaseExecution calls expected — one per account")

	// The core parallelism assertion: both accounts' PurchaseCommitment calls
	// were in flight at the same time. A serial loop peaks at 1.
	assert.GreaterOrEqual(t, peak.Load(), int32(accountCount),
		"executeMultiAccount must run accounts in parallel: peak in-flight PurchaseCommitment calls was %d, want %d",
		peak.Load(), accountCount)
	assert.False(t, barrierTimedOut.Load(),
		"a PurchaseCommitment call waited %v at the barrier without the other account's call arriving: accounts ran serially",
		barrierTimeout)

	mockStore.AssertExpectations(t)
	mockEmail.AssertExpectations(t)
	mockFactory.AssertExpectations(t)
	mockProviderInst.AssertExpectations(t)
	mockServiceClient.AssertExpectations(t)
}
