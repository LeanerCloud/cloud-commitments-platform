package api

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) cancelPurchase(ctx context.Context, req *events.LambdaFunctionURLRequest, execID, token string) (any, error) {
	if err := validateUUID(execID); err != nil {
		return nil, err
	}

	if err := h.requireSessionBeforeLookup(ctx, req); err != nil {
		return nil, err
	}

	execution, err := h.getExecutionOr404(ctx, execID)
	if err != nil {
		return nil, err
	}

	// Three-mode dispatch:
	//   1. Session present AND RBAC-authorized (admin / cancel-any /
	//      cancel-own match) → session-authed cancel, regardless of
	//      whether a token is in the URL. Restores parity with the
	//      History page Cancel button: a user who can cancel from the
	//      dashboard should also be able to cancel from the email-link
	//      flow, since the same email-link flow already requires a
	//      logged-in session before reaching this endpoint (see
	//      frontend purchases-deeplink.ts). Without this branch, an
	//      admin clicking Cancel from a notification email about an
	//      ambient-credentials execution (CloudAccountID == nil → no
	//      per-account contact_email available) is locked out of an
	//      action they can perform from the dashboard. The token in
	//      the URL is informational at that point — the session has
	//      already authenticated the actor.
	//      A session whose held cancel verb is denied by its Constraints
	//      (issue #678) is final: it does not fall through to the token.
	//   2. token != "" → legacy email-link flow without a qualifying
	//      session. authorizeApprovalAction enforces the per-account
	//      contact_email gate from PR #101; the purchase service
	//      validates the token itself before mutating state. This
	//      preserves the security model for forwarded email / shared
	//      inbox / stolen link cases — a non-privileged session falls
	//      through to this branch.
	//   3. token == "" → a session denial above is final (issue #173);
	//      with no session, cancelPurchaseViaSession returns 401.
	if session := h.tryGetSession(ctx, req); session != nil {
		switch err := h.authorizeCancelSession(ctx, session, execution); {
		case err == nil:
			// Session is RBAC-authorized → run the session-authed cancel.
			return h.cancelPurchaseViaSession(ctx, req, execution)
		case fallsThroughToToken(err, token):
			// Explicit "permission denied" (403) or out-of-scope 404, with
			// a token → fall through to the token branch so the
			// contact_email gate still gets a chance (a logged-in user
			// without admin / cancel-* may still be the per-account contact
			// email recipient).
		default:
			// Transient failure (auth-service down, HasPermissionAPI
			// returning a wrapped error, h.auth==nil 500). Propagate
			// instead of silently widening to the contact_email gate —
			// a stale auth backend should not mask itself as a 403 about
			// missing contact emails. CR feedback on PR #216. A final
			// denial answers like a missing execution (issue #597).
			return nil, concealExecutionDenial(err)
		}
	}

	if token != "" {
		return h.cancelViaToken(ctx, req, execution, execID, token)
	}

	return h.cancelPurchaseViaSession(ctx, req, execution)
}

// cancelViaToken is the email-link branch of cancelPurchase (mode 2): the
// per-account contact_email gate first, then the purchase service validates
// the token before it mutates state. Same gate order as before extraction.
func (h *Handler) cancelViaToken(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution, execID, token string) (any, error) {
	actor, err := h.authorizeApprovalAction(ctx, req, execution)
	if err != nil {
		return nil, concealExecutionDenial(err)
	}
	if err := h.purchase.CancelExecution(ctx, execID, token, actor); err != nil {
		return nil, tokenActionError(err)
	}
	return map[string]string{"status": "canceled"}, nil
}

// guardCancelableViaSession returns a ClientError if execution is not eligible
// for cancellation on the session path (pending|notified only):
//   - "scheduled" executions are not IsCancelable; they go through the /revoke
//     endpoint. The explicit branch keeps the revoke pointer in the 409 instead
//     of the generic message (CodeRabbit finding #6, PR #1277; issue #73).
//   - all other non-IsCancelable statuses (approved, completed, etc.) are rejected
//     with a generic "cannot be canceled" 409.
func guardCancelableViaSession(execution *config.PurchaseExecution) error {
	if execution.Status == "scheduled" {
		return NewClientError(409, fmt.Sprintf(
			"execution %s is scheduled; use the revoke endpoint to cancel it before it fires",
			execution.ExecutionID))
	}
	if !execution.IsCancelable() {
		return NewClientError(409, fmt.Sprintf("execution %s cannot be canceled (status=%s)", execution.ExecutionID, execution.Status))
	}
	return nil
}

