package purchase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// CancelExecution cancels a pending execution. actor carries the email of
// the session-authenticated user who clicked cancel; verified by the
// caller (HTTP path: authorizeApprovalAction) before reaching here. Same empty-actor
// rationale as ApproveExecution.
//
// Concurrency: CancelExecutionAtomicTokenPinned uses a conditional UPDATE
// WHERE status IN ('pending','notified') so a concurrent approve that wins
// the race causes zero rows to be affected and the caller receives a
// clean error with the current status rather than silently overwriting
// the approved row. The same UPDATE also requires the stored token hash to
// still match token and be unexpired: loadCancelableExecution validated it
// against a snapshot, and a re-notification that rotated the token since
// must not let the old one cancel (issue #1190). That refusal returns an
// error wrapping config.ErrApprovalTokenChanged.
func (m *Manager) CancelExecution(ctx context.Context, executionID, token, actor string) error {
	logging.Infof("Canceling execution: %s", executionID)

	if _, err := m.loadCancelableExecution(ctx, executionID, token); err != nil {
		return err
	}

	// Build the nullable canceled_by pointer — see ApproveExecution for
	// the nil-vs-empty-string rationale.
	var cancelledBy *string
	if actor != "" {
		a := actor
		cancelledBy = &a
	}

	// Atomic conditional UPDATE + suppression cleanup in one transaction.
	// CancelExecutionAtomicTokenPinned flips status only when status IN
	// ('pending','notified') and the stored token still matches, so a
	// concurrent approve that has already transitioned the row causes zero
	// rows affected and we surface a 409, and a rotated token is refused.
	var canceled bool
	var currentStatus string
	if err := m.config.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		canceled, currentStatus, err = m.config.CancelExecutionAtomicTokenPinned(ctx, tx, executionID, cancelledBy, token)
		if err != nil {
			return err
		}
		if !canceled {
			// Row already transitioned (concurrent approve/cancel won the
			// race). Return early without touching suppressions — the other
			// operation owns the execution state now.
			return nil
		}
		return m.config.DeleteSuppressionsByExecutionTx(ctx, tx, executionID)
	}); err != nil {
		return fmt.Errorf("failed to cancel execution: %w", err)
	}

	if !canceled {
		return fmt.Errorf("%w: execution %s cannot be canceled: concurrent operation already transitioned it to %q", config.ErrExecutionNotInExpectedStatus, executionID, currentStatus)
	}

	logging.Infof("Execution %s canceled", executionID)
	// Token clear (best-effort): a canceled execution has no valid follow-up
	// action, so the approval token is cleared to prevent replay. Unlike the
	// approve path (which mints a fresh revocation token), cancel produces no
	// usable successor action, so an empty token is correct here.
	if rotateErr := m.clearApprovalToken(ctx, executionID); rotateErr != nil {
		logging.Warnf("purchase[%s]: CancelExecution: token clear failed (best-effort): %v", executionID, rotateErr)
	}
	return nil
}

// rotateApprovalToken generates a fresh cryptographically-secure token,
// mutates exec in place to hold its SHA-256 hash and a new ttl-based expiry
// (issue #103: only the token hash is stored, so only the digest is ever
// written to the DB), persists exec with a single SavePurchaseExecution
// call, and returns the RAW token for the caller to embed in an email.
//
// exec MUST already be the caller's current, freshly-loaded copy of the row
// -- this function does no fetch of its own. That is deliberate: an internal
// fetch-by-ID would allocate a SEPARATE struct from whatever the caller is
// already holding, and mutating that separate copy while the caller
// independently re-saves ITS OWN (still-stale) copy afterwards would silently
// overwrite this function's hash with the pre-rotation one -- exactly the
// class of two-writers-one-row bug this signature is designed to make
// impossible by construction.
//
// Its caller is transitionApproveAndExecute (ttl=RevocationWindow), after
// every successful execute, passing `updated` (mutated in place by the
// preceding TransitionExecutionStatus / executeAndFinalize calls), so the
// "purchase executed" email carries a valid revoke-capable token rather than
// the consumed or unrecoverable approval token. The pre-approval
// notification path rotates via RotatePendingApprovalToken instead, because
// it persists only after the email is sent and must not upsert a stale row.
//
// Best-effort: if the write fails the caller's own state change (the
// approve) has already landed, so this returns ("", err) and the caller only logs -- the
// email-link convenience is degraded for this row, not the underlying
// purchase state.
func (m *Manager) rotateApprovalToken(ctx context.Context, exec *config.PurchaseExecution, ttl time.Duration) (string, error) {
	if exec == nil {
		return "", fmt.Errorf("rotateApprovalToken: execution is nil")
	}
	tok, err := common.GenerateApprovalToken()
	if err != nil {
		return "", fmt.Errorf("rotateApprovalToken: failed to generate token: %w", err)
	}
	expiry := time.Now().Add(ttl)
	exec.ApprovalToken = config.HashApprovalToken(tok)
	exec.ApprovalTokenExpiresAt = &expiry
	// Guarded on the terminal status the executor just wrote, so this
	// full-row write cannot put back a status another writer set since
	// (issue #835).
	if saveErr := m.config.SaveExecutionIfStatus(ctx, exec, []string{exec.Status}); saveErr != nil {
		return "", saveErr
	}
	return tok, nil
}

