package purchase

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

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
