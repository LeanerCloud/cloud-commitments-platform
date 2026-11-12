package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
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

// executeTokenApprove is the immediate (no pre-fire delay) tail of
// approveViaToken. It runs only after every approveViaToken gate passed.
func (h *Handler) executeTokenApprove(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution, token, actor string, tokenSession *Session) (any, error) {
	// ApproveExecution now runs the purchase synchronously inside the
	// same call (issue #372). When it returns nil the AWS API call
	// has already happened, so the response surfaces "completed"
	// instead of the transient "approved" the old no-op flow returned.
	//
	// revocationToken is the raw token ApproveExecution's own
	// ApproveAndExecute minted for this approval (issue #103); it is
	// sourced from this return value, never from a DB re-read, since
	// only the token hash is stored and a re-read can only ever yield
	// the hash.
	// approveConstraintsForSession above read a snapshot; the funnel re-runs
	// it on the row its claim returns, the row it buys (issue #415).
	final, revocationToken, approveErr := h.purchase.ApproveExecution(ctx, execution.ExecutionID, token, actor, h.tokenApproveClaimCheck(tokenSession))
	if approveErr != nil {
		return nil, h.tokenApproveFailure(ctx, req, execution.ExecutionID, final, revocationToken, actor, approveErr)
	}
	h.emailTokenApproveOutcome(ctx, req, execution, revocationToken, actor)
	return map[string]string{"status": "completed"}, nil
}

// tokenApproveFailure answers a failed token approve (issue #532). Once the
// claim was won (a row came back) it answers like the session approve: a
// partially_completed run gets its executed email, a provider failure is a
// 502 carrying the row's status, an audit loss a 500. A 4-eyes denial is a
// 403 on every path. Any other refusal before the claim (bad, rotated or
// expired token, lost claim) keeps the tokenActionError mapping.
func (h *Handler) tokenApproveFailure(ctx context.Context, req *events.LambdaFunctionURLRequest, executionID string, final *config.PurchaseExecution, revocationToken, actor string, err error) error {
	if final == nil && !errors.Is(err, purchase.ErrFourEyesDenied) {
		return tokenActionError(err)
	}
	logging.Errorf("purchase[%s]: token approve failed: %v", executionID, err)
	h.emailCommittedPartialRun(ctx, req, final, revocationToken, actor, err)
	return tokenFailureError(executionID, final, err)
}

// emailTokenApproveOutcome sends the executed email for a token approve that
// completed (tokenApproveFailure emails a partial run from the returned row).
// It re-fetches the execution to pick up the FINAL state (status,
// completed_at, per-rec purchase results) ApproveAndExecute wrote -- the
// pre-approve `execution` struct is stale on those fields. Falling back to the stale struct on a re-fetch failure is a
// cosmetic degradation of the email content only: revocationToken is already
// in hand either way, so the Revoke link is always valid. Best-effort (issue
// #291): errors are logged inside sendPurchaseExecutedEmail and never
// propagate, since the purchase is already done.
func (h *Handler) emailTokenApproveOutcome(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution, revocationToken, actor string) {
	emailExec, fetchErr := h.config.GetExecutionByID(ctx, execution.ExecutionID)
	if fetchErr != nil || emailExec == nil {
		logging.Warnf("approveViaToken[%s]: re-fetch for email failed (%v); using pre-approve snapshot",
			execution.ExecutionID, fetchErr)
		emailExec = execution
	}
	h.sendPurchaseExecutedEmail(ctx, req, emailExec, revocationToken, actor)
}

