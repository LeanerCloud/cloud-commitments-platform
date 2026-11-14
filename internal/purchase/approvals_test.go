package purchase

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// approveFromStatuses mirrors the value Manager.ApproveAndExecute passes to
// TransitionExecutionStatus. Centralized so a future change to the
// allowed-from set updates every test in one place.
var approveFromStatuses = []string{"pending", "notified"}

// newApproveManager wires a Manager backed by a fresh MockConfigStore +
// MockEmailSender. Returns both mocks so tests can register expectations
// per scenario without re-stating the boilerplate.
func newApproveManager(t *testing.T) (*Manager, *MockConfigStore, *MockEmailSender) {
	t.Helper()
	store := new(MockConfigStore)
	sender := allowFailedNotification(new(MockEmailSender))

	// A plan step with no recommendations is refused (#609), so the
	// approved fixtures carry one recommendation; this factory lets it
	// purchase. Maybe(): tests that never reach execution register nothing.
	prov := new(MockProvider)
	svc := new(MockServiceClient)
	factory := new(MockProviderFactory)
	assertMocksOnCleanup(t, store, sender, prov, svc, factory)
	factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(prov, nil).Maybe()
	prov.On("GetServiceClient", mock.Anything, common.ServiceEC2, "us-east-1").Return(svc, nil).Maybe()
	svc.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
		Return(common.PurchaseResult{Success: true, CommitmentID: "ri-approval-test"}, nil).Maybe()

	return &Manager{
		config:          store,
		email:           sender,
		providerFactory: factory,
		dashboardURL:    "https://dashboard.example.com",
	}, store, sender
}

// approvalTestRecs is the one recommendation an approved plan step carries in
// these tests.
func approvalTestRecs() []config.RecommendationRecord {
	return []config.RecommendationRecord{{
		Provider:     "aws",
		Service:      "ec2",
		ResourceType: "m5.large",
		Region:       "us-east-1",
		Count:        1,
		Savings:      10,
		UpfrontCost:  50,
		Selected:     true,
	}}
}

// stubExecuteChain wires the mocks that Manager.executeAndFinalize touches
// for an approved execution carrying approvalTestRecs (no plan accounts).
// GetPlanAccounts uses the Fn-override pattern in this mock, not testify's
// .On; left at its nil default it returns (nil, nil) which drops us into the
// single-account branch.
func stubExecuteChain(t *testing.T, store *MockConfigStore, sender *MockEmailSender, planID string) {
	t.Helper()
	plan := &config.PurchasePlan{ID: planID, Name: "test-plan"}
	store.On("GetPurchasePlan", mock.Anything, planID).Return(plan, nil)
	store.On("SavePurchaseHistory", mock.Anything, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil)
	sender.On("SendPurchaseConfirmation", mock.Anything, mock.Anything).Return(nil)
	// finalizeExecution writes status=completed via SavePurchaseExecution.
	store.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)
	// updatePlanProgress calls CompletePlanStep (atomic, issue #1071); every
	// caller of this helper stamps StepNumber: 1 on its "updated" fixture.
	store.On("CompletePlanStep", mock.Anything, planID, 1).Return(nil)
}

func TestManager_ApproveExecution_Success(t *testing.T) {
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-123",
		PlanID:        "plan-456",
		Status:        "pending",
		ApprovalToken: config.HashApprovalToken("valid-token"),
	}
	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-123",
		PlanID:          "plan-456",
		Status:          "approved",
		ApprovalToken:   config.HashApprovalToken("valid-token"),
		StepNumber:      1,
		Recommendations: approvalTestRecs(),
	}

	store.On("GetExecutionByID", ctx, "exec-123").Return(execution, nil)
	store.On("TransitionExecutionStatusTokenPinned", ctx, "exec-123", approveFromStatuses, "running", (*string)(nil), "valid-token").Return(updated, nil)
	stubExecuteChain(t, store, sender, "plan-456")

	final, _, err := manager.ApproveExecution(ctx, "exec-123", "valid-token", "", nil)
	require.NoError(t, err)
	require.NotNil(t, final, "the token approve returns the row it ran (issue #532)")
	assert.Equal(t, "completed", final.Status)
	store.AssertExpectations(t)
	sender.AssertExpectations(t)
}

