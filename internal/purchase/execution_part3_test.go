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

// TestExecutePurchase_SingleAccount_MixedAccountRecs_Refused is the A05-002
// regression guard (#232). TestSingleCloudAccountIDFromRecs already proves the
// helper itself errors on a mixed-account rec set in isolation, but nothing
// drove that error through the actual single-account execution path (a
// direct-execute purchase with no plan, or a plan-scoped execution with no
// configured accounts) to prove the purchase is refused before any provider
// is ever touched. Without this guard, a regression in resolveSingleAccountProvider
// (e.g. swallowing the SingleCloudAccountIDFromRecs error, or picking the first
// account instead of refusing) would fall through to ambient AWS credentials
// and purchase against the wrong account (#1902) while every existing test
// stays green.
func TestExecutePurchase_SingleAccount_MixedAccountRecs_Refused(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockProviderInst := new(MockProvider)

	aid1, aid2 := "acct-1", "acct-2"
	exec := &config.PurchaseExecution{
		ExecutionID: "exec-mixed-accounts",
		PlanID:      "", // direct-execute: no plan, no plan-accounts fan-out.
		Recommendations: []config.RecommendationRecord{
			{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, UpfrontCost: 100.0, Selected: true, CloudAccountID: &aid1},
			{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, UpfrontCost: 100.0, Selected: true, CloudAccountID: &aid2},
		},
	}

	// Register a matching expectation so the AssertNotCalled below is a
	// meaningful check rather than a vacuous one (a mock with zero
	// registered calls never "not calls" anything interesting).
	mockFactory.On("CreateAndValidateProvider", mock.Anything, mock.Anything, mock.Anything).Return(mockProviderInst, nil).Maybe()

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
	}

	err := manager.executePurchase(ctx, exec)

	require.Error(t, err, "a purchase whose selected recs span two cloud accounts must be refused")
	assert.ErrorIs(t, err, errAmbiguousAccountScope)
	assert.Contains(t, err.Error(), "2 cloud accounts (acct-1, acct-2)")

	// The provider factory must NEVER be invoked: the account-scope check
	// happens before credential resolution, so no ambient-credential
	// fallback purchase can occur against either account.
	mockFactory.AssertNotCalled(t, "CreateAndValidateProvider", mock.Anything, mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "SavePurchaseHistory", mock.Anything, mock.Anything)
}

// TestManager_ExecuteAndFinalize_HistorySaveFailure_StaysVisible is the issue
// #621 secondary-path regression guard. When the AWS purchase SUCCEEDS but the
// purchase_history insert fails, the execution must NOT be silently marked a
// clean "completed" with no trace: it stays "completed" (the money WAS
// committed, so flagging it failed would tempt a double-spend re-approval) but
// carries an audit-gap Error so the History view can surface it. Pre-fix,
// savePurchaseHistory swallowed the error and the purchase vanished from the UI.
func TestManager_ExecuteAndFinalize_HistorySaveFailure_StaysVisible(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockSTS := new(MockSTSClient)
	mockFactory := new(MockProviderFactory)
	mockProviderInst := new(MockProvider)
	mockServiceClient := new(MockServiceClient)
	assertMocksOnCleanup(t, mockEmail, mockSTS, mockProviderInst, mockServiceClient)

	// Direct purchase (no plan) → single-account path, no updatePlanProgress.
	exec := &config.PurchaseExecution{
		ExecutionID: "exec-auditgap",
		PlanID:      "",
		StepNumber:  0,
		Recommendations: []config.RecommendationRecord{
			{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, UpfrontCost: 500.0, Selected: true},
		},
	}

	saveErr := errors.New("insert failed: monthly_cost violates not-null constraint")
	mockStore.On("SavePurchaseHistory", mock.Anything, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(saveErr)
	// The execution record itself must still be persisted (with the audit marker).
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)
	mockEmail.On("SendPurchaseConfirmation", mock.Anything, mock.AnythingOfType("email.NotificationData")).Return(nil)
	mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("123456789012"),
	}, nil)
	mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), "aws", mock.Anything).Return(mockProviderInst, nil)
	mockProviderInst.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil)
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(common.PurchaseResult{
		Success:      true,
		CommitmentID: "ri-auditgap",
	}, nil)

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		stsClient:       mockSTS,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	// executeAndFinalize must NOT return an error: the purchase succeeded.
	err := manager.executeAndFinalize(ctx, exec)
	require.NoError(t, err, "a failed history write must not surface as a purchase failure (re-approval / double-spend risk)")

	assert.Equal(t, "completed", exec.Status, "purchase succeeded, so the execution stays completed")
	assert.NotEmpty(t, exec.Error, "history-save failure must be recorded so the row stays visible in History (issue #621)")
	assert.Contains(t, exec.Error, "ri-auditgap")
	assert.Contains(t, exec.Error, "history record failed to save")
	// The raw persistence error must NOT leak into exec.Error, which is
	// surfaced to clients via the history status_description (CR on #623).
	assert.NotContains(t, exec.Error, "insert failed",
		"raw persistence error text must not reach the user-facing exec.Error")
	assert.NotContains(t, exec.Error, "not-null constraint",
		"raw persistence error text must not reach the user-facing exec.Error")

	mockStore.AssertExpectations(t)
	mockFactory.AssertExpectations(t)
}

