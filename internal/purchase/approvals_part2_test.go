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

// --- Regression tests for issue #609 (orphan-account guard, ApproveExecution path) ---

// TestManager_ApproveExecution_NonAWSOrphanReturnsError is the regression
// guard for issue #609 on the token-authenticated ApproveExecution path (used
// by the token-link and legacy email-link flows). An execution
// whose CloudAccountID is nil and whose provider is non-AWS must return a
// descriptive error before the execute chain is entered, instead of surfacing
// an opaque cloud-SDK credential error at runtime.
func TestManager_ApproveExecution_NonAWSOrphanReturnsError(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-orphan",
		Status:        "pending",
		ApprovalToken: config.HashApprovalToken("valid-token"),
		// CloudAccountID nil — account was deleted.
		Recommendations: []config.RecommendationRecord{{ID: "r1", Provider: "azure"}},
	}
	store.On("GetExecutionByID", ctx, "exec-orphan").Return(execution, nil)

	_, _, err := manager.ApproveExecution(ctx, "exec-orphan", "valid-token", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer exists")
	assert.Contains(t, err.Error(), "azure")
	// Execute chain must not run after the orphan guard fires.
	store.AssertNotCalled(t, "TransitionExecutionStatusTokenPinned", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

// TestManager_ApproveExecution_AWSOrphanFallsThrough confirms the AWS
// ambient-fallback carve-out: nil CloudAccountID + AWS provider must NOT be
// blocked by the issue-#609 guard. The execution proceeds normally.
func TestManager_ApproveExecution_AWSOrphanFallsThrough(t *testing.T) {
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-aws-ambient",
		PlanID:        "plan-aws",
		Status:        "pending",
		ApprovalToken: config.HashApprovalToken("valid-token"),
		// CloudAccountID nil but provider is AWS — ambient fallback applies.
		Recommendations: []config.RecommendationRecord{{ID: "r1", Provider: "aws"}},
	}
	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-aws-ambient",
		PlanID:          "plan-aws",
		Status:          "approved",
		ApprovalToken:   config.HashApprovalToken("valid-token"),
		StepNumber:      1,
		Recommendations: approvalTestRecs(),
	}
	store.On("GetExecutionByID", ctx, "exec-aws-ambient").Return(execution, nil)
	store.On("TransitionExecutionStatusTokenPinned", ctx, "exec-aws-ambient", approveFromStatuses, "running", (*string)(nil), "valid-token").Return(updated, nil)
	stubExecuteChain(t, store, sender, "plan-aws")

	_, _, err := manager.ApproveExecution(ctx, "exec-aws-ambient", "valid-token", "", nil)
	require.NoError(t, err)
	store.AssertExpectations(t)
	sender.AssertExpectations(t)
}

// TestOrphanExecutionError_RecLevelAccountIDPreventsOrphan is the regression
// guard for CR pass-1 actionable 2: an execution with a nil execution-level
// CloudAccountID and a non-AWS provider must NOT be treated as an orphan when
// at least one recommendation carries its own non-nil CloudAccountID. The
// multi-rec fan-out path stores account affinity at the rec level and leaves
// the execution-level field nil.
func TestOrphanExecutionError_RecLevelAccountIDPreventsOrphan(t *testing.T) {
	accountID := "acct-azure-multi-001"
	execution := &config.PurchaseExecution{
		ExecutionID: "exec-multi-rec",
		// Execution-level CloudAccountID is nil — fan-out shape.
		Recommendations: []config.RecommendationRecord{
			{ID: "r1", Provider: "azure", CloudAccountID: &accountID},
			{ID: "r2", Provider: "azure", CloudAccountID: &accountID},
		},
	}
	// Despite execution-level nil + non-AWS provider, the rec-level account
	// ID means the execution is NOT orphaned. OrphanExecutionError must return nil.
	err := OrphanExecutionError(execution)
	assert.NoError(t, err, "rec-level CloudAccountID must prevent orphan classification")
}