func TestManager_ApproveExecution_StampsApprovedBy(t *testing.T) {
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-123",
		PlanID:        "plan-456",
		Status:        "pending",
		ApprovalToken: config.HashApprovalToken("valid-token"),
	}
	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-123",
		PlanID:          "plan-456",
		Status:          "approved",
		ApprovalToken:   config.HashApprovalToken("valid-token"),
		StepNumber:      1,
		Recommendations: approvalTestRecs(),
	}

	store.On("GetExecutionByID", ctx, "exec-123").Return(execution, nil)
	store.On("TransitionExecutionStatusTokenPinned", ctx, "exec-123", approveFromStatuses, "running", (*string)(nil), "valid-token").Return(updated, nil)
	stubExecuteChain(t, store, sender, "plan-456")

	_, _, err := manager.ApproveExecution(ctx, "exec-123", "valid-token", "operator@example.com", nil)
	require.NoError(t, err)

	// Two SavePurchaseExecution calls land on the same pointer (the
	// transitioned row): once for the ApprovedBy stamp, once at the end
	// of executeAndFinalize. Both should reflect the actor.
	require.NotNil(t, updated.ApprovedBy)
	assert.Equal(t, "operator@example.com", *updated.ApprovedBy)
	store.AssertExpectations(t)
	sender.AssertExpectations(t)
}

func TestManager_ApproveExecution_NotifiedStatus(t *testing.T) {
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-123",
		PlanID:        "plan-456",
		Status:        "notified",
		ApprovalToken: config.HashApprovalToken("valid-token"),
	}
	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-123",
		PlanID:          "plan-456",
		Status:          "approved",
		ApprovalToken:   config.HashApprovalToken("valid-token"),
		StepNumber:      1,
		Recommendations: approvalTestRecs(),
	}

	store.On("GetExecutionByID", ctx, "exec-123").Return(execution, nil)
	store.On("TransitionExecutionStatusTokenPinned", ctx, "exec-123", approveFromStatuses, "running", (*string)(nil), "valid-token").Return(updated, nil)
	stubExecuteChain(t, store, sender, "plan-456")

	_, _, err := manager.ApproveExecution(ctx, "exec-123", "valid-token", "", nil)
	require.NoError(t, err)
	store.AssertExpectations(t)
	sender.AssertExpectations(t)
}

