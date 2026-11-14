package purchase

import (
	"context"
	"errors"
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

// TestExecuteMultiAccount_PartialFailure_IsolatesAccounts is the regression
// guard for spec E-2: when one account (account-I) has invalid credentials and
// another (account-V) has valid credentials, account-V's execution must
// complete successfully and its record must be independent of account-I's failure.
//
// What this test pins:
//   - Two SavePurchaseExecution calls fire (one per account — no record is skipped).
//   - account-V's record: Status=="completed", PurchaseID set (purchase went through).
//   - account-I's record: Status=="failed", Error non-empty.
//   - account-I's error text does NOT contain account-V's credential material
//     (cross-account data isolation / log-sanitisation guard).
//   - account-V's CommitmentID does NOT appear in account-I's error text
//     (result_data independence).
//   - The overall executePurchase call returns an error (aggregated from I) —
//     proving the errgroup collected I's failure — while V's record was still
//     saved as "completed" (proving the errgroup did NOT short-circuit on I).
func TestExecuteMultiAccount_PartialFailure_IsolatesAccounts(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := allowFailedNotification(new(MockEmailSender))
	mockFactory := new(MockProviderFactory)
	mockProviderInst := new(MockProvider)
	mockServiceClient := new(MockServiceClient)

	// account-V: valid mock credentials (access_keys path, LoadRaw returns key JSON).
	// account-I: invalid mock credentials (LoadRaw returns nil → "no credentials" resolver error).
	const (
		accountVID  = "vvvvvvvv-0000-0000-0000-000000000001"
		accountIID  = "iiiiiiii-0000-0000-0000-000000000002"
		accountVKey = "AKIAIOSFODNN7EXAMPLE" // sentinel — must not appear in I's error
		commitmentV = "ri-valid-001"         // V's CommitmentID — must not appear in I's error
	)

	accounts := []config.CloudAccount{
		{ID: accountVID, Name: "Valid", Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys"},
		{ID: accountIID, Name: "Invalid", Provider: "aws", ExternalID: "222222222222", AWSAuthMode: "access_keys"},
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
		ExecutionID:     "exec-partial",
		PlanID:          "plan-partial",
		Status:          "pending",
		Recommendations: []config.RecommendationRecord{withAccount(rec, accountVID), withAccount(rec, accountIID)},
	}

	plan := &config.PurchasePlan{
		ID:           "plan-partial",
		Name:         "Partial Failure Plan",
		RampSchedule: config.RampSchedule{CurrentStep: 0, TotalSteps: 1},
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-partial").Return(plan, nil)
	mockStore.GetPlanAccountsFn = func(_ context.Context, _ string) ([]config.CloudAccount, error) {
		return accounts, nil
	}

	// Collect saved execution records concurrency-safely (goroutine fan-out).
	var savedExecs []*config.PurchaseExecution
	var mu sync.Mutex
	mockStore.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
		mu.Lock()
		// Copy the record before appending — executeForAccount reuses the acctExec
		// struct value but Recommendations slice is already deep-copied; capture
		// the status and error which are set before Save is called.
		saved := *e
		savedExecs = append(savedExecs, &saved)
		mu.Unlock()
		return nil
	}

	// Only account-V reaches the provider; account-I fails at credential resolution.
	// Use .Once() so testify fails if the factory is called for account-I as well.
	mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), "aws", mock.Anything).Return(mockProviderInst, nil).Once()
	mockProviderInst.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil).Once()
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)),
		mock.AnythingOfType("common.Recommendation"),
		mock.AnythingOfType("common.PurchaseOptions"),
	).Return(common.PurchaseResult{Success: true, CommitmentID: commitmentV}, nil).Once()

	// Only account-V triggers history + notification.
	mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil).Once()
	mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil).Once()

	// Credential store: V gets valid key JSON; I gets nil (no credentials stored).
	credStore := &MockCredentialStore{
		LoadRawFn: func(_ context.Context, accountID, _ string) ([]byte, error) {
			if accountID == accountVID {
				return []byte(`{"access_key_id":"` + accountVKey + `","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`), nil
			}
			// account-I: no credentials stored → resolver returns error.
			return nil, nil
		},
	}

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		credStore:       credStore,
	}

	err := manager.executePurchase(ctx, exec)

	// The call must return an error (account-I's failure is aggregated).
	// This proves the errgroup collected the failure rather than discarding it.
	require.Error(t, err, "executePurchase must surface account-I's credential failure")

	// Both per-account records must have been saved (no silent skip).
	require.Len(t, savedExecs, 2, "exactly two SavePurchaseExecution calls expected — one per account")

	// Build a map keyed by cloud_account_id for order-independent lookup.
	byAccount := make(map[string]*config.PurchaseExecution, 2)
	for _, e := range savedExecs {
		require.NotNil(t, e.CloudAccountID, "every saved record must have cloud_account_id set")
		byAccount[*e.CloudAccountID] = e
	}

	// --- account-V assertions (success path must be unaffected by I's failure) ---
	recordV, ok := byAccount[accountVID]
	require.True(t, ok, "account-V's execution record must be saved")
	assert.Equal(t, "completed", recordV.Status, "account-V must complete successfully")
	assert.Empty(t, recordV.Error, "account-V's record must carry no error message")

	// Verify the purchase went through: at least one recommendation must be marked Purchased.
	purchasedV := false
	for _, r := range recordV.Recommendations {
		if r.Purchased {
			purchasedV = true
			assert.Equal(t, commitmentV, r.PurchaseID, "account-V's commitment ID must match the mock return value")
		}
	}
	assert.True(t, purchasedV, "account-V must have at least one purchased recommendation")

	// --- account-I assertions (failure path must be correctly recorded) ---
	recordI, ok := byAccount[accountIID]
	require.True(t, ok, "account-I's execution record must be saved")
	assert.Equal(t, "failed", recordI.Status, "account-I must be marked failed")
	assert.NotEmpty(t, recordI.Error, "account-I's record must carry an error message")

	// Log-sanitisation guard: account-I's error must not contain account-V's
	// credential material (cross-account data leak would be a security regression).
	assert.NotContains(t, recordI.Error, accountVKey,
		"account-I's error must not contain account-V's access key (cross-account credential leak)")

	// Result-data independence: account-V's CommitmentID must not bleed into
	// account-I's error or recommendation error fields.
	assert.NotContains(t, recordI.Error, commitmentV,
		"account-I's error must not reference account-V's commitment ID")
	for _, r := range recordI.Recommendations {
		assert.NotContains(t, r.Error, commitmentV,
			"account-I's recommendation errors must not reference account-V's commitment ID")
	}

	// The base exec record must remain untagged (fan-out creates per-account copies).
	assert.Nil(t, exec.CloudAccountID, "base execution record must have nil cloud_account_id after fan-out")

	mockStore.AssertExpectations(t)
	mockEmail.AssertExpectations(t)
	mockFactory.AssertExpectations(t)
	mockProviderInst.AssertExpectations(t)
	mockServiceClient.AssertExpectations(t)
}