// historyGuardStore applies the real purchase_history validation in front of
// the mock, so a test sees the error the Postgres store would return.
type historyGuardStore struct{ *MockConfigStore }

func (s historyGuardStore) SavePurchaseHistory(ctx context.Context, record *config.PurchaseHistoryRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	return s.MockConfigStore.SavePurchaseHistory(ctx, record)
}

// TestManager_ExecuteAndFinalize_EmptyCommitmentID_CompletesWithAuditGap
// (#774): a successful purchase whose result carries no CommitmentID cannot be
// stored in purchase_history. The execution must still complete, with the
// audit-gap note, never fail and invite a re-approval of a purchase that fired.
func TestManager_ExecuteAndFinalize_EmptyCommitmentID_CompletesWithAuditGap(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockSTS := new(MockSTSClient)
	mockFactory := new(MockProviderFactory)
	mockProviderInst := new(MockProvider)
	mockServiceClient := new(MockServiceClient)
	assertMocksOnCleanup(t, mockStore, mockEmail, mockSTS, mockFactory, mockProviderInst, mockServiceClient)

	exec := &config.PurchaseExecution{
		ExecutionID: "exec-empty-id",
		StepNumber:  0,
		Recommendations: []config.RecommendationRecord{
			{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, UpfrontCost: 500.0, Selected: true},
		},
	}
	// SavePurchaseHistory is deliberately not expected on the mock: the guard
	// must reject the record before it reaches the store.
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)
	mockEmail.On("SendPurchaseConfirmation", mock.Anything, mock.AnythingOfType("email.NotificationData")).Return(nil)
	mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("123456789012"),
	}, nil)
	mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), "aws", mock.Anything).Return(mockProviderInst, nil)
	mockProviderInst.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil)
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(common.PurchaseResult{
		Success:      true,
		CommitmentID: "",
	}, nil)

	manager := &Manager{
		config:          historyGuardStore{mockStore},
		email:           mockEmail,
		stsClient:       mockSTS,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	require.NoError(t, manager.executeAndFinalize(ctx, exec))

	assert.Equal(t, "completed", exec.Status, "the purchase fired, so the execution stays completed")
	assert.Contains(t, exec.Error, "history record failed to save", "the missing history row must be flagged on the execution")
	mockStore.AssertNotCalled(t, "SavePurchaseHistory", mock.Anything, mock.Anything)
}

// TestManager_ExecuteAndFinalize_SingleAccount_PartialSuccess is the issue #642
// regression guard for the single-account path. A two-rec direct purchase where
// rec A succeeds and rec B fails must:
//   - NOT be marked "failed" (rec A committed a real purchase; failing the row
//     invites a re-approve that double-buys A).
//   - be recorded as "partially_completed".
//   - still SEND the confirmation email (for the rec that did purchase).
//   - persist a purchase_history row only for the succeeded rec.
func TestManager_ExecuteAndFinalize_SingleAccount_PartialSuccess(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := allowFailedNotification(new(MockEmailSender))
	mockSTS := new(MockSTSClient)
	mockFactory := new(MockProviderFactory)
	mockProviderInst := new(MockProvider)
	mockServiceClient := new(MockServiceClient)
	assertMocksOnCleanup(t, mockFactory, mockProviderInst, mockServiceClient)

	exec := &config.PurchaseExecution{
		ExecutionID: "exec-partial-single",
		PlanID:      "",
		StepNumber:  0,
		Source:      common.PurchaseSourceWeb,
		Recommendations: []config.RecommendationRecord{
			{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, UpfrontCost: 300.0, Savings: 50.0, Selected: true},
			{Provider: "aws", Service: "ec2", ResourceType: "c5.xlarge", Region: "us-east-1", Count: 1, UpfrontCost: 600.0, Savings: 90.0, Selected: true},
		},
	}

	// History row must be saved ONLY for the rec that succeeded (m5.large).
	mockStore.On("SavePurchaseHistory", mock.Anything, mock.MatchedBy(func(r *config.PurchaseHistoryRecord) bool {
		return r.ResourceType == "m5.large"
	})).Return(nil).Once()
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)
	// Confirmation must still go out (the issue's core complaint).
	mockEmail.On("SendPurchaseConfirmation", mock.Anything, mock.AnythingOfType("email.NotificationData")).Return(nil).Once()
	// Ambient STS only consulted as a fallback (no account resolved here).
	mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("123456789012"),
	}, nil).Maybe()

	mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), "aws", mock.Anything).Return(mockProviderInst, nil)
	mockProviderInst.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil)
	// m5.large succeeds; c5.xlarge fails.
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)),
		mock.MatchedBy(func(r common.Recommendation) bool { return r.ResourceType == "m5.large" }),
		mock.AnythingOfType("common.PurchaseOptions"),
	).Return(common.PurchaseResult{Success: true, CommitmentID: "ri-ok"}, nil).Once()
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)),
		mock.MatchedBy(func(r common.Recommendation) bool { return r.ResourceType == "c5.xlarge" }),
		mock.AnythingOfType("common.PurchaseOptions"),
	).Return(common.PurchaseResult{}, errors.New("offering not available")).Once()

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		stsClient:       mockSTS,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	err := manager.executeAndFinalize(ctx, exec)
	require.Error(t, err, "a partial failure must surface an error so the run isn't reported as a clean success")

	assert.Equal(t, "partially_completed", exec.Status, "partial success must never be 'failed' (double-spend hazard on the committed rec)")
	require.NotNil(t, exec.CompletedAt, "a partial run is terminal for its committed recs")
	assert.NotEmpty(t, exec.Error, "the per-rec failure must be recorded on the row")
	assert.Contains(t, exec.Error, "offering not available")

	// The succeeded rec must be flagged Purchased; the failed one carries its error.
	assert.True(t, exec.Recommendations[0].Purchased, "m5.large committed")
	assert.Equal(t, "ri-ok", exec.Recommendations[0].PurchaseID)
	assert.False(t, exec.Recommendations[1].Purchased, "c5.xlarge did not commit")
	assert.NotEmpty(t, exec.Recommendations[1].Error)

	mockStore.AssertExpectations(t)
	mockEmail.AssertExpectations(t)
}