func TestManager_ApproveExecution_InvalidToken(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-123",
		PlanID:        "plan-456",
		Status:        "pending",
		ApprovalToken: config.HashApprovalToken("valid-token"),
	}

	store.On("GetExecutionByID", ctx, "exec-123").Return(execution, nil)

	_, _, err := manager.ApproveExecution(ctx, "exec-123", "invalid-token", "", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid approval token")
	// Critically: TransitionExecutionStatus and SavePurchaseExecution must
	// NOT have been called. A token-validation bypass is exactly what the
	// constant-time comparison guards against.
	store.AssertNotCalled(t, "TransitionExecutionStatusTokenPinned", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "SavePurchaseExecution", mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

func TestManager_ApproveExecution_EmptyToken(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-123",
		Status:        "pending",
		ApprovalToken: config.HashApprovalToken("valid-token"),
	}
	store.On("GetExecutionByID", ctx, "exec-123").Return(execution, nil)

	_, _, err := manager.ApproveExecution(ctx, "exec-123", "", "", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid approval token")
	store.AssertNotCalled(t, "TransitionExecutionStatusTokenPinned", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestManager_ApproveExecution_NotFound(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	store.On("GetExecutionByID", ctx, "exec-123").Return(nil, fmt.Errorf("%w: execution exec-123", config.ErrNotFound))

	_, _, err := manager.ApproveExecution(ctx, "exec-123", "token", "", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "execution not found")
	store.AssertExpectations(t)
}

func TestManager_ApproveExecution_GetError(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	store.On("GetExecutionByID", ctx, "exec-123").Return(nil, errors.New("database error"))

	_, _, err := manager.ApproveExecution(ctx, "exec-123", "token", "", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get execution")
	store.AssertExpectations(t)
}

func TestManager_ApproveExecution_TransitionFails(t *testing.T) {
	// Closes the race-with-cancel/expire path: token is valid but the row
	// drifted out of pending/notified between fetch and atomic UPDATE.
	// TransitionExecutionStatus 0-rows; the caller surfaces a clean error
	// and never enters the execute chain.
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-123",
		PlanID:        "plan-456",
		Status:        "pending",
		ApprovalToken: config.HashApprovalToken("valid-token"),
	}
	store.On("GetExecutionByID", ctx, "exec-123").Return(execution, nil)
	store.On("TransitionExecutionStatusTokenPinned", ctx, "exec-123", approveFromStatuses, "running", (*string)(nil), "valid-token").
		Return(nil, errors.New(`execution exec-123 cannot transition from "canceled" to "approved"`))

	final, _, err := manager.ApproveExecution(ctx, "exec-123", "valid-token", "", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot transition")
	assert.Nil(t, final, "a lost claim never started a run, so no row comes back")
	// Execute chain must not run after a failed transition.
	store.AssertNotCalled(t, "GetPurchasePlan", mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

// TestManager_ApproveAndExecute_EmptyPlanID is the regression guard for
// issue #395. Direct-execute purchases (Opportunities "Purchase" button)
// arrive with PlanID = "". Before the fix, executePurchase called
// GetPurchasePlan(ctx, "") which crashed with SQLSTATE 22P02 because the
// Postgres purchase_plans.id column is UUID. This test asserts the
// approve+execute path now short-circuits the plan/accounts fetch and the
// plan-progress update, lands the execution in a terminal state, and
// never calls GetPurchasePlan / GetPlanAccounts / UpdatePurchasePlan.
func TestManager_ApproveAndExecute_EmptyPlanID(t *testing.T) {
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	updated := &config.PurchaseExecution{
		ExecutionID:   "exec-direct-1",
		PlanID:        "", // direct-execute shape — no associated plan
		Status:        "approved",
		ApprovalToken: "tok",
	}
	store.On("TransitionExecutionStatus", ctx, "exec-direct-1", approveFromStatuses, "running", (*string)(nil)).Return(updated, nil)
	// No GetPurchasePlan / GetPlanAccounts / UpdatePurchasePlan calls — see AssertNotCalled below.
	// No recommendations, so nothing is bought and no "purchase confirmed"
	// email goes out (#339): with no SendPurchaseConfirmation expectation
	// registered, a send fails the test.
	store.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)

	_, _, err := manager.ApproveAndExecute(ctx, "exec-direct-1", "operator@example.com", nil, nil)
	require.NoError(t, err)

	// Crucially, the empty PlanID must never reach the UUID-typed store columns.
	store.AssertNotCalled(t, "GetPurchasePlan", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "UpdatePurchasePlan", mock.Anything, mock.Anything)
	// GetPlanAccounts uses the Fn-override pattern in MockConfigStore; leaving
	// GetPlanAccountsFn nil means the production code path is the one under
	// test. The early-return on empty PlanID skips the call entirely.
	assert.Nil(t, store.GetPlanAccountsFn, "test sanity: empty-PlanID branch must not depend on GetPlanAccounts being stubbed")

	// Status reaches a terminal state — completed because no recs failed.
	assert.Equal(t, "completed", updated.Status)
	require.NotNil(t, updated.ApprovedBy)
	assert.Equal(t, "operator@example.com", *updated.ApprovedBy)
	store.AssertExpectations(t)
	sender.AssertExpectations(t)
}

func TestManager_ApproveAndExecute_SkipsTokenCheck(t *testing.T) {
	// Session-authed path: ApproveAndExecute is called directly without a
	// token, after the caller has run RBAC. Verifies the entry point works
	// independently of the token branch.
	//
	// Also the manager-level regression guard for issue #1009: a non-nil
	// transitionedBy (the session user's UUID) must be threaded into
	// TransitionExecutionStatus so transitioned_by is stamped for human
	// session approvals. Pre-fix ApproveAndExecute always passed nil here.
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	actorUUID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-456",
		PlanID:          "plan-789",
		Status:          "approved",
		ApprovalToken:   "tok",
		StepNumber:      1,
		Recommendations: approvalTestRecs(),
	}
	store.On("TransitionExecutionStatus", ctx, "exec-456", approveFromStatuses, "running",
		mock.MatchedBy(func(actor *string) bool { return actor != nil && *actor == actorUUID })).Return(updated, nil)
	stubExecuteChain(t, store, sender, "plan-789")

	_, _, err := manager.ApproveAndExecute(ctx, "exec-456", "session-user@example.com", &actorUUID, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.ApprovedBy)
	assert.Equal(t, "session-user@example.com", *updated.ApprovedBy)
	store.AssertExpectations(t)
	sender.AssertExpectations(t)
}

// TestManager_RunPlannedPurchaseNow_ExecutesFromPaused pins issue #218:
// "Run now" on a scheduled purchase must actually execute it, not just flip
// its status. Pre-fix, the handler CASed the row straight to "running" with
// no executor consuming that state -- the scheduler only picks up
// pending/notified, so the purchase was never bought and the reaper
// eventually failed the row out from under the operator who was told it
// ran. This drives RunPlannedPurchaseNow from "paused" (a source
// ApproveAndExecute itself does not accept) and asserts the full
// executeAndFinalize chain runs: the plan lookup, the confirmation email,
// SavePurchaseExecution, and CompletePlanStep all fire, proving the
// purchase was actually executed rather than left stuck in "running".
func TestManager_RunPlannedPurchaseNow_ExecutesFromPaused(t *testing.T) {
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-run-now",
		PlanID:          "plan-run-now",
		Status:          "approved",
		StepNumber:      1,
		Recommendations: approvalTestRecs(),
	}
	store.On("TransitionExecutionStatus", ctx, "exec-run-now", []string{"pending", "notified", "paused"}, "running", (*string)(nil)).Return(updated, nil)
	stubExecuteChain(t, store, sender, "plan-run-now")

	final, revocationToken, err := manager.RunPlannedPurchaseNow(ctx, "exec-run-now", "operator@example.com", nil, nil)
	require.NoError(t, err)
	// Run-now shares the execute funnel's revocation-token rotation (#103).
	require.NotEmpty(t, revocationToken)
	assert.Equal(t, config.HashApprovalToken(revocationToken), updated.ApprovalToken)
	assert.NotEqual(t, revocationToken, updated.ApprovalToken)
	require.NotNil(t, updated.ApprovedBy)
	assert.Equal(t, "operator@example.com", *updated.ApprovedBy)
	assert.Same(t, updated, final)
	assert.Equal(t, "completed", final.Status)
	store.AssertExpectations(t)
	sender.AssertExpectations(t)
}

// TestManager_RunPlannedPurchaseNow_LostCASReturnsError proves a lost CAS
// (row already claimed -- by a concurrent "Run now" click, or the scheduler
// racing the same pending row) surfaces as an error rather than silently
// reporting success, and that no execution work runs on that path (no
// unexpected mock calls -- store has no execute-chain stubs registered).
// This is the idempotency half of issue #218's "no double purchase" ask.
func TestManager_RunPlannedPurchaseNow_LostCASReturnsError(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	store.On("TransitionExecutionStatus", ctx, "exec-race", []string{"pending", "notified", "paused"}, "running", (*string)(nil)).
		Return(nil, config.ErrExecutionNotInExpectedStatus)

	final, _, err := manager.RunPlannedPurchaseNow(ctx, "exec-race", "operator@example.com", nil, nil)
	require.Error(t, err)
	assert.Nil(t, final)
	assert.ErrorIs(t, err, config.ErrExecutionNotInExpectedStatus)
	store.AssertExpectations(t)
}

// TestManager_DirectExecute_StampsAuditFieldsAfterClaim pins #532: the
// executed_* audit fields land on the row the claim returned, together with
// the skip reason, and the row comes back to the caller.
func TestManager_DirectExecute_StampsAuditFieldsAfterClaim(t *testing.T) {
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	actorUUID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-direct-stamp",
		PlanID:          "plan-direct-stamp",
		Status:          "approved",
		StepNumber:      1,
		Recommendations: approvalTestRecs(),
	}
	store.On("TransitionExecutionStatus", ctx, "exec-direct-stamp", approveFromStatuses, "running",
		mock.MatchedBy(func(actor *string) bool { return actor != nil && *actor == actorUUID })).Return(updated, nil)
	stubExecuteChain(t, store, sender, "plan-direct-stamp")

	final, _, err := manager.DirectExecute(ctx, "exec-direct-stamp", "session-user@example.com", &actorUUID, nil)
	require.NoError(t, err)
	assert.Same(t, updated, final)
	require.NotNil(t, final.ExecutedAt)
	require.NotNil(t, final.ExecutedByUserID)
	assert.Equal(t, actorUUID, *final.ExecutedByUserID)
	require.NotNil(t, final.PreApprovalSkipReason)
	assert.Equal(t, directExecuteSkipReason, *final.PreApprovalSkipReason)
}

// TestManager_DirectExecute_RefusedLeavesNoExecutedAt pins #532: a lost claim
// returns no row and saves nothing, so no executed_at can land on a row that
// never ran.
func TestManager_DirectExecute_RefusedLeavesNoExecutedAt(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	store.On("TransitionExecutionStatus", ctx, "exec-direct-lost", approveFromStatuses, "running", (*string)(nil)).
		Return(nil, config.ErrExecutionNotInExpectedStatus)

	final, _, err := manager.DirectExecute(ctx, "exec-direct-lost", "operator@example.com", nil, nil)
	require.ErrorIs(t, err, config.ErrExecutionNotInExpectedStatus)
	assert.Nil(t, final)
	store.AssertNotCalled(t, "SavePurchaseExecution", mock.Anything, mock.Anything)
}

// TestManager_ApproveAndExecute_ReturnsRowOnExecutionFailure pins #532: after
// the claim, an execution failure still returns the row (with its final
// status) so the HTTP layer can tell it from a lost claim.
func TestManager_ApproveAndExecute_ReturnsRowOnExecutionFailure(t *testing.T) {
	ctx := context.Background()
	store := new(MockConfigStore)
	factory := new(MockProviderFactory)
	assertMocksOnCleanup(t, store, factory)
	factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(nil, errors.New("credentials rejected"))
	manager := &Manager{config: store, email: allowFailedNotification(new(MockEmailSender)), providerFactory: factory}

	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-fail-after-claim",
		Status:          "approved",
		Recommendations: approvalTestRecs(),
	}
	store.On("TransitionExecutionStatus", ctx, "exec-fail-after-claim", approveFromStatuses, "running", (*string)(nil)).Return(updated, nil)
	store.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)

	final, token, err := manager.ApproveAndExecute(ctx, "exec-fail-after-claim", "operator@example.com", nil, nil)
	require.Error(t, err)
	assert.Empty(t, token)
	require.NotNil(t, final)
	assert.Equal(t, "failed", final.Status)
}

// TestManager_ApproveExecution_ReturnsRowOnExecutionFailure is the token-path
// twin (issue #532 slice 3): the email-link approve also returns the claimed
// row with its final status, so the handler answers 502, not a bare 500.
func TestManager_ApproveExecution_ReturnsRowOnExecutionFailure(t *testing.T) {
	ctx := context.Background()
	store := new(MockConfigStore)
	factory := new(MockProviderFactory)
	assertMocksOnCleanup(t, store, factory)
	factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(nil, errors.New("credentials rejected"))
	manager := &Manager{config: store, email: allowFailedNotification(new(MockEmailSender)), providerFactory: factory}

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-token-fail-after-claim",
		Status:        "pending",
		ApprovalToken: config.HashApprovalToken("valid-token"),
	}
	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-token-fail-after-claim",
		Status:          "running",
		ApprovalToken:   config.HashApprovalToken("valid-token"),
		Recommendations: approvalTestRecs(),
	}
	store.On("GetExecutionByID", ctx, "exec-token-fail-after-claim").Return(execution, nil)
	store.On("TransitionExecutionStatusTokenPinned", ctx, "exec-token-fail-after-claim", approveFromStatuses, "running", (*string)(nil), "valid-token").Return(updated, nil)
	store.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)

	final, token, err := manager.ApproveExecution(ctx, "exec-token-fail-after-claim", "valid-token", "", nil)
	require.Error(t, err)
	assert.Empty(t, token)
	require.NotNil(t, final)
	assert.Equal(t, "exec-token-fail-after-claim", final.ExecutionID)
	assert.Equal(t, "failed", final.Status)
}