// approvePurchaseViaSession is the session-authed branch of approvePurchase.
// Enforces the approve-any/approve-own RBAC matrix, then hands off to
// purchase.Manager.ApproveAndExecute which atomically flips the row to
// "approved" (stamping session.Email onto ApprovedBy) and runs the
// purchase synchronously. The synchronous-execute is what closes the gap
// from issue #372 — pre-fix, approval was a no-op beyond the status flip
// because no scheduler picked the "approved" row up for the Lambda
// deployment.
//
// Concurrency: ApproveAndExecute uses an atomic transition, so two
// in-flight Approve clicks on the same execution see exactly one win.
// Approvals across different executions run independently — each HTTP
// invocation drives its own executeAndFinalize, which already fans out
// per-account in parallel via executeMultiAccount.
func (h *Handler) approvePurchaseViaSession(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution) (any, error) {
	t0 := time.Now()
	logging.Infof("purchase[%s]: approvePurchaseViaSession entry (auth=session)", execution.ExecutionID)

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

	// approvePurchase only calls this with a session it has authorized, so
	// the orphan 409 reveals nothing to an unauthorized caller (issue #597).
	if err := rejectOrphanExecution(execution); err != nil {
		return nil, err
	}

	if execution.Status != "pending" && execution.Status != "notified" {
		return nil, NewClientError(409, fmt.Sprintf("execution %s cannot be approved (status=%s)", execution.ExecutionID, execution.Status))
	}

	if err := h.authorizeApproveSession(ctx, session, execution); err != nil {
		return nil, err
	}

	// Human session approval: stamp the session user's UUID onto
	// transitioned_by (FK-safe via validUUIDPtrOrNil) so the audit trail
	// records who flipped the row to "approved" (or to "scheduled" on the
	// pre-fire delay path).
	actor := validUUIDPtrOrNil(&session.UserID)

	// Check for Gmail-style pre-fire delay (issue #291 wave-2). When
	// PurchaseDelayHours > 0 the SDK call is deferred; the user gets a
	// "scheduled, revoke before X" email and a window to cancel at $0.
	// Fail closed: a config-read error must NOT silently discard the configured
	// free-cancel window and execute immediately. Return 500 so the caller can retry.
	delay, delayErr := h.purchaseDelay(ctx)
	if delayErr != nil {
		return nil, delayErr
	}
	if delay > 0 {
		return h.approveWithDelay(ctx, execution, delay, session.Email, actor, "")
	}
	return h.executeSessionApprove(ctx, req, execution, session, actor, t0)
}

// executeSessionApprove is the immediate (no pre-fire delay) tail of
// approvePurchaseViaSession. It runs only after every gate there passed.
func (h *Handler) executeSessionApprove(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution, session *Session, actor *string, t0 time.Time) (any, error) {
	// revocationToken is the raw token ApproveAndExecute mints for this
	// approval (issue #103); only the token hash is stored, so this
	// return value is the only place a raw, emailable token exists after
	// the call returns.
	// authorizeApproveSession above read a snapshot; the funnel re-runs the
	// approve-any/approve-own check on the row its claim returns (issue #415).
	final, revocationToken, approveErr := h.purchase.ApproveAndExecute(ctx, execution.ExecutionID, fourEyesActorIdentity(session), actor, h.sessionApproveClaimCheck(session))
	if approveErr != nil {
		logging.Errorf("purchase[%s]: approvePurchaseViaSession failed after %s: %v",
			execution.ExecutionID, time.Since(t0), approveErr)
		return nil, h.executeFailure(ctx, req, execution.ExecutionID, "approved", final, revocationToken, session.Email, approveErr)
	}

	logging.Infof("purchase[%s]: approvePurchaseViaSession completed in %s (auth=session)",
		execution.ExecutionID, time.Since(t0))
	// Best-effort post-execution notification email (issue #291). Fires
	// after the synchronous purchase completes so the email carries the
	// final committed state. Errors are logged inside sendPurchaseExecutedEmail
	// and never propagate — the purchase is already done at this point.
	h.sendPurchaseExecutedEmail(ctx, req, execution, revocationToken, session.Email)
	return map[string]string{"status": "completed"}, nil
}