// clearApprovalToken clears the ApprovalToken on the execution after a
// successful cancel. A canceled execution has no valid follow-up action, so
// clearing the token prevents replay for any purpose. Unlike the approve path
// (which mints a fresh revocation token for the revoke-window email link),
// cancel produces no usable successor action.
//
// Best-effort: if the follow-up save fails the cancel has already landed, so
// we only warn rather than surfacing an error to the caller.
func (m *Manager) clearApprovalToken(ctx context.Context, executionID string) error {
	exec, err := m.config.GetExecutionByID(ctx, executionID)
	if err != nil {
		return fmt.Errorf("clearApprovalToken: failed to fetch execution: %w", err)
	}
	if exec == nil {
		return fmt.Errorf("clearApprovalToken: execution not found: %s", executionID)
	}
	exec.ApprovalToken = ""
	exec.ApprovalTokenExpiresAt = nil
	// Guarded on "canceled": the row was read after the cancel landed, and
	// this full-row write must not undo a status change made since (issue
	// #835).
	return m.config.SaveExecutionIfStatus(ctx, exec, []string{config.StatusCanceled})
}

// loadCancelableExecution fetches an execution, validates the approval
// token, and checks the status is cancelable. Extracted from
// CancelExecution to keep both functions below the gocyclo threshold.
func (m *Manager) loadCancelableExecution(ctx context.Context, executionID, token string) (*config.PurchaseExecution, error) {
	execution, err := m.config.GetExecutionByID(ctx, executionID)
	if errors.Is(err, config.ErrNotFound) {
		return nil, fmt.Errorf("execution not found: %s", executionID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get execution: %w", err)
	}
	// execution.ApprovalToken is the SHA-256 hex digest stored at rest
	// (issue #103); ValidateApprovalToken hashes the supplied token and
	// compares digests in constant time, and enforces the TTL (issue #397;
	// legacy rows without ApprovalTokenExpiresAt pass).
	if err := ValidateApprovalToken(execution, token); err != nil {
		return nil, err
	}

	// Only pre-approval rows (pending/notified) are cancelable -- shares the
	// single PurchaseExecution.IsCancelable predicate with the session path in
	// cancelPurchaseViaSession so the policy can never drift between the two
	// flows (issue #645). A scheduled row is revoked, not canceled: the atomic
	// cancel UPDATE matches only pending/notified, so letting it through would
	// report a "concurrent operation" that never happened (issue #73).
	// approved/running rows are mid-execution (the AWS commitment is being or
	// has been created), so canceling them would desync the DB from the cloud.
	if execution.Status == "scheduled" {
		return nil, fmt.Errorf("%w: execution cannot be canceled, current status: scheduled; use the revoke endpoint before it fires", config.ErrExecutionNotInExpectedStatus)
	}
	if !execution.IsCancelable() {
		return nil, fmt.Errorf("%w: execution cannot be canceled, current status: %s", config.ErrExecutionNotInExpectedStatus, execution.Status)
	}
	return execution, nil
}
