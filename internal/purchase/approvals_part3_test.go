package purchase

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestManager_CancelExecution_InvalidToken(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-123",
		PlanID:        "plan-456",
		Status:        "pending",
		ApprovalToken: config.HashApprovalToken("valid-token"),
	}

	mockStore.On("GetExecutionByID", ctx, "exec-123").Return(execution, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.CancelExecution(ctx, "exec-123", "invalid-token", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid approval token")

	mockStore.AssertExpectations(t)
}

func TestManager_CancelExecution_AlreadyCompleted(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-123",
		PlanID:        "plan-456",
		Status:        "completed",
		ApprovalToken: config.HashApprovalToken("valid-token"),
	}

	mockStore.On("GetExecutionByID", ctx, "exec-123").Return(execution, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.CancelExecution(ctx, "exec-123", "valid-token", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "execution cannot be canceled")

	mockStore.AssertExpectations(t)
}

// TestManager_CancelExecution_RejectsNonCancelableStatus is the regression
// guard for issue #645: the token/email cancel path previously rejected only
// completed/canceled, so an email-link holder could cancel an
// approved/running/paused/failed/expired execution that the dashboard
// (session) path refuses. Each non-cancelable status must now be rejected with
// no write to the store — approved/running rows in particular are mid-execution
// and canceling them would desync the DB from the cloud. Only pending and
// notified are cancelable; a scheduled row is revoked, not canceled (issue #73).
func TestManager_CancelExecution_RejectsNonCancelableStatus(t *testing.T) {
	rejected := []string{"approved", "running", "paused", "failed", "expired", "completed", "canceled", "scheduled"}
	for _, status := range rejected {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			mockStore := new(MockConfigStore)
			mockEmail := new(MockEmailSender)

			execution := &config.PurchaseExecution{
				ExecutionID:   "exec-123",
				PlanID:        "plan-456",
				Status:        status,
				ApprovalToken: config.HashApprovalToken("valid-token"),
			}
			mockStore.On("GetExecutionByID", ctx, "exec-123").Return(execution, nil)

			manager := &Manager{
				config:       mockStore,
				email:        mockEmail,
				dashboardURL: "https://dashboard.example.com",
			}

			err := manager.CancelExecution(ctx, "exec-123", "valid-token", status)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "execution cannot be canceled")
			assert.Contains(t, err.Error(), status)
			assert.ErrorIs(t, err, config.ErrExecutionNotInExpectedStatus)
			// A rejected row was never raced, so the message must not claim it.
			assert.NotContains(t, err.Error(), "concurrent operation")
			if status == "scheduled" {
				assert.Contains(t, err.Error(), "use the revoke endpoint before it fires")
			}
			// Status guard fires before the atomic UPDATE — a rejected
			// cancel must never reach CancelExecutionAtomicTokenPinned.
			mockStore.AssertNotCalled(t, "CancelExecutionAtomicTokenPinned", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			mockStore.AssertExpectations(t)
		})
	}
}

// TestManager_CancelExecution_AllowsCancelableStatus confirms the inverse of
// the #645 guard: pending and notified rows are cancelable on
// the token path, and the cancel commits (status flip + suppression cleanup)
// in a single tx. Without this the alignment fix could silently over-restrict
// and break the legitimate email-link cancel of a row awaiting approval.
func TestManager_CancelExecution_AllowsCancelableStatus(t *testing.T) {
	allowed := []string{"pending", "notified"}
	for _, status := range allowed {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			mockStore := new(MockConfigStore)
			mockEmail := new(MockEmailSender)

			execution := &config.PurchaseExecution{
				ExecutionID:   "exec-123",
				PlanID:        "plan-456",
				Status:        status,
				ApprovalToken: config.HashApprovalToken("valid-token"),
			}
			mockStore.On("GetExecutionByID", ctx, "exec-123").Return(execution, nil)
			// CancelExecutionAtomicTokenPinned is called inside WithTx (nil tx sentinel in tests).
			mockStore.On("CancelExecutionAtomicTokenPinned", ctx, mock.Anything, "exec-123", (*string)(nil), "valid-token").
				Return(true, "canceled", nil)
			// Suppression cleanup must follow a successful atomic cancel.
			mockStore.On("DeleteSuppressionsByExecutionTx", ctx, mock.Anything, "exec-123").
				Return(nil)
			// Token rotation after successful cancel.
			mockStore.On("SavePurchaseExecution", ctx, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)

			manager := &Manager{
				config:       mockStore,
				email:        mockEmail,
				dashboardURL: "https://dashboard.example.com",
			}

			err := manager.CancelExecution(ctx, "exec-123", "valid-token", "")
			require.NoError(t, err)
			mockStore.AssertCalled(t, "CancelExecutionAtomicTokenPinned", ctx, mock.Anything, "exec-123", (*string)(nil), "valid-token")
			mockStore.AssertCalled(t, "DeleteSuppressionsByExecutionTx", ctx, mock.Anything, "exec-123")
			mockStore.AssertExpectations(t)
		})
	}
}