// authorizeApproveSession runs every gate approvePurchaseViaSession must pass
// before mutating state: the account-scope check (issue #92), the
// approve-any/approve-own RBAC matrix with its Constraints, and the 4-eyes
// distinct-approver rule.
func (h *Handler) authorizeApproveSession(ctx context.Context, session *Session, execution *config.PurchaseExecution) error {
	if err := h.authorizeScopedSessionApprove(ctx, session, execution); err != nil {
		return err
	}
	// 4-eyes mode (issue #1005): enforce after RBAC so only users who already
	// pass the approve-any / approve-own gate reach this check. The admin
	// wildcard inside authorizeSessionApprove short-circuits RBAC but NOT this
	// check; admins who created the row must disable the mode first.
	return h.requireDifferentApprover(ctx, session, execution.ExecutionID, execution.CreatedByUserID, execution.CreatedBySystem)
}

// authorizeScopedSessionApprove runs the account-scope gate (issue #92)
// before the approve RBAC and Constraints checks, so an out-of-scope caller
// gets the enumeration-safe 404 before a 403 can reveal the execution exists.
func (h *Handler) authorizeScopedSessionApprove(ctx context.Context, session *Session, execution *config.PurchaseExecution) error {
	if err := h.requireExecutionAccess(ctx, session, execution.ExecutionID); err != nil {
		return err
	}
	return h.authorizeSessionApprove(ctx, session, execution)
}

// authorizeSessionApprove returns nil when the session is permitted to
// approve the given execution under the approve-any / approve-own RBAC
// rules added in issue #286. Returns a 403 ClientError otherwise.
// Mirror of authorizeSessionCancel.
//
// Also enforces the approve-any/approve-own permission's own per-permission
// Constraints against execution.Recommendations (issue #60): approving here
// hands off to purchase.Manager.ApproveAndExecute, which fires the AWS
// purchase synchronously, so this is as much a money-spending path as
// execute-any/execute-own and must be capped the same way. Without this, a
// session whose approve-any is constrained to e.g. MaxPurchaseAmount: $1,000
// could approve (and thereby execute) an arbitrarily large pending execution
// created by someone else, even though the identically-capped execute-any
// permission already blocks a matching direct-execute.
//
// A session holding BOTH approve-any and approve-own is evaluated on
// approve-any ONLY (the first branch below returns unconditionally once
// hasAny is true): approve-any's Constraints are what gate it, even for an
// execution this session happens to have created itself. This mirrors
// authorizeSessionExecuteDirect's execute-any/execute-own ordering and the
// group model's own semantics -- approve-any is the broader grant, so a
// group holding both is expected to configure whichever Constraints the
// broader grant should carry, not rely on approve-own's (narrower-intent)
// Constraints ever being consulted for such a session.
//
// The returned error is wrapped via wrapConstraintDenied whenever it comes
// from the Constraints check (as opposed to the bare verb/ownership checks
// above it): approvePurchase's session-vs-token dispatch must treat a
// Constraints-exceeded denial as terminal, not as a signal to fall through
// to the token/contact_email flow, which never evaluates Constraints (issue
// #60 review follow-up; see constraintDeniedError's doc comment).
func (h *Handler) authorizeSessionApprove(ctx context.Context, session *Session, execution *config.PurchaseExecution) error {
	// The stateless admin API key has full access and no user row to resolve
	// permissions from. Administrators-group users fall through and pass via
	// the approve-any check below, since {admin, *} matches
	// any requested permission.
	if session.UserID == apiKeyAdminUserID {
		return nil
	}
	if h.auth == nil {
		return NewClientError(500, "authentication service not configured")
	}

	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionApproveAny)
	if err != nil {
		return err
	}
	if hasAny {
		return wrapConstraintDenied(h.enforcePurchaseConstraints(ctx, session, auth.ActionApproveAny, execution.Recommendations))
	}

	hasOwn, err := h.sessionHoldsVerb(ctx, session, auth.ActionApproveOwn)
	if err != nil {
		return err
	}
	if !hasOwn {
		return NewClientError(403, "permission denied: requires approve-any or approve-own on purchases")
	}

	if execution.CreatedByUserID == nil || *execution.CreatedByUserID != session.UserID {
		return NewClientError(403, "permission denied: cannot approve another user's pending purchase")
	}
	return wrapConstraintDenied(h.enforcePurchaseConstraints(ctx, session, auth.ActionApproveOwn, execution.Recommendations))
}
