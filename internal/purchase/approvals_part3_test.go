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
