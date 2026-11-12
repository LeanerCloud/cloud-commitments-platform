package api

import (
	"context"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/aws/aws-lambda-go/events"
)

// Purchase action handlers.
func (h *Handler) approvePurchase(ctx context.Context, req *events.LambdaFunctionURLRequest, execID, token string) (any, error) {
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

	// Three-mode dispatch — same shape as cancelPurchase (issue #46) and
	// retryPurchase (issue #47):
	//   1. Session present AND RBAC-authorized (admin / approve-any /
	//      approve-own match) → session-authed approve, regardless of
	//      whether a token is in the URL. Closes issue #286: an admin
	//      logged into the dashboard can now approve from the History
	//      view without round-tripping to the SES email.
	//   2. token != "" → legacy email-link flow without a qualifying
	//      session. authorizeApprovalAction enforces the per-account
	//      contact_email gate from PR #101; the purchase service
	//      validates the token itself before mutating state.
	//   3. token == "" → a session denial above is final (issue #173);
	//      with no session, approvePurchaseViaSession returns 401.
	// A final denial answers exactly like a missing execution (issue #597).
	if session := h.tryGetSession(ctx, req); session != nil {
		switch err := h.authorizeScopedSessionApprove(ctx, session, execution); {
		case err == nil:
			return h.approvePurchaseViaSession(ctx, req, execution)
		case fallsThroughToToken(err, token):
			// Explicit 403 or out-of-scope 404, with a token → fall
			// through to the token branch so the contact_email gate gets a
			// chance (a logged-in user without approve-* or outside the
			// account scope may still be the per-account contact recipient).
		default:
			// A final denial (no token) or a transient failure — propagate
			// instead of silently widening; the denial is concealed.
			return nil, concealExecutionDenial(err)
		}
	}

	if token != "" {
		return h.approveViaToken(ctx, req, execution, token)
	}

	return h.approvePurchaseViaSession(ctx, req, execution)
}

// requireSessionBeforeLookup answers 401 to a caller with no valid session
// before the execution is loaded. Every branch of the email-link
// approve/cancel/revoke routes needs a session (the token alone never
// authorizes), so an unauthenticated caller gets the same response for a
// missing and an existing execution ID (issue #435).
//
// CSRF is validated here too, before the lookup (issue #597). Every
// remaining caller is session-authenticated, and the session bearer token
// is cookie-equivalent, so the token branch needs CSRF as much as the
// session branch does. Checking it only on the session branch, after the
// lookup, answered "CSRF validation failed" for an existing execution and
// 404 for a missing one.
func (h *Handler) requireSessionBeforeLookup(ctx context.Context, req *events.LambdaFunctionURLRequest) error {
	if h.tryGetSession(ctx, req) == nil {
		return NewClientError(401, "sign in with the account's contact email to approve or cancel this purchase")
	}
	if err := h.validateCSRF(ctx, req); err != nil {
		return errCSRFRejected
	}
	return nil
}

// approveViaToken handles the email-link approve branch of approvePurchase to keep
// that function under the cyclomatic limit.
func (h *Handler) approveViaToken(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution, token string) (any, error) {
	actor, err := h.authorizeApprovalAction(ctx, req, execution)
	if err != nil {
		return nil, concealExecutionDenial(err)
	}
	// 4-eyes mode (issue #1005): resolve the session again for identity
	// comparison. On the email-token deep-link flow the user is logged in
	// (the frontend forces a login before reaching this endpoint), so
	// tryGetSession returns their session. Pure-email-client flows have no
	// session; requireDifferentApprover returns 500 when mode is on and no
	// session is available (fail-closed).
	tokenSession := h.tryGetSession(ctx, req)
	if err := h.requireDifferentApprover(ctx, tokenSession, execution.ExecutionID, execution.CreatedByUserID, execution.CreatedBySystem); err != nil {
		return nil, err
	}
	// SEC-01 follow-up (issue #60 review): the deep-link flow always forces
	// a login, so tokenSession is populated whenever a browser reaches this
	// endpoint. approvePurchase's session-vs-token dispatch already denies a
	// session whose approve-any/approve-own is capped tighter than this
	// request before it ever reaches approveViaToken (authorizeSessionApprove
	// returns a terminal constraintDeniedError, not a fall-through 403). This
	// is a second, independent evaluation of that same session's Constraints
	// so a capped approver who is ALSO the account's registered
	// contact_email cannot use the email link to approve past their own cap
	// -- the contact_email gate above authorizes a completely different
	// principal (the account's contact), and must not be read as clearing
	// Constraints on this session's own approve-* permission.
	if err := h.approveConstraintsForSession(ctx, tokenSession, execution); err != nil {
		return nil, err
	}
	// Check for Gmail-style pre-fire delay (issue #291 wave-2).
	// Token/email-link path: no authenticated session UUID is available, so the
	// scheduled transition is recorded as system-initiated (transitioned_by = NULL).
	// Fail closed: a config-read error must NOT silently discard the configured
	// free-cancel window and execute immediately. Return 500 so the caller can retry.
	delay, delayErr := h.purchaseDelay(ctx)
	if delayErr != nil {
		return nil, delayErr
	}
	if delay > 0 {
		// The delayed branch bypasses ApproveExecution, which is where the
		// token is otherwise validated (issue #586). This check reads the
		// snapshot loaded at the start of the request; approveWithDelay pins
		// the token again inside the status CAS so a token rotated since
		// cannot win the transition (issue #590).
		if err := purchase.ValidateApprovalToken(execution, token); err != nil {
			return nil, tokenActionError(err)
		}
		return h.approveWithDelay(ctx, execution, delay, actor, nil, token)
	}
	return h.executeTokenApprove(ctx, req, execution, token, actor, tokenSession)
}

// purchaseDelay reads the configured Gmail-style pre-fire delay (issue #291).
// Fail closed: a config-read error must not silently discard the configured
// free-cancel window, so it is returned (a 500 the caller can retry).
func (h *Handler) purchaseDelay(ctx context.Context) (time.Duration, error) {
	globalCfg, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to read global config for purchase delay check: %w", err)
	}
	delay := globalCfg.GetPurchaseDelay()
	if delay > 0 && h.fireSweepOff {
		// A stored (or default) delay predating the deployment's loss of its
		// fire sweep: scheduling would strand the approval (#1588). Executing
		// immediately would drop the free-cancel window the approver was
		// promised, so refuse until the delay is set to 0.
		return 0, NewClientError(409, `this deployment schedules no fire_scheduled_purchases sweep, so a delayed approval would never execute: send PUT /api/config with {"purchase_delay_hours": 0}, then approve again`)
	}
	return delay, nil
}