// TestExecuteForAccount_PartialSuccess is the issue #642 regression guard for
// the multi-account fan-out path: within a single account, one rec commits and
// another fails. The per-account execution record must be saved as
// "partially_completed" (NOT "failed"), carry the failure in Error, and the
// confirmation must still go out for the committed rec.
func TestExecuteForAccount_PartialSuccess(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := allowFailedNotification(new(MockEmailSender))
	mockFactory := new(MockProviderFactory)
	mockProviderInst := new(MockProvider)
	mockServiceClient := new(MockServiceClient)
	assertMocksOnCleanup(t, mockFactory, mockProviderInst, mockServiceClient)

	const acctID = "aaaaaaaa-0000-0000-0000-000000000001"
	account := config.CloudAccount{ID: acctID, Name: "Prod", Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys"}

	baseExec := &config.PurchaseExecution{
		ExecutionID: "exec-base",
		PlanID:      "plan-x",
		Source:      common.PurchaseSourceWeb,
		Recommendations: []config.RecommendationRecord{
			{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, UpfrontCost: 300.0, Savings: 50.0, Selected: true},
			{Provider: "aws", Service: "ec2", ResourceType: "c5.xlarge", Region: "us-east-1", Count: 1, UpfrontCost: 600.0, Savings: 90.0, Selected: true},
		},
	}
	plan := &config.PurchasePlan{ID: "plan-x", Name: "Plan X"}

	var savedExec *config.PurchaseExecution
	mockStore.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
		c := *e
		savedExec = &c
		return nil
	}
	mockStore.On("SavePurchaseHistory", ctx, mock.MatchedBy(func(r *config.PurchaseHistoryRecord) bool {
		return r.ResourceType == "m5.large"
	})).Return(nil).Once()
	mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil).Once()

	credStore := &MockCredentialStore{
		LoadRawFn: func(_ context.Context, _, _ string) ([]byte, error) {
			return []byte(`{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`), nil
		},
	}

	mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), "aws", mock.Anything).Return(mockProviderInst, nil)
	mockProviderInst.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil)
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)),
		mock.MatchedBy(func(r common.Recommendation) bool { return r.ResourceType == "m5.large" }),
		mock.AnythingOfType("common.PurchaseOptions"),
	).Return(common.PurchaseResult{Success: true, CommitmentID: "ri-ok"}, nil).Once()
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)),
		mock.MatchedBy(func(r common.Recommendation) bool { return r.ResourceType == "c5.xlarge" }),
		mock.AnythingOfType("common.PurchaseOptions"),
	).Return(common.PurchaseResult{}, errors.New("offering not available")).Once()

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		credStore:       credStore,
		dashboardURL:    "https://dashboard.example.com",
	}

	committed, err := manager.executeForAccount(ctx, baseExec, plan, account)
	require.Error(t, err, "the per-rec failure must surface to the aggregator")
	assert.True(t, committed, "a partial run committed at least one rec, so committed must be true (issue #1014)")

	require.NotNil(t, savedExec, "per-account record must be saved")
	assert.Equal(t, "partially_completed", savedExec.Status, "partial success must never be 'failed' (double-spend hazard)")
	require.NotNil(t, savedExec.CompletedAt)
	assert.Contains(t, savedExec.Error, "offering not available")

	mockStore.AssertExpectations(t)
	mockEmail.AssertExpectations(t)
}