// TestManager_CancelExecution_RaceWithApprove is the regression guard for
// issue #671: when a concurrent approve wins the race and transitions the
// execution to 'approved' before the cancel's conditional UPDATE runs,
// CancelExecutionAtomicTokenPinned returns (false, "approved", nil). CancelExecution
// must surface a clean error containing the racing status rather than
// silently overwriting the approved row.
func TestManager_CancelExecution_RaceWithApprove(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-raced",
		Status:        "pending", // status at load time
		ApprovalToken: config.HashApprovalToken("valid-token"),
	}
	store.On("GetExecutionByID", ctx, "exec-raced").Return(execution, nil)
	// Simulate: concurrent approve won between our IsCancelable check and
	// the atomic UPDATE. The DB row is now 'approved' so zero rows affected.
	store.On("CancelExecutionAtomicTokenPinned", ctx, mock.Anything, "exec-raced", (*string)(nil), "valid-token").
		Return(false, "approved", nil)

	err := manager.CancelExecution(ctx, "exec-raced", "valid-token", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "approved", "error must surface the racing status")
	assert.Contains(t, err.Error(), "concurrent", "error must mention the concurrent operation")
	// The approve is already in flight — the DB row must not have been
	// overwritten by the cancel. AssertExpectations verifies that
	// CancelExecutionAtomicTokenPinned was called (confirming the guard reached the DB)
	// and that no further writes landed.
	store.AssertExpectations(t)
	// When the CAS misses, suppression cleanup must never fire.
	store.AssertNotCalled(t, "DeleteSuppressionsByExecutionTx", mock.Anything, mock.Anything, mock.Anything)
}