// ─── enforceFourEyesPolicy at the ApproveAndExecute choke point ───────────────
// (issue #1005 / PR #1500 adversarial review)
//
// ApproveAndExecute is the ONE place every approve/execute entry point
// funnels through: approvePurchaseViaSession, directExecutePurchase
// (execute_mode="direct", issue #289), and ApproveExecution (both the
// email-token deep-link caller, which passes an actor email and no
// transitionedBy). The
// tests below exercise the manager-layer gate directly with the exact
// (actor, transitionedBy) shapes each real caller passes, proving the two
// bypasses adversarial review found on PR #1500 -- execute_mode="direct"
// (HIGH) and the token-link approve call shape (MEDIUM) -- are now closed at the
// shared choke point rather than only at the 2 HTTP handler call sites the
// original PR wired requireDifferentApprover into.

func fourEyesCfgOnForManager() *config.GlobalConfig {
	return &config.GlobalConfig{RequireDifferentApprover: true}
}

func fourEyesManagerExec(executionID string, creatorID *string) *config.PurchaseExecution {
	return &config.PurchaseExecution{
		ExecutionID:     executionID,
		PlanID:          "plan-fourEyes",
		Status:          "pending",
		ApprovalToken:   "tok",
		CreatedByUserID: creatorID,
	}
}

