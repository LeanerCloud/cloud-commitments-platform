package purchase

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestClearApprovalToken_PersistsEmpty is a unit test for clearApprovalToken
// (the cancel path) that verifies the helper clears both ApprovalToken and
// ApprovalTokenExpiresAt on the persisted row. A canceled execution has no
// valid follow-up action, so emptying the token is correct.
func TestClearApprovalToken_PersistsEmpty(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	future := time.Now().Add(24 * time.Hour)
	execution := &config.PurchaseExecution{
		ExecutionID:            "exec-tok-clear",
		Status:                 "canceled",
		ApprovalToken:          "some-token",
		ApprovalTokenExpiresAt: &future,
	}
	store.On("GetExecutionByID", ctx, "exec-tok-clear").Return(execution, nil)
	// Guarded on "canceled" (issue #835). The mock falls back to
	// SavePurchaseExecution only when no guarded expectation is set, so this
	// expectation is what pins the guard.
	store.On("SaveExecutionIfStatus", ctx, mock.MatchedBy(func(e *config.PurchaseExecution) bool {
		return e.ExecutionID == "exec-tok-clear" && e.ApprovalToken == "" && e.ApprovalTokenExpiresAt == nil
	}), []string{"canceled"}).Return(nil)

	err := manager.clearApprovalToken(ctx, "exec-tok-clear")
	require.NoError(t, err)
	store.AssertExpectations(t)
}

// TestMintRevocationToken_PersistsFreshToken is a unit test for
// rotateApprovalToken that verifies the helper writes a non-empty, changed
// token with a future expiry on the persisted row, mutating the caller's own
// struct in place (no internal fetch) and persisting it in a single save.
func TestMintRevocationToken_PersistsFreshToken(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-mint",
		Status:        "completed",
		ApprovalToken: "old-approval-token",
	}
	// Guarded on the terminal status the row already carries (issue #835).
	store.On("SaveExecutionIfStatus", ctx, mock.MatchedBy(func(e *config.PurchaseExecution) bool {
		// Fresh token must be non-empty, different from old, and expiry set.
		return e.ExecutionID == "exec-mint" &&
			e.ApprovalToken != "" &&
			e.ApprovalToken != "old-approval-token" &&
			e.ApprovalTokenExpiresAt != nil
	}), []string{"completed"}).Return(nil)

	rawToken, err := manager.rotateApprovalToken(ctx, execution, config.RevocationWindow)
	require.NoError(t, err)
	store.AssertExpectations(t)
	// The returned value is the RAW token (issue #103); the in-memory
	// struct's field was mutated to the HASH of a different, freshly
	// generated token, so the two must never be equal.
	assert.NotEmpty(t, rawToken)
	assert.NotEqual(t, rawToken, execution.ApprovalToken)
	assert.Equal(t, config.HashApprovalToken(rawToken), execution.ApprovalToken)
	assert.NotEmpty(t, execution.ApprovalToken)
	assert.NotEqual(t, "old-approval-token", execution.ApprovalToken)
	assert.NotNil(t, execution.ApprovalTokenExpiresAt)
}

// TestManager_ApproveAndExecute_FourEyesOn_SystemCreated pins the #233 rule for
// scheduler-minted rows (identified approver passes, anonymous and creator denied).
func TestManager_ApproveAndExecute_FourEyesOn_SystemCreated(t *testing.T) {
	ctx := context.Background()
	creatorID, approverID := "user-creator", "user-approver"
	gate := func(creator *string, email string, userID *string) error {
		manager, store, _ := newApproveManager(t)
		execution := fourEyesManagerExec("exec-sys", creator)
		execution.CreatedBySystem = true
		store.On("GetGlobalConfig", ctx).Return(fourEyesCfgOnForManager(), nil)
		store.On("GetExecutionByID", ctx, "exec-sys").Return(execution, nil)
		return manager.enforceFourEyesPolicy(ctx, "exec-sys", email, userID)
	}

	require.NoError(t, gate(nil, "approver@example.com", nil))
	require.NoError(t, gate(nil, "", &approverID))
	require.ErrorIs(t, gate(nil, "  ", nil), ErrFourEyesDenied, "anonymous actor")
	require.ErrorIs(t, gate(&creatorID, "", &creatorID), ErrFourEyesDenied, "flag must not override a creator")
}

// TestMaskActor pins the PII mask written to the approval logs: everything
// before the first '@' is hidden, a non-email keeps only its last 4 bytes,
// and an empty actor is "<anon>".
func TestMaskActor(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "<anon>"},
		{"approver@example.com", "***@example.com"},
		{"a@b", "***@b"},
		{"@example.com", "***@example.com"},
		{"trailing@", "***@"},
		{"first@second@example.com", "***@second@example.com"},
		{"josé@example.com", "***@example.com"},
		{"\xff\xfe@example.com", "***@example.com"},
		{"a", "****"},
		{"abcd", "****"},
		{"abcde", "...bcde"},
		{"service-account", "...ount"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, maskActor(tc.in), "maskActor(%q)", tc.in)
	}
}

// TestManager_ApproveExecution_FourEyesOn_ASCIICaseOnly pins that
// the creator check and the approver match agree on which addresses are equal.
func TestManager_ApproveExecution_FourEyesOn_ASCIICaseOnly(t *testing.T) {
	cases := []struct {
		name, creator, actor string
		wantDenied           bool
	}{
		{"same address, different case and padding", "Admin@Example.com", "  admin@example.COM ", true},
		{"non-ASCII letters are not folded", "Mus@Example.com", "mu\u017f@example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager, store, _ := newApproveManager(t)
			creatorID := "user-creator"
			execution := &config.PurchaseExecution{
				ExecutionID:     "exec-cmp",
				PlanID:          "plan-fourEyes",
				Status:          "pending",
				ApprovalToken:   config.HashApprovalToken("valid-token"),
				CreatedByUserID: &creatorID,
			}
			store.On("GetUserEmailByID", ctx, creatorID).Return(tc.creator, nil)

			err := manager.checkDifferentApprover(ctx, "exec-cmp", execution, tc.actor, nil)
			if tc.wantDenied {
				require.ErrorIs(t, err, ErrFourEyesDenied)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestValidateApprovalToken_TTLAndPrecedence pins the TTL guard on the shared
// token check (issue #268) and that a wrong token on an expired row reports
// invalid, not expired, so expiry never leaks token validity.
func TestValidateApprovalToken_TTLAndPrecedence(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	tests := []struct {
		wantErr   error
		expiresAt *time.Time
		name      string
		token     string
	}{
		{name: "expired token is rejected", expiresAt: &past, token: "correct-token", wantErr: ErrApprovalTokenExpired},
		{name: "unexpired token is accepted", expiresAt: &future, token: "correct-token"},
		{name: "legacy row without expiry is accepted", expiresAt: nil, token: "correct-token"},
		{name: "wrong token is rejected", expiresAt: &future, token: "wrong-token", wantErr: ErrInvalidApprovalToken},
		{name: "wrong token on expired row reports invalid, not expired", expiresAt: &past, token: "wrong-token", wantErr: ErrInvalidApprovalToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &config.PurchaseExecution{
				ExecutionID:            "exec-ttl",
				Status:                 "pending",
				ApprovalToken:          config.HashApprovalToken("correct-token"),
				ApprovalTokenExpiresAt: tt.expiresAt,
			}
			err := ValidateApprovalToken(exec, tt.token)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