func TestManager_CancelExecution_NotFound(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	mockStore.On("GetExecutionByID", ctx, "exec-123").Return(nil, fmt.Errorf("%w: execution exec-123", config.ErrNotFound))

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.CancelExecution(ctx, "exec-123", "token", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "execution not found")

	mockStore.AssertExpectations(t)
}

func TestManager_CancelExecution_GetError(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	mockStore.On("GetExecutionByID", ctx, "exec-123").Return(nil, errors.New("database error"))

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.CancelExecution(ctx, "exec-123", "token", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get execution")

	mockStore.AssertExpectations(t)
}

// TestManager_ApproveExecution_FourEyesOn_TokenActorEqualsCreator_Denied models
// the token-link approve call shape (MEDIUM finding): ApproveExecution
// always passes transitionedBy=nil, and the only identity signal is the
// verified actor email. Pre-fix, ApproveExecution/ApproveAndExecute had no
// awareness of 4-eyes mode at all, so a replayed or forwarded token+actor
// matching the creator's own email would approve the row despite mode being
// on.
//
// The case-differing rows (issue #307) pin the case-insensitive comparison:
// email addresses are matched case-insensitively everywhere else in the
// approval flow, so a creator whose stored email differs from the actor's
// only in letter case is the same person and must still be denied. A
// regression that swaps strings.EqualFold for == fails those rows.
func TestManager_ApproveExecution_FourEyesOn_TokenActorEqualsCreator_Denied(t *testing.T) {
	cases := []struct {
		name         string
		creatorEmail string
		actorEmail   string
	}{
		{name: "byte-identical emails", creatorEmail: "creator@example.com", actorEmail: "creator@example.com"},
		{name: "creator stored mixed case", creatorEmail: "Creator@Example.COM", actorEmail: "creator@example.com"},
		{name: "actor mixed case", creatorEmail: "creator@example.com", actorEmail: "CREATOR@Example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager, store, _ := newApproveManager(t)
			// Fail this row on an unexpected call instead of panicking, so
			// one regressed row doesn't abort the remaining rows.
			store.Test(t)

			creatorID := "user-creator"
			execution := &config.PurchaseExecution{
				ExecutionID:     "exec-token-self",
				PlanID:          "plan-fourEyes",
				Status:          "pending",
				ApprovalToken:   config.HashApprovalToken("valid-token"),
				CreatedByUserID: &creatorID,
			}

			store.On("GetExecutionByID", ctx, "exec-token-self").Return(execution, nil)
			store.On("GetGlobalConfig", ctx).Return(fourEyesCfgOnForManager(), nil)
			store.On("GetUserEmailByID", ctx, creatorID).Return(tc.creatorEmail, nil)

			_, _, err := manager.ApproveExecution(ctx, "exec-token-self", "valid-token", tc.actorEmail, nil)
			require.ErrorIs(t, err, ErrFourEyesDenied)
			assert.Contains(t, err.Error(), "4-eyes mode requires a different approver")
			store.AssertNotCalled(t, "TransitionExecutionStatusTokenPinned",
				mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			store.AssertExpectations(t)
		})
	}
}

// TestManager_ApproveExecution_FourEyesOn_DifferentActor_Allowed is the
// positive control for the token-link call shape: an actor that resolves to
// a different email than the creator must still succeed.
func TestManager_ApproveExecution_FourEyesOn_DifferentActor_Allowed(t *testing.T) {
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	creatorID := "user-creator"
	execution := &config.PurchaseExecution{
		ExecutionID:     "exec-token-diff",
		PlanID:          "plan-fourEyes",
		Status:          "pending",
		ApprovalToken:   config.HashApprovalToken("valid-token"),
		CreatedByUserID: &creatorID,
	}
	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-token-diff",
		PlanID:          "plan-fourEyes",
		Status:          "approved",
		ApprovalToken:   config.HashApprovalToken("valid-token"),
		StepNumber:      1,
		Recommendations: approvalTestRecs(),
	}

	store.On("GetExecutionByID", ctx, "exec-token-diff").Return(execution, nil)
	store.On("GetGlobalConfig", mock.Anything).Return(fourEyesCfgOnForManager(), nil)
	store.On("GetUserEmailByID", ctx, creatorID).Return("creator@example.com", nil)
	store.On("TransitionExecutionStatusTokenPinned", ctx, "exec-token-diff", approveFromStatuses, "running", (*string)(nil), "valid-token").Return(updated, nil)
	stubExecuteChain(t, store, sender, "plan-fourEyes")

	_, _, err := manager.ApproveExecution(ctx, "exec-token-diff", "valid-token", "approver@example.com", nil)
	require.NoError(t, err)
	require.NotNil(t, updated.ApprovedBy)
	assert.Equal(t, "approver@example.com", *updated.ApprovedBy)
	store.AssertExpectations(t)
	sender.AssertExpectations(t)
}