// TestExecuteForAccount_CredentialFailure_SaveErrorSurfaced is the regression
// guard for COR-10 (#1184): when credential resolution fails AND persisting
// the per-account "failed" row also fails, the save failure must surface as an
// AUDIT LOSS error alongside the credential error instead of being silently
// discarded. The failed row is as much audit trail as a success row.
func TestExecuteForAccount_CredentialFailure_SaveErrorSurfaced(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := allowFailedNotification(new(MockEmailSender))
	mockFactory := new(MockProviderFactory)

	// No credentials stored — credential resolution fails for the account.
	credStore := &MockCredentialStore{
		LoadRawFn: func(_ context.Context, _, _ string) ([]byte, error) {
			return nil, nil
		},
	}

	// Persisting the per-account "failed" row also fails.
	saveErr := errors.New("db write failed")
	mockStore.SavePurchaseExecutionFn = func(_ context.Context, _ *config.PurchaseExecution) error {
		return saveErr
	}

	account := config.CloudAccount{
		ID: "aaaaaaaa-0000-0000-0000-000000000001", Name: "aws-failing",
		Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys",
	}
	plan := &config.PurchasePlan{
		ID:           "plan-credfail-save",
		Name:         "Credential Failure Save Error Plan",
		RampSchedule: config.RampSchedule{CurrentStep: 0, TotalSteps: 1},
	}
	baseExec := &config.PurchaseExecution{
		ExecutionID: "exec-credfail-save",
		PlanID:      "plan-credfail-save",
		Status:      "pending",
		Recommendations: []config.RecommendationRecord{
			{
				Provider: "aws", Service: "ec2", ResourceType: "m5.large",
				Region: "us-east-1", Count: 1, Selected: true,
			},
		},
	}

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		credStore:       credStore,
	}

	committed, err := manager.executeForAccount(ctx, baseExec, plan, account)
	assert.False(t, committed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AUDIT LOSS: failed to save execution record for account "+account.ID)
	assert.ErrorIs(t, err, saveErr, "the underlying save error must stay inspectable via errors.Is")
	assert.Contains(t, err.Error(), "credential resolution failed for account "+account.ID,
		"the original credential failure must not be masked by the save failure")

	// No provider may ever be constructed when credentials fail to resolve.
	mockFactory.AssertNotCalled(t, "CreateAndValidateProvider", mock.Anything, mock.Anything, mock.Anything)
	mockStore.AssertExpectations(t)
}