// TestManager_CancelExecution_ExpiredToken is the cancel-path regression
// guard for issue #397.
func TestManager_CancelExecution_ExpiredToken(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	past := time.Now().Add(-2 * 24 * time.Hour)
	execution := &config.PurchaseExecution{
		ExecutionID:            "exec-cancel-expired",
		Status:                 "pending",
		ApprovalToken:          config.HashApprovalToken("valid-token"),
		ApprovalTokenExpiresAt: &past,
	}
	store.On("GetExecutionByID", ctx, "exec-cancel-expired").Return(execution, nil)

	err := manager.CancelExecution(ctx, "exec-cancel-expired", "valid-token", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
	store.AssertNotCalled(t, "CancelExecutionAtomicTokenPinned", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// Finding #1: Token rotation on approve/cancel
// ---------------------------------------------------------------------------

// TestApproveExecution_MintsRevocationToken verifies that after a successful
// approve a FRESH revocation token is minted (not cleared), so the consumed
// approval token can no longer trigger a revoke while the executed-notification
// email carries a valid revoke-capable token.
func TestApproveExecution_MintsRevocationToken(t *testing.T) {
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	execution := &config.PurchaseExecution{
		ExecutionID:   "exec-rotate",
		PlanID:        "plan-rotate",
		Status:        "pending",
		ApprovalToken: config.HashApprovalToken("pre-rotate-token"),
	}
	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-rotate",
		PlanID:          "plan-rotate",
		Status:          "approved",
		ApprovalToken:   config.HashApprovalToken("pre-rotate-token"),
		StepNumber:      1,
		Recommendations: approvalTestRecs(),
	}

	// GetExecutionByID is called once, in ApproveExecution itself, to
	// validate the supplied token against `execution`. rotateApprovalToken
	// (the rotation step) mutates `updated` -- the row TransitionExecutionStatus
	// returned and executeAndFinalize continued operating on -- in place; it
	// does no fetch of its own (see rotateApprovalToken's doc comment for why).
	store.On("GetExecutionByID", ctx, "exec-rotate").Return(execution, nil)
	store.On("TransitionExecutionStatusTokenPinned", ctx, "exec-rotate", approveFromStatuses, "running", (*string)(nil), "pre-rotate-token").Return(updated, nil)
	stubExecuteChain(t, store, sender, "plan-rotate")

	_, _, err := manager.ApproveExecution(ctx, "exec-rotate", "pre-rotate-token", "", nil)
	require.NoError(t, err)

	// After approve, rotateApprovalToken must have stamped a fresh token onto
	// `updated` (the post-transition row, NOT the pre-approve `execution`
	// struct, which is never touched again once its token has validated).
	// The old approval token's hash must be gone (proving the rotation
	// happened), and the expiry must be set to the 24-hour revocation window.
	// stubExecuteChain registers SavePurchaseExecution with AnythingOfType
	// so it absorbs both the finalize write and the rotateApprovalToken write.
	assert.NotEmpty(t, updated.ApprovalToken,
		"a fresh revocation token must be minted after successful approve (not cleared)")
	assert.NotEqual(t, config.HashApprovalToken("pre-rotate-token"), updated.ApprovalToken,
		"fresh revocation token must differ from the consumed approval token")
	assert.NotNil(t, updated.ApprovalTokenExpiresAt,
		"revocation token expiry must be set to the 24-hour window")
	// The pre-approve struct is untouched: it is a separate object from
	// `updated` and rotateApprovalToken never receives it.
	assert.Equal(t, config.HashApprovalToken("pre-rotate-token"), execution.ApprovalToken)
}

// --- Regression tests for issue #397 (token TTL enforcement) ---

// TestManager_ApproveExecution_ExpiredToken is the regression guard for
// issue #397: an approval token whose ApprovalTokenExpiresAt deadline has
// passed must be rejected with "expired", not silently accepted. This
// prevents a phished or log-leaked token from authorizing a purchase weeks
// after the approval window closed.
func TestManager_ApproveExecution_ExpiredToken(t *testing.T) {
	ctx := context.Background()
	manager, store, _ := newApproveManager(t)

	past := time.Now().Add(-1 * time.Hour) // expired 1 hour ago
	execution := &config.PurchaseExecution{
		ExecutionID:            "exec-expired",
		Status:                 "pending",
		ApprovalToken:          config.HashApprovalToken("valid-token"),
		ApprovalTokenExpiresAt: &past,
	}
	store.On("GetExecutionByID", ctx, "exec-expired").Return(execution, nil)

	_, _, err := manager.ApproveExecution(ctx, "exec-expired", "valid-token", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
	// Token validation passes but the expiry check fires — TransitionExecutionStatus
	// must never be called on an expired token.
	store.AssertNotCalled(t, "TransitionExecutionStatusTokenPinned", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

// TestManager_ApproveExecution_ValidTokenWithinTTL confirms that a
// non-expired token still works (regression guard for the inverse case).
func TestManager_ApproveExecution_ValidTokenWithinTTL(t *testing.T) {
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	future := time.Now().Add(7 * 24 * time.Hour)
	execution := &config.PurchaseExecution{
		ExecutionID:            "exec-live",
		PlanID:                 "plan-live",
		Status:                 "pending",
		ApprovalToken:          config.HashApprovalToken("valid-token"),
		ApprovalTokenExpiresAt: &future,
	}
	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-live",
		PlanID:          "plan-live",
		Status:          "approved",
		ApprovalToken:   config.HashApprovalToken("valid-token"),
		StepNumber:      1,
		Recommendations: approvalTestRecs(),
	}
	store.On("GetExecutionByID", ctx, "exec-live").Return(execution, nil)
	store.On("TransitionExecutionStatusTokenPinned", ctx, "exec-live", approveFromStatuses, "running", (*string)(nil), "valid-token").Return(updated, nil)
	stubExecuteChain(t, store, sender, "plan-live")

	_, _, err := manager.ApproveExecution(ctx, "exec-live", "valid-token", "", nil)
	require.NoError(t, err)
	store.AssertExpectations(t)
}

// TestManager_ApproveExecution_NilExpiresAt_LegacyRow verifies backward
// compatibility: rows created before migration 000051 have a nil
// ApprovalTokenExpiresAt and must not be rejected (issue #397 explicitly
// carves out legacy rows for compatibility).
func TestManager_ApproveExecution_NilExpiresAt_LegacyRow(t *testing.T) {
	ctx := context.Background()
	manager, store, sender := newApproveManager(t)

	execution := &config.PurchaseExecution{
		ExecutionID:            "exec-legacy",
		PlanID:                 "plan-legacy",
		Status:                 "pending",
		ApprovalToken:          config.HashApprovalToken("valid-token"),
		ApprovalTokenExpiresAt: nil, // pre-migration row
	}
	updated := &config.PurchaseExecution{
		ExecutionID:     "exec-legacy",
		PlanID:          "plan-legacy",
		Status:          "approved",
		StepNumber:      1,
		Recommendations: approvalTestRecs(),
	}
	store.On("GetExecutionByID", ctx, "exec-legacy").Return(execution, nil)
	store.On("TransitionExecutionStatusTokenPinned", ctx, "exec-legacy", approveFromStatuses, "running", (*string)(nil), "valid-token").Return(updated, nil)
	stubExecuteChain(t, store, sender, "plan-legacy")

	_, _, err := manager.ApproveExecution(ctx, "exec-legacy", "valid-token", "", nil)
	require.NoError(t, err)
	store.AssertExpectations(t)
}