// TestManager_ApproveExecution_FourEyesOn_UnresolvableCreator_Denied is the
// A16-001 regression guard (#256). checkDifferentApprover's tier-2 (email)
// path resolves the creator's email via GetUserEmailByID and must fail
// closed when that lookup cannot identify the creator -- either because the
// creator's row was deleted (Postgres's GetUserEmailByID returns ("", nil)
// on pgx.ErrNoRows, per internal/config/store_postgres.go) or because the
// lookup itself errors. Neither branch had a test: every stub in this
// package returned a real address, so a refactor that dropped or inverted
// the `creatorEmail == ""` check would silently let a forwarded token from
// the original requester clear dual control for any execution whose creator
// was deleted, without failing a single existing test.
func TestManager_ApproveExecution_FourEyesOn_UnresolvableCreator_Denied(t *testing.T) {
	creatorID := "user-creator-deleted"
	baseExecution := func() *config.PurchaseExecution {
		return &config.PurchaseExecution{
			ExecutionID:     "exec-token-unresolvable",
			PlanID:          "plan-fourEyes",
			Status:          "pending",
			ApprovalToken:   config.HashApprovalToken("valid-token"),
			CreatedByUserID: &creatorID,
		}
	}

	t.Run("GetUserEmailByID returns empty address (deleted creator row)", func(t *testing.T) {
		ctx := context.Background()
		manager, store, _ := newApproveManager(t)

		store.On("GetExecutionByID", ctx, "exec-token-unresolvable").Return(baseExecution(), nil)
		store.On("GetGlobalConfig", ctx).Return(fourEyesCfgOnForManager(), nil)
		// Mirrors the real Postgres store's ("", nil) result for
		// pgx.ErrNoRows -- a deleted/nonexistent creator user row.
		store.On("GetUserEmailByID", ctx, creatorID).Return("", nil)

		_, _, err := manager.ApproveExecution(ctx, "exec-token-unresolvable", "valid-token", "approver@example.com", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "creator's account could not be resolved")
		store.AssertNotCalled(t, "TransitionExecutionStatusTokenPinned",
			mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		store.AssertExpectations(t)
	})

	t.Run("GetUserEmailByID errors", func(t *testing.T) {
		ctx := context.Background()
		manager, store, _ := newApproveManager(t)
		lookupErr := errors.New("connection reset by peer")

		store.On("GetExecutionByID", ctx, "exec-token-unresolvable").Return(baseExecution(), nil)
		store.On("GetGlobalConfig", ctx).Return(fourEyesCfgOnForManager(), nil)
		store.On("GetUserEmailByID", ctx, creatorID).Return("", lookupErr)

		_, _, err := manager.ApproveExecution(ctx, "exec-token-unresolvable", "valid-token", "approver@example.com", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to resolve creator identity")
		store.AssertNotCalled(t, "TransitionExecutionStatusTokenPinned",
			mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		store.AssertExpectations(t)
	})
}

func TestManager_CancelExecution(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-123",
		PlanID:        "plan-456",
		Status:        "pending",
		ApprovalToken: config.HashApprovalToken("valid-token"),
	}

	mockStore.On("GetExecutionByID", ctx, "exec-123").Return(execution, nil)
	// WithTx passes nil as the tx sentinel in tests; empty actor -> nil cancelledBy.
	mockStore.On("CancelExecutionAtomicTokenPinned", ctx, mock.Anything, "exec-123", (*string)(nil), "valid-token").
		Return(true, "canceled", nil)
	mockStore.On("DeleteSuppressionsByExecutionTx", ctx, mock.Anything, "exec-123").
		Return(nil)
	// Token rotation: SavePurchaseExecution is called after a successful cancel.
	mockStore.On("SavePurchaseExecution", ctx, mock.MatchedBy(func(e *config.PurchaseExecution) bool {
		return e.ExecutionID == "exec-123" && e.ApprovalToken == ""
	})).Return(nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.CancelExecution(ctx, "exec-123", "valid-token", "")
	require.NoError(t, err)

	mockStore.AssertExpectations(t)
}