// TestManager_ApproveAndExecute_FourEyesOn_DeniesSelfApprove models the
// execute_mode="direct" call shape (issue #289): directExecutePurchase always
// passes the session's own email as actor and its own UUID as transitionedBy,
// and by construction CreatedByUserID is stamped from that same session --
// so self-approval is unconditional on this path. Pre-fix, ApproveAndExecute
// had no 4-eyes awareness at all and this call would have proceeded straight
// to TransitionExecutionStatus and executed the purchase (the HIGH finding).
// The actor's UUID (transitionedBy) is compared directly against
// CreatedByUserID -- no GetUserEmailByID call is needed or expected on this
// tier-1 (UUID) path; see checkDifferentApprover's doc comment.
func TestManager_ApproveAndExecute_FourEyesOn_DeniesSelfApprove(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	creatorID := "user-creator"
	creatorEmail := "creator@example.com"
	execution := fourEyesManagerExec("exec-direct-self", &creatorID)

	store.On("GetGlobalConfig", ctx).Return(fourEyesCfgOnForManager(), nil)
	store.On("GetExecutionByID", ctx, "exec-direct-self").Return(execution, nil)

	_, _, err := manager.ApproveAndExecute(ctx, "exec-direct-self", creatorEmail, &creatorID, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "4-eyes mode requires a different approver")
	store.AssertNotCalled(t, "TransitionExecutionStatus",
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "GetUserEmailByID", mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

// TestManager_ApproveAndExecute_FourEyesOn_AllowsDifferentApprover is the
// positive control for the same call shape: a different actor than the
// creator must still succeed end to end (the fix must not over-broaden into
// blocking legitimate dual-control approvals). Compared by UUID (tier 1); no
// GetUserEmailByID call expected.
func TestManager_ApproveAndExecute_FourEyesOn_AllowsDifferentApprover(t *testing.T) {
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	creatorID := "user-creator"
	approverUUID := "user-approver"
	approverEmail := "approver@example.com"
	execution := fourEyesManagerExec("exec-direct-diff", &creatorID)
	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-direct-diff",
		PlanID:          "plan-fourEyes",
		Status:          "approved",
		StepNumber:      1,
		Recommendations: approvalTestRecs(),
	}

	store.On("GetGlobalConfig", mock.Anything).Return(fourEyesCfgOnForManager(), nil)
	store.On("GetExecutionByID", ctx, "exec-direct-diff").Return(execution, nil)
	store.On("TransitionExecutionStatus", ctx, "exec-direct-diff", approveFromStatuses, "running", &approverUUID).Return(updated, nil)
	stubExecuteChain(t, store, sender, "plan-fourEyes")

	_, _, err := manager.ApproveAndExecute(ctx, "exec-direct-diff", approverEmail, &approverUUID, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.ApprovedBy)
	assert.Equal(t, approverEmail, *updated.ApprovedBy)
	store.AssertNotCalled(t, "GetUserEmailByID", mock.Anything, mock.Anything)
	store.AssertExpectations(t)
	sender.AssertExpectations(t)
}

// TestManager_ApproveAndExecute_FourEyesOn_PerUserAPIKey_DeniesSelfExecute
// closes the exact HIGH gap an independent adversarial review found in an
// earlier version of this fix: a per-user API key session
// (Session.UserAPIKeyID != "") carries a real user UUID as Session.UserID
// but an EMPTY Session.Email -- unlike a normal bearer-token session.
// internal/api's fourEyesActorIdentity falls back to session.UserID (a UUID
// string, not an email) whenever Email is empty; if checkDifferentApprover
// only ever compared emails, that UUID string could never equal the
// creator's real resolved email and self-direct-execute would silently
// succeed. Comparing UUIDs first (tier 1, via transitionedBy) closes this
// regardless of what the email-shaped actor string looks like.
func TestManager_ApproveAndExecute_FourEyesOn_PerUserAPIKey_DeniesSelfExecute(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	creatorID := "11111111-1111-1111-1111-111111111111"
	execution := fourEyesManagerExec("exec-apikey-self", &creatorID)

	store.On("GetGlobalConfig", ctx).Return(fourEyesCfgOnForManager(), nil)
	store.On("GetExecutionByID", ctx, "exec-apikey-self").Return(execution, nil)

	// actor is the UUID string itself (empty Session.Email fallback), exactly
	// what internal/api's fourEyesActorIdentity would pass for a per-user API
	// key session; transitionedBy is the same UUID, populated regardless of
	// Email by validUUIDPtrOrNil(&session.UserID).
	_, _, err := manager.ApproveAndExecute(ctx, "exec-apikey-self", creatorID, &creatorID, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "4-eyes mode requires a different approver")
	store.AssertNotCalled(t, "TransitionExecutionStatus",
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "GetUserEmailByID", mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

// TestManager_ApproveAndExecute_FourEyesOn_NullCreatorDenied: a legacy row
// with no recorded creator cannot satisfy "different from creator" and must
// fail closed, mirroring the handler-level requireDifferentApprover behavior
// for the same case.
func TestManager_ApproveAndExecute_FourEyesOn_NullCreatorDenied(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	execution := fourEyesManagerExec("exec-legacy", nil)
	store.On("GetGlobalConfig", ctx).Return(fourEyesCfgOnForManager(), nil)
	store.On("GetExecutionByID", ctx, "exec-legacy").Return(execution, nil)

	_, _, err := manager.ApproveAndExecute(ctx, "exec-legacy", "someone@example.com", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "predates the dual-control feature")
	store.AssertExpectations(t)
}

// TestManager_ApproveAndExecute_FourEyesOn_EmptyActorDenied covers the
// "actor identity is unknown" fail-closed branch explicitly: mode is on, the
// execution has a recorded creator, but no actor identity was supplied.
func TestManager_ApproveAndExecute_FourEyesOn_EmptyActorDenied(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	creatorID := "user-creator"
	execution := fourEyesManagerExec("exec-no-actor", &creatorID)
	store.On("GetGlobalConfig", ctx).Return(fourEyesCfgOnForManager(), nil)
	store.On("GetExecutionByID", ctx, "exec-no-actor").Return(execution, nil)

	_, _, err := manager.ApproveAndExecute(ctx, "exec-no-actor", "", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no approver identity could be determined")
	store.AssertExpectations(t)
}

// TestManager_ApproveAndExecute_FourEyesOff_AllowsSelfApprove is the
// regression-continuity guard: mode off (the default) must behave exactly
// as before this fix -- the creator may approve/direct-execute their own
// row, and the FOUR-EYES GATE ITSELF performs no extra DB calls (it never
// calls GetUserEmailByID, and never calls GetExecutionByID for identity
// purposes since loadExecutionForFourEyes is skipped when mode is off).
// rotateApprovalToken (called unconditionally after a successful execute,
// issue #103) mutates the `updated` struct already in hand rather than
// re-fetching by ID, so it adds no extra GetExecutionByID call either.
func TestManager_ApproveAndExecute_FourEyesOff_AllowsSelfApprove(t *testing.T) {
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	creatorID := "user-creator"
	updated := &config.PurchaseExecution{ExecutionID: "exec-mode-off", PlanID: "plan-fourEyes", Status: "approved", StepNumber: 1, Recommendations: approvalTestRecs()}
	store.On("TransitionExecutionStatus", ctx, "exec-mode-off", approveFromStatuses, "running", &creatorID).Return(updated, nil)
	stubExecuteChain(t, store, sender, "plan-fourEyes")

	_, _, err := manager.ApproveAndExecute(ctx, "exec-mode-off", "creator@example.com", &creatorID, nil)
	require.NoError(t, err)
	store.AssertNotCalled(t, "GetExecutionByID", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "GetUserEmailByID", mock.Anything, mock.Anything)
	store.AssertExpectations(t)
	sender.AssertExpectations(t)
}