// cancelPurchaseViaSession is the session-authed branch of cancelPurchase.
// Enforces the cancel-any/cancel-own RBAC matrix, validates the execution
// is in a cancelable state (pending|notified), atomically flips the row
// to "canceled" AND drops its purchase_suppressions in the same
// transaction, and stamps session.Email onto CanceledBy. The History
// UI's annotateCanceled() helper renders CanceledBy as
// "canceled by <email>" at read time -- see handler_history.go.
//
// The atomic suppression cleanup mirrors purchase.Manager.CancelExecution
// on the email-token path: an executePurchase upfront writes
// purchase_suppressions to hide the just-bought capacity from the
// recommendations list during the grace window, and cancel must drop
// those rows in the same commit so a crash between the two writes can't
// leave the rec list hiding capacity the user already canceled.
func (h *Handler) cancelPurchaseViaSession(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution) (any, error) {
	// These endpoints are AuthPublic so the outer middleware skips CSRF.
	// Enforce it here for the session-authed sub-path: the session bearer
	// token is cookie-equivalent and must be CSRF-protected.
	if err := h.validateCSRF(ctx, req); err != nil {
		return nil, NewClientError(403, "CSRF validation failed")
	}

	session, err := h.requireSession(ctx, req)
	if err != nil {
		return nil, err
	}

	if err := guardCancelableViaSession(execution); err != nil {
		return nil, err
	}

	if err := h.authorizeCancelSession(ctx, session, execution); err != nil {
		return nil, err
	}

	// Atomically flip status from pending/notified to canceled + clear
	// suppressions in one tx. CancelExecutionAtomic issues a conditional
	// UPDATE WHERE status IN ('pending','notified'), so a concurrent approve
	// that has already transitioned the row to 'approved' causes zero rows
	// to be affected and we return a 409 with the current status rather
	// than silently overwriting an approved purchase.
	var canceledBy *string
	if session.Email != "" {
		e := session.Email
		canceledBy = &e
	}
	canceled, currentStatus, cancelErr := h.cancelAndReleaseSuppressions(ctx, execution.ExecutionID, canceledBy)
	if cancelErr != nil {
		return nil, fmt.Errorf("cancel execution %s: %w", execution.ExecutionID, cancelErr)
	}
	if !canceled {
		return nil, NewClientError(409, fmt.Sprintf("execution %s cannot be canceled: a concurrent operation already transitioned it to %q", execution.ExecutionID, currentStatus))
	}

	return map[string]string{"status": "canceled"}, nil
}

// cancelAndReleaseSuppressions flips a pending/notified row to canceled and
// drops its purchase_suppressions in one transaction. canceled is false (with
// the row's current status) when a concurrent operation moved it first; the
// suppressions are then left alone.
func (h *Handler) cancelAndReleaseSuppressions(ctx context.Context, executionID string, canceledBy *string) (canceled bool, currentStatus string, err error) {
	err = h.config.WithTx(ctx, func(tx pgx.Tx) error {
		var txErr error
		canceled, currentStatus, txErr = h.config.CancelExecutionAtomic(ctx, tx, executionID, canceledBy)
		if txErr != nil {
			return txErr
		}
		if !canceled {
			return nil
		}
		return h.config.DeleteSuppressionsByExecutionTx(ctx, tx, executionID)
	})
	return canceled, currentStatus, err
}

// authorizeCancelSession runs the account-scope gate (issue #92) before the
// cancel-any/cancel-own RBAC matrix, so an out-of-scope caller gets the
// enumeration-safe 404 before a 403 can reveal the execution exists. Shared
// by the cancel and revoke session paths.
func (h *Handler) authorizeCancelSession(ctx context.Context, session *Session, execution *config.PurchaseExecution) error {
	if err := h.requireExecutionAccess(ctx, session, execution.ExecutionID); err != nil {
		return err
	}
	return h.authorizeSessionCancel(ctx, session, execution)
}

// authorizeSessionCancel returns nil when the session is permitted to cancel
// the given execution under the cancel-any / cancel-own RBAC rules added in
// issue #46, and the Constraints of the permission that grants the verb cover
// every recommendation of the execution (issue #678, same strict sets as the
// #404 revoke check). A constrained cancel-any falls back to cancel-own only on
// a 403; a Constraints denial is terminal for the email-token fall-through (it
// is wrapped so fallsThroughToToken does not hand it to the contact_email
// branch). The token branch itself is a separate capability and is not
// Constraints-checked. Returns a 403 ClientError otherwise.
func (h *Handler) authorizeSessionCancel(ctx context.Context, session *Session, execution *config.PurchaseExecution) error {
	// Stateless admin API key: full access, no user row. Administrators-group
	// users pass via the cancel-any check below.
	if session.UserID == apiKeyAdminUserID {
		return nil
	}
	if h.auth == nil {
		return NewClientError(500, "authentication service not configured")
	}

	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionCancelAny)
	if err != nil {
		return err
	}
	var anyErr error
	if hasAny {
		anyErr = wrapConstraintDenied(h.requirePermissionConstraints(ctx, session, auth.ActionCancelAny, auth.ResourcePurchases, revokeExecutionConstraintSets(execution)))
		if anyErr == nil || !isForbidden(anyErr) {
			return anyErr
		}
	}
	return h.authorizeCancelOwn(ctx, session, execution, anyErr)
}

// authorizeCancelOwn requires cancel-own, a matching creator and the cancel-own
// Constraints. anyErr is the cancel-any Constraints denial (nil when the
// session has no cancel-any); it is returned instead of the generic denials so
// a constraint refusal stays terminal.
func (h *Handler) authorizeCancelOwn(ctx context.Context, session *Session, execution *config.PurchaseExecution, anyErr error) error {
	hasOwn, err := h.sessionHoldsVerb(ctx, session, auth.ActionCancelOwn)
	if err != nil {
		return err
	}
	if !hasOwn {
		if anyErr != nil {
			return anyErr
		}
		return NewClientError(403, "permission denied: requires cancel-any or cancel-own on purchases")
	}
	if execution.CreatedByUserID == nil || *execution.CreatedByUserID != session.UserID {
		if anyErr != nil {
			return anyErr
		}
		return NewClientError(403, "permission denied: cannot cancel another user's pending purchase")
	}
	return wrapConstraintDenied(h.requirePermissionConstraints(ctx, session, auth.ActionCancelOwn, auth.ResourcePurchases, revokeExecutionConstraintSets(execution)))
}
