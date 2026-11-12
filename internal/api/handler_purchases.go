// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// executeFailure answers a failed run-now, session approve or direct execute.
// A partially_completed run bought part of its commitments, so it gets the
// executed email with the revocation token the funnel minted for it, as a
// completed run does, before the 502 (issue #532). final is the row the
// funnel returned, carrying the per-rec outcome and completed_at.
func (h *Handler) executeFailure(ctx context.Context, req *events.LambdaFunctionURLRequest, executionID, action string, final *config.PurchaseExecution, revocationToken, executedBy string, err error) error {
	h.emailCommittedPartialRun(ctx, req, final, revocationToken, executedBy, err)
	return executeFailureError(executionID, action, final, err)
}

func (h *Handler) emailCommittedPartialRun(ctx context.Context, req *events.LambdaFunctionURLRequest, final *config.PurchaseExecution, revocationToken, executedBy string, err error) {
	if purchase.IsCommittedPartialRun(final, err) {
		h.sendPurchaseExecutedEmail(ctx, req, final, revocationToken, executedBy)
	}
}

// tokenFailureError is executeFailureError with fixed messages: a token
// approver is not scoped to every account on the execution, so the run
// error's text stays in the server log. Statuses and details are unchanged.
func tokenFailureError(executionID string, final *config.PurchaseExecution, err error) error {
	if final == nil || errors.Is(err, purchase.ErrFourEyesDenied) {
		// No row, or a static policy denial: nothing from the run to hide.
		return executeFailureError(executionID, "approved", final, err)
	}
	// Every branch below is fixed text; audit loss is first because it wraps
	// whatever else the run failed with.
	details := map[string]any{"execution_id": executionID, "status": final.Status}
	switch {
	case errors.Is(err, config.ErrAuditLoss):
		return NewClientError(500, "purchase audit could not be completed")
	case errors.Is(err, purchase.ErrStaleAzurePricing):
		return NewClientErrorWithDetails(409, purchase.ErrStaleAzurePricing.Error(), details)
	case errors.Is(err, purchase.ErrClaimedRowRefused):
		code := 500
		if ce, ok := IsClientError(err); ok {
			code = ce.code
		}
		return NewClientErrorWithDetails(code, "the purchase changed after it was authorized and no longer passes your permission checks, so nothing was bought and it is now marked failed", details)
	case purchase.IsCommittedPartialRun(final, err):
		return NewClientErrorWithDetails(502, "purchase partially completed: some commitments were purchased and can be revoked, others failed", details)
	default:
		return NewClientErrorWithDetails(502, "purchase execution failed", details)
	}
}

// Stale Azure prices are refused before purchase, even after a successful claim.
func executeFailureError(executionID, action string, final *config.PurchaseExecution, err error) error {
	if errors.Is(err, purchase.ErrFourEyesDenied) {
		return NewClientError(403, fmt.Sprintf("execution %s cannot be %s: %v", executionID, action, err))
	}
	if final == nil {
		return unclaimedExecuteError(executionID, action, err)
	}
	switch {
	case errors.Is(err, config.ErrAuditLoss):
		logRawExecuteFailure(executionID, err)
		return NewClientError(500, fmt.Sprintf("execution %s ran but its final status could not be saved (reference: execution %s)", executionID, executionID))
	case errors.Is(err, purchase.ErrClaimedRowRefused):
		return claimedRowRefusalError(final, err)
	case errors.Is(err, purchase.ErrStaleAzurePricing):
		return NewClientError(409, purchase.ErrStaleAzurePricing.Error())
	case purchase.IsCommittedPartialRun(final, err):
		category, msg := summarizeExecuteFailure(executionID, err)
		return NewClientErrorWithDetails(502, fmt.Sprintf("execution %s partially completed: some commitments were purchased and can be revoked, others failed: %s (reference: execution %s)", executionID, msg, executionID),
			map[string]any{"execution_id": executionID, "status": final.Status, "category": category})
	default:
		category, msg := summarizeExecuteFailure(executionID, err)
		return NewClientErrorWithDetails(502, fmt.Sprintf("execution %s failed: %s (reference: execution %s)", executionID, msg, executionID),
			map[string]any{"execution_id": executionID, "status": final.Status, "category": category})
	}
}

// logRawExecuteFailure keeps the provider's own error text in the server log
// only; responses carry a category message and the execution ID.
func logRawExecuteFailure(executionID string, err error) {
	logging.Errorf("purchase[%s]: execution failed: %v", executionID, err)
}

func summarizeExecuteFailure(executionID string, err error) (category, message string) {
	logRawExecuteFailure(executionID, err)
	return purchase.SummarizeFailure(err)
}

// unclaimedExecuteError answers a failure where the funnel returned no row:
// a lost claim race or a vanished row is a 409, anything else is a plain error.
func unclaimedExecuteError(executionID, action string, err error) error {
	if errors.Is(err, config.ErrExecutionNotInExpectedStatus) || errors.Is(err, config.ErrNotFound) {
		return NewClientError(409, fmt.Sprintf("execution %s cannot be %s: %v", executionID, action, err))
	}
	return fmt.Errorf("execution %s could not be %s: %w", executionID, action, err)
}

// executionNotFound is the answer for an execution ID that does not exist
// on the approve, cancel and revoke routes. concealExecutionDenial returns
// the same error for an execution the caller may not act on, so the two
// cases have the same status, headers and body (issue #597).
func executionNotFound() *clientError {
	return &clientError{code: 404, message: "execution not found"}
}

// concealedDenialError is the "execution not found" 404 returned in place of
// a final authorization denial. It responds exactly like executionNotFound
// (IsClientError resolves it through Unwrap) and keeps the denial it hides
// in cause, for logs and tests only.
type concealedDenialError struct {
	*clientError
	cause error
}

func (e *concealedDenialError) Unwrap() error { return e.clientError }

// concealExecutionDenial maps a final authorization denial on an existing
// execution (an account-scope miss, an RBAC or contact_email 403, or a 401
// from the contact_email gate) to the 404 a missing execution gets, so a
// signed-in caller cannot use the approve, cancel and revoke routes to
// learn which execution IDs exist (issue #597). Only direct *clientError
// values are mapped: a Constraints denial (constraintDeniedError) goes to a
// caller who already holds approve-* for the execution, and wrapped
// backend failures must keep surfacing as 500s.
func concealExecutionDenial(err error) error {
	ce, ok := err.(*clientError) //nolint:errorlint // strict (unwrapped) assertion is deliberate; see isPermissionDenied
	if !errors.Is(err, errNotFound) && (!ok || (ce.code != 401 && ce.code != 403)) {
		return err
	}
	logging.Infof("purchase action denied, answered as execution not found: %v", err)
	return &concealedDenialError{clientError: executionNotFound(), cause: err}
}

// tokenActionError maps the email-link approve/cancel failures that are the
// caller's fault to a 4xx: a wrong token is 403 (as on the RI exchange
// approve path), as is a token rotated or expired between the snapshot check
// and the status CAS (config.ErrApprovalTokenChanged, issue #1190), an expired
// token is 410, and an execution whose status no longer allows the action is
// 409. Anything else stays a server error.
func tokenActionError(err error) error {
	switch {
	case errors.Is(err, config.ErrAuditLoss):
		return err
	case errors.Is(err, purchase.ErrClaimedRowRefused):
		return claimedRowRefusalError(nil, err)
	case errors.Is(err, purchase.ErrStaleAzurePricing):
		return NewClientError(409, purchase.ErrStaleAzurePricing.Error())
	case errors.Is(err, purchase.ErrInvalidApprovalToken), errors.Is(err, config.ErrApprovalTokenChanged):
		return NewClientError(403, "invalid approval token")
	case errors.Is(err, purchase.ErrApprovalTokenExpired):
		return NewClientError(410, "approval token has expired")
	case errors.Is(err, config.ErrExecutionNotInExpectedStatus):
		return NewClientError(409, err.Error())
	default:
		return err
	}
}

// approveConstraintsForSession is approveViaToken's independent Constraints
// check (issue #60 review follow-up): it mirrors authorizeSessionApprove's
// approve-any/approve-own grant-then-constrain matrix. A session with no
// applicable approve-* verb has no Constraints to be bound by on a
// user-created row and returns nil (it is authorized through the token
// flow's contact_email mechanism); this function only narrows, never widens.
// Plan-step rows (PlanID set, no creator) are tighter (#631): approve-own
// Constraints apply regardless of creator, and an approver with no verb or no
// session is held to the plan's step cap (requirePlanStepCap). The admin API
// key stays exempt.
func (h *Handler) approveConstraintsForSession(ctx context.Context, session *Session, execution *config.PurchaseExecution) error {
	if session != nil && session.UserID == apiKeyAdminUserID {
		return nil
	}
	if session != nil && h.auth != nil {
		bound, err := h.enforceHeldApproveConstraints(ctx, session, execution)
		if bound || err != nil {
			return err
		}
	}
	return h.requirePlanStepCap(ctx, execution)
}

// enforceHeldApproveConstraints applies the Constraints of the approve verb the
// session holds (approve-any first, then approve-own). bound is false when no
// verb applies to this row, so the caller falls back to the plan cap.
func (h *Handler) enforceHeldApproveConstraints(ctx context.Context, session *Session, execution *config.PurchaseExecution) (bound bool, err error) {
	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionApproveAny)
	if err != nil {
		return false, err
	}
	if hasAny {
		return true, h.enforcePurchaseConstraints(ctx, session, auth.ActionApproveAny, execution.Recommendations)
	}
	hasOwn, err := h.sessionHoldsVerb(ctx, session, auth.ActionApproveOwn)
	if err != nil {
		return false, err
	}
	// A plan-step row has no creator (plan_step_attach.go), so its approve-own
	// holder is bound by their Constraints regardless of it.
	if hasOwn && (execution.PlanID != "" || (execution.CreatedByUserID != nil && *execution.CreatedByUserID == session.UserID)) {
		return true, h.enforcePurchaseConstraints(ctx, session, auth.ActionApproveOwn, execution.Recommendations)
	}
	return false, nil
}

// requirePlanStepCap applies the plan's max_step_commitment to a plan-step row
// approved through the email-link flow by someone who holds no approve verb
// (or no session at all): without it that approver is bound by no cap. A
// manual-approval plan with no cap keeps its human approval as the only
// control and approves as before (CheckStepCommitmentCap would refuse it);
// an auto-purchase plan, or a manual one that sets a cap, is checked.
func (h *Handler) requirePlanStepCap(ctx context.Context, execution *config.PurchaseExecution) error {
	if execution.PlanID == "" {
		return nil
	}
	plan, err := h.config.GetPurchasePlan(ctx, execution.PlanID)
	if err != nil {
		return fmt.Errorf("failed to read plan %s for the approval cap check: %w", execution.PlanID, err)
	}
	if !purchase.StepCapApplies(plan) {
		return nil
	}
	if capErr := purchase.CheckStepCommitmentCap(plan, execution.Recommendations); capErr != nil {
		return NewClientError(403, capErr.Error())
	}
	return nil
}

// approveWithDelay is the Gmail-style pre-fire delay branch (issue #291 wave-2).
// When PurchaseDelayHours > 0 this path runs INSTEAD OF ApproveAndExecute/
// ApproveExecution:
//  1. Transitions the execution to status=scheduled (no SDK call).
//  2. Sets ScheduledExecutionAt = now+delay (SDK fires at that time).
//  3. Sends the "scheduled — revoke before X" email immediately.
//  4. Returns the execution ID and the window timestamp.
//
// The scheduler picks up rows with status=scheduled and
// scheduled_execution_at <= NOW() and fires the actual SDK call.
// Revoking a status=scheduled execution (via the revoke handler or the
// History "Revoke" button) transitions it to "canceled" at zero cloud cost.
//
// approvalToken is the raw email-link token on the token path and "" on the
// session path; see scheduleApprovedExecution.
func (h *Handler) approveWithDelay(ctx context.Context, execution *config.PurchaseExecution, delay time.Duration, actor string, transitionedBy *string, approvalToken string) (any, error) {
	updated, err := h.scheduleApprovedExecution(ctx, execution, delay, actor, transitionedBy, approvalToken)
	if err != nil {
		if errors.Is(err, config.ErrApprovalTokenChanged) {
			// The token was rotated or expired after approveViaToken validated
			// it against the request's snapshot (issue #590).
			return nil, NewClientError(403, "invalid approval token")
		}
		if errors.Is(err, config.ErrExecutionNotInExpectedStatus) {
			// A concurrent Cancel beat the approve: the CAS rejected because the row
			// is no longer in a schedulable status. Return 409 so the client knows to
			// reload and not retry with the same payload.
			return nil, NewClientError(409, "purchase no longer schedulable: state changed concurrently (concurrent cancel)")
		}
		if approvalToken != "" {
			// A token approver is not scoped to the store's error text.
			logging.Errorf("purchase[%s]: token approve failed to schedule: %v", execution.ExecutionID, err)
			return nil, NewClientError(500, "failed to schedule execution")
		}
		logging.Errorf("purchase[%s]: failed to schedule execution: %v", execution.ExecutionID, err)
		return nil, NewClientError(500, fmt.Sprintf("failed to schedule execution (reference: execution %s)", execution.ExecutionID))
	}
	// Best-effort scheduled notification email.
	h.sendPurchaseScheduledEmail(ctx, updated, actor)
	var windowClosesAt string
	if updated.ScheduledExecutionAt != nil {
		windowClosesAt = updated.ScheduledExecutionAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	return map[string]string{
		"status":       "scheduled",
		"execution_id": updated.ExecutionID,
		"executes_at":  windowClosesAt,
		"message":      "Purchase scheduled. You can revoke it for free until the execution window closes.",
	}, nil
}

// scheduleApprovedExecution transitions an execution from status=pending or
// status=notified to status=scheduled and stamps ScheduledExecutionAt =
// now+delay and ApprovedBy, in one store transaction: a scheduled row without
// an execution time is never selected by the scheduler, so a stamp that failed
// after a committed transition stranded the purchase (#348 F6). No SDK call
// is made. Returns the updated execution on success.
//
// actor is the human-readable approver identity (email) recorded in
// ApprovedBy. transitionedBy is the approver's user UUID stamped onto the
// audit trail by the CAS (nil for token/scheduler paths with no session UUID).
//
// The atomic CAS (TransitionExecutionStatus WHERE status IN (pending,notified))
// prevents a silent revoke loss: if a concurrent Cancel flipped the row to
// "canceled" between the caller's SELECT and this write, TransitionExecutionStatus
// returns ErrExecutionNotInExpectedStatus and we surface a 409 instead of
// blindly overwriting the canceled state.
//
// A non-empty approvalToken (the email-link path) is also pinned inside the
// CAS: the row's stored token hash must still match it and be unexpired, so
// a re-notification that rotated the token after the caller validated its
// snapshot rejects the transition with config.ErrApprovalTokenChanged
// (issue #590). The session path passes "" and is not token-gated.
func (h *Handler) scheduleApprovedExecution(ctx context.Context, execution *config.PurchaseExecution, delay time.Duration, actor string, transitionedBy *string, approvalToken string) (*config.PurchaseExecution, error) {
	var approvedBy *string
	if actor != "" {
		approvedBy = &actor
	}
	// One transaction: no other writer can see or change the row between the
	// transition and the stamp, so a revoke can no longer land in between
	// (issue #835).
	var updated *config.PurchaseExecution
	var err error
	if approvalToken != "" {
		updated, err = h.config.ScheduleApprovedExecutionTokenPinned(ctx, execution.ExecutionID, []string{"pending", "notified"},
			transitionedBy, approvalToken, time.Now().Add(delay), approvedBy)
	} else {
		updated, err = h.config.ScheduleApprovedExecution(ctx, execution.ExecutionID, []string{"pending", "notified"},
			transitionedBy, time.Now().Add(delay), approvedBy)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to schedule execution %s: %w", execution.ExecutionID, err)
	}
	return updated, nil
}

// sendPurchaseScheduledEmail fires the Gmail-style scheduled-delay notification
// email immediately after scheduling. Best-effort: errors are logged and never
// returned to the caller (the purchase state is already committed).
func (h *Handler) sendPurchaseScheduledEmail(ctx context.Context, execution *config.PurchaseExecution, actor string) {
	if h.emailNotifier == nil {
		logging.Debug("sendPurchaseScheduledEmail: no email notifier configured, skipping")
		return
	}
	if execution.ExecutionID == "" {
		logging.Warn("sendPurchaseScheduledEmail: empty execution ID, skipping")
		return
	}

	globalCfg, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		logging.Errorf("sendPurchaseScheduledEmail: failed to load global config: %v", err)
		return
	}

	if sendErr := purchase.SendScheduledNotice(ctx, h.emailNotifier, globalCfg, h.dashboardURL, execution, actor); sendErr != nil {
		logging.Errorf("sendPurchaseScheduledEmail: %v", sendErr)
	}
}

// requireDifferentApprover enforces the 4-eyes approval policy (issue #1005).
// It is called on every approve path (session-authed and email-token) after
// the standard RBAC gate. Returns nil when the policy allows the approval.
//
// Decision logic:
//   - mode off (RequireDifferentApprover == false): always returns nil (default behavior preserved).
//   - session == nil AND mode on: returns 500 (fail-closed; we cannot determine
//     identity without a session; the email-token path should always carry a
//     session when mode is on because the deep-link flow forces a login).
//   - session present AND creatorUserID == nil: returns 403 with a
//     targeted message explaining that the legacy row predates dual-control and
//     an admin must disable 4-eyes mode to proceed.
//   - session present AND session.UserID == *creatorUserID: returns 403.
//   - session present AND session.UserID != *creatorUserID: returns nil.
//
// Admin wildcard is intentionally NOT exempt: an admin who created an execution
// must disable the mode in Settings before approving their own row.
func (h *Handler) requireDifferentApprover(ctx context.Context, session *Session, subjectID string, creatorUserID *string, systemCreated bool) error {
	cfg, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		return fmt.Errorf("4-eyes policy check: failed to load global config: %w", err)
	}
	if cfg == nil || !cfg.RequireDifferentApprover {
		return nil
	}

	// Mode is on.
	if session == nil {
		logging.Warnf("4-eyes[%s]: mode on but no session available; denying (fail-closed)", subjectID)
		return NewClientError(500, "4-eyes approval mode is enabled but no session could be resolved; sign in before approving")
	}

	if creatorUserID == nil && systemCreated {
		// Scheduler-minted row: no human requester exists, so any identified
		// approver (session != nil, checked above) is the second pair of eyes.
		logging.Infof("4-eyes[%s]: mode on; system-created row approved by user %s", subjectID, session.UserID)
		return nil
	}

	if creatorUserID == nil {
		logging.Warnf("4-eyes[%s]: mode on; NULL creator (legacy row) attempted by user %s, denied",
			subjectID, session.UserID)
		return NewClientError(403, "approval declined: this execution predates the dual-control feature and has no recorded creator; an admin must disable 4-eyes mode to approve")
	}

	if session.UserID == *creatorUserID {
		logging.Warnf("4-eyes[%s]: mode on; creator %s attempted self-approval, denied",
			subjectID, session.UserID)
		return NewClientError(403, "approval declined: 4-eyes mode requires a different approver than the requester")
	}

	return nil
}

// authorizeSessionExecuteDirect returns nil when the session is permitted to
// bypass the approval email and execute a purchase immediately under the
// execute-any / execute-own RBAC rules added in issue #289.
// Returns a 403 ClientError otherwise.
//
// creatorID is the creator of the execution being submitted (resolved via
// resolveCreatorUserID before this call; "" on non-human or legacy rows).
// recs is the execution's (store-priced) recommendation set, used to build
// the same per-permission Constraints sets validateExecutePurchaseRequest
// already enforces against the base execute:purchases permission (issue
// #60): execute-any/execute-own are DISTINCT permissions from execute, so a
// session capped tighter on execute-any than on execute could otherwise
// direct-execute past that tighter cap by holding both verbs.
//
// Gate logic (mirrors authorizeSessionApprove / authorizeSessionCancel):
//   - stateless admin API key: always permitted (apiKeyAdminUserID sentinel).
//   - execute-any: permitted when the execute-any permission's own
//     Constraints also allow this batch, regardless of creator.
//     Administrators-group users pass the verb check here because
//     {admin, *} matches ActionExecuteAny.
//   - execute-own: permitted only when creatorID == session.UserID (both
//     non-empty) AND the execute-own permission's own Constraints allow
//     this batch.
//   - no matching grant, or a constraint denial: 403 fail-closed; nil auth
//     component is a 500 as per feedback_fail_closed_middleware.md.
func (h *Handler) authorizeSessionExecuteDirect(ctx context.Context, session *Session, creatorID string, recs []config.RecommendationRecord) error {
	// Stateless admin API key: full access, no user row. Administrators-group
	// users pass via the execute-any check below, since
	// {admin, *} matches any requested permission.
	if session.UserID == apiKeyAdminUserID {
		return nil
	}
	if h.auth == nil {
		return NewClientError(500, "authentication service not configured")
	}

	// Both verbs are probed on the principal enforcePurchaseConstraints
	// judges: the key (intersected with its owner) for a user API key, so a
	// key holding execute-own is not routed to its owner's execute-any and
	// then refused on the key's (issue #1005).
	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionExecuteAny)
	if err != nil {
		return err
	}
	if hasAny {
		return h.enforcePurchaseConstraints(ctx, session, auth.ActionExecuteAny, recs)
	}
	return h.authorizeExecuteOwnDirect(ctx, session, creatorID, recs)
}

// authorizeExecuteOwnDirect is the execute-own branch of
// authorizeSessionExecuteDirect, reached only after the admin, nil-auth and
// execute-any gates. Its gates keep authorizeSessionExecuteDirect's order:
// verb held, then creator match, then the execute-own Constraints.
func (h *Handler) authorizeExecuteOwnDirect(ctx context.Context, session *Session, creatorID string, recs []config.RecommendationRecord) error {
	hasOwn, err := h.sessionHoldsVerb(ctx, session, auth.ActionExecuteOwn)
	if err != nil {
		return err
	}
	if !hasOwn {
		return NewClientError(403, "permission denied: requires execute-any or execute-own on purchases")
	}

	// execute-own: both IDs must be non-empty and must match (empty-string
	// collision would otherwise grant access to any null-creator row).
	if session.UserID == "" || creatorID == "" || creatorID != session.UserID {
		return NewClientError(403, "permission denied: execute-own requires you to be the creator of this purchase")
	}
	return h.enforcePurchaseConstraints(ctx, session, auth.ActionExecuteOwn, recs)
}

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

// revokeViaEmailToken is the one-click revocation handler embedded in the
// post-execution notification email (issue #291). It accepts the same
// three-mode dispatch shape as approvePurchase / cancelPurchase:
//
//  1. Session present AND RBAC-authorized (admin / cancel-any /
//     cancel-own) → session-authed path.
//  2. token != "" → token-authed path via the email one-click link.
//  3. No token, no qualifying session → 401.
//
// The revocation window check is intentionally limited in this
// iteration: because the sibling "AWS RI/SP revocation via support case"
// issue has not yet landed its dedicated revocation_window_closes_at
// column, revokeViaEmailToken reuses the ApprovalToken. Once the sibling lands
// and adds a RevocationToken + RevocationWindowClosesAt column, this
// handler should validate RevocationWindowClosesAt here before
// attempting the cancellation.
//
// At this scope the revoke action is equivalent to a cancel on a
// completed/completed execution — the underlying cloud revocation
// (AWS support-case path) is out of scope for this issue and is handled
// by the sibling "AWS RI/SP revocation" issue.
//
// Present-day behavior: calling this route within the AWS revocation
// window requests the cancellation and returns {"status":"revocation_requested"}.
// Past the window it returns a friendly 409 with a plain-language message
// rather than a stack trace.
// revokeConfirmPageTmpl is the minimal HTML form rendered on GET /revoke.
// The form POSTs the token from a hidden input so the actual mutation never
// fires via a GET request (prevents email prefetchers from accidentally
// triggering the revocation).
const revokeConfirmPageTmpl = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>Confirm Revocation</title>
<style>body{font-family:sans-serif;max-width:480px;margin:60px auto;padding:0 16px}
.btn{display:inline-block;padding:10px 24px;background:#dc2626;color:#fff;border:none;
border-radius:4px;font-size:15px;cursor:pointer;text-decoration:none}
.btn:hover{background:#b91c1c}.note{color:#64748b;font-size:13px;margin-top:12px}</style>
</head>
<body>
<h2>Confirm Revocation</h2>
<p>You are about to request revocation for purchase execution <code>{{.ExecutionID}}</code>.</p>
<p>This will record a revocation request. Contact AWS Support to complete the
cancellation within the allowed window.</p>
<form method="POST" action="/api/purchases/revoke/{{.ExecutionID}}">
  <input type="hidden" name="token" value="{{.Token}}">
  <button type="submit" class="btn">Confirm Revoke</button>
</form>
<p class="note">If you did not request this, ignore this page. No action has been taken.</p>
</body>
</html>`

func (h *Handler) revokeViaEmailToken(ctx context.Context, req *events.LambdaFunctionURLRequest, execID, token string) (any, error) {
	if err := validateUUID(execID); err != nil {
		return nil, err
	}

	// GET: render a confirmation page so email prefetchers cannot auto-trigger
	// the revocation. The token travels in the URL query string (unavoidable for
	// the email one-click link), but no mutation occurs on GET.
	if req.RequestContext.HTTP.Method == "GET" {
		return renderRevokeConfirmPage(execID, token), nil
	}

	if err := h.requireSessionBeforeLookup(ctx, req); err != nil {
		return nil, err
	}

	execution, err := h.getExecutionOr404(ctx, execID)
	if err != nil {
		return nil, err
	}

	// Three-mode dispatch — same shape as cancelPurchase.
	result, handled, revokeErr := h.tryRevokeViaSession(ctx, req, execution, token)
	if handled {
		return result, revokeErr
	}

	if token == "" {
		return nil, NewClientError(401, "sign in or use the revocation link from the notification email")
	}

	// One-click limitation (issue #291): the email link carries a token, but
	// authorizeApprovalAction derives the actor from the *session*
	// (tryResolveActorEmail), not from the token's bound contact email. A
	// recipient who clicks the link while logged out therefore gets a 401 and
	// must sign in with the matching contact email first. This intentionally
	// matches the existing approve/cancel email-link behavior -- we do not
	// widen the authz model here. True tokenless one-click (deriving the actor
	// from a single-use, contact-bound token) is deferred to the sibling
	// "AWS RI/SP revocation" work, which adds a dedicated revocation token.
	return h.revokeViaToken(ctx, req, execution, token)
}

// revokeViaToken is the token-authed tail of revokeViaEmailToken: confirm the
// caller is the authorized contact email, validate the token (reusing the
// approval token for this iteration), then check the status. Same gate order
// as before extraction.
func (h *Handler) revokeViaToken(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution, token string) (any, error) {
	actor, err := h.authorizeApprovalAction(ctx, req, execution)
	if err != nil {
		return nil, concealExecutionDenial(err)
	}
	if err := validateRevokeToken(execution, token); err != nil {
		return nil, err
	}
	if err := checkRevokableStatus(execution); err != nil {
		return nil, err
	}
	return h.revokeViaSession(ctx, execution, actor, resolveCreatorUserID(h.tryGetSession(ctx, req)))
}

// getExecutionOr404 loads an execution, answering 404 for a missing one
// (ErrNotFound or a nil row) and leaving real store errors as 500s.
func (h *Handler) getExecutionOr404(ctx context.Context, execID string) (*config.PurchaseExecution, error) {
	execution, err := h.config.GetExecutionByID(ctx, execID)
	if errors.Is(err, config.ErrNotFound) || (err == nil && execution == nil) {
		return nil, executionNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get execution: %w", err)
	}
	return execution, nil
}

// renderRevokeConfirmPage returns a rawResponse containing the HTML
// confirmation form for GET /revoke. The form POSTs the token so the actual
// mutation only happens when the user explicitly clicks "Confirm Revoke".
func renderRevokeConfirmPage(execID, token string) *rawResponse {
	// Manual substitution to avoid importing html/template just for this one
	// small page. The values substituted here (execID and token) are both
	// treated as opaque strings — execID is a UUID (hex+hyphens only),
	// token is a generated random string. HTML-escape them defensively anyway.
	escaped := func(s string) string {
		s = strings.ReplaceAll(s, "&", "&amp;")
		s = strings.ReplaceAll(s, "<", "&lt;")
		s = strings.ReplaceAll(s, ">", "&gt;")
		s = strings.ReplaceAll(s, "\"", "&#34;")
		return s
	}
	page := strings.ReplaceAll(revokeConfirmPageTmpl, "{{.ExecutionID}}", escaped(execID))
	page = strings.ReplaceAll(page, "{{.Token}}", escaped(token))
	return &rawResponse{
		contentType: "text/html; charset=utf-8",
		body:        page,
	}
}

// tryRevokeViaSession attempts the session-authenticated branch of the
// revokeViaEmailToken three-mode dispatch (same shape as the session branch of
// cancelPurchase). Returns (result, true, err) when the session was present
// and either completed the revocation or encountered a hard error; returns
// (nil, false, nil) when the session was absent or returned a permission-denied
// error so the caller can fall through to the token branch. Extracted from
// revokeViaEmailToken to keep that function under the cyclomatic limit.
func (h *Handler) tryRevokeViaSession(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution, token string) (result any, handled bool, err error) {
	session := h.tryGetSession(ctx, req)
	if session == nil {
		return nil, false, nil
	}
	switch sessErr := h.authorizeCancelSession(ctx, session, execution); {
	case sessErr == nil:
		// These endpoints are AuthPublic so the outer middleware skips CSRF.
		// Enforce it here for the session-authed revoke sub-path, consistent
		// with cancelPurchaseViaSession and approvePurchaseViaSession which both
		// validate CSRF before mutating state (nit #1).
		if csrfErr := h.validateCSRF(ctx, req); csrfErr != nil {
			return nil, true, NewClientError(403, "CSRF validation failed")
		}
		// Status is checked only once the caller is authorized, so an
		// out-of-scope caller cannot learn it from a 409.
		if statusErr := checkRevokableStatus(execution); statusErr != nil {
			return nil, true, statusErr
		}
		res, revokeErr := h.revokeViaSession(ctx, execution, session.Email, resolveCreatorUserID(session))
		return res, true, revokeErr
	case fallsThroughToToken(sessErr, token):
		// Fall through to the token branch.
		return nil, false, nil
	default:
		// A final denial answers like a missing execution (issue #597).
		return nil, true, concealExecutionDenial(sessErr)
	}
}

// checkRevokableStatus returns nil when the execution is in a state that allows
// revocation (completed or partially_completed), or a 409 ClientError when the
// status makes revocation impossible. Extracted from revokeViaEmailToken to keep
// that function under the cyclomatic limit.
func checkRevokableStatus(execution *config.PurchaseExecution) error {
	switch execution.Status {
	case "completed", "partially_completed":
		return nil
	case "pending", "notified":
		return NewClientError(409, fmt.Sprintf(
			"execution %s is still pending — use the Cancel link instead of Revoke", execution.ExecutionID))
	default:
		return NewClientError(409, fmt.Sprintf(
			"execution %s cannot be revoked (status=%s); the revocation window may have closed or the purchase was not completed",
			execution.ExecutionID, execution.Status))
	}
}

// validateRevokeToken checks the revocation token with the same status
// mapping as the approve and cancel links (tokenActionError, issue #587): a
// missing or non-matching token is 403 and a matching but expired token is
// 410. Pre-migration rows without an ApprovalTokenExpiresAt pass the expiry
// check (legacy backward compat). Last, the 24-hour revocation window must
// still be open (config.RevocationWindow after CompletedAt).
//
// The token is compared first, as on the approve path: it shares
// purchase.ValidateApprovalToken, which matches the token (in constant time,
// against the SHA-256 digest stored at rest, issue #103) before it reads the
// expiry. A wrong token must get the same "invalid" answer whatever the row's
// expiry or window, or the expiry/window message tells a token guesser that
// the link exists and has lapsed (issue #1190).
// Extracted from revokeViaEmailToken to keep that function under the cyclomatic limit.
func validateRevokeToken(execution *config.PurchaseExecution, token string) error {
	switch err := purchase.ValidateApprovalToken(execution, token); {
	case errors.Is(err, purchase.ErrInvalidApprovalToken):
		return NewClientError(403, "invalid revocation token")
	case errors.Is(err, purchase.ErrApprovalTokenExpired):
		return NewClientError(410, fmt.Sprintf(
			"execution %s cannot be revoked: the revocation link has expired", execution.ExecutionID))
	case err != nil:
		return err
	}
	// Enforce the 24-hour revocation window (issue #291 Finding #6).
	// Use CompletedAt as the reference timestamp; fall back to ExecutedAt
	// for direct-execute rows which may not have a CompletedAt yet.
	return checkRevocationWindow(execution)
}

// checkRevocationWindow returns a 403 ClientError when the 24-hour revocation
// window has passed. The reference timestamp is CompletedAt if set, otherwise
// ExecutedAt. When neither is set (legacy rows without these columns) the
// check is skipped to preserve backward compatibility.
func checkRevocationWindow(execution *config.PurchaseExecution) error {
	var ref *time.Time
	if execution.CompletedAt != nil {
		ref = execution.CompletedAt
	} else if execution.ExecutedAt != nil {
		ref = execution.ExecutedAt
	}
	if ref == nil {
		// Legacy row: no timestamp to anchor the window; allow the revoke.
		return nil
	}
	windowCloses := ref.Add(config.RevocationWindow)
	if time.Now().After(windowCloses) {
		return NewClientError(403, fmt.Sprintf(
			"revocation window has closed (window closed at %s)", windowCloses.UTC().Format(time.RFC3339)))
	}
	return nil
}

// revocationWindowClosesAt returns the human-readable UTC string at which the
// 24-hour revocation window closes. Used by sendPurchaseExecutedEmail to
// populate the RevocationWindowClosesAt field in the email template.
// Returns "" when no reference timestamp is available (legacy rows).
func revocationWindowClosesAt(execution *config.PurchaseExecution) string {
	var ref *time.Time
	if execution.CompletedAt != nil {
		ref = execution.CompletedAt
	} else if execution.ExecutedAt != nil {
		ref = execution.ExecutedAt
	}
	if ref == nil {
		return ""
	}
	return ref.Add(config.RevocationWindow).UTC().Format("2006-01-02 15:04 UTC")
}

// revokeViaSession performs the post-execution revocation action by recording
// a "revocation_requested" status on the execution. Actual cloud-side
// revocation (AWS support-case) is out of scope for issue #291 — it is
// handled by the sibling "AWS RI/SP revocation via support case" issue.
// This call records the intent so the History UI can surface it.
//
// The status flip uses TransitionExecutionStatus, which issues a conditional
// UPDATE WHERE status IN ('completed','partially_completed'). This guards
// against lost-update races: if the row changed between the GetExecutionByID
// read and this write (e.g. a concurrent transition), zero rows are affected
// and we return a 409 rather than blindly overwriting. CancelledBy is then
// stamped by a narrow SetCancelledBy UPDATE keyed on the execution ID, so the
// row TransitionExecutionStatus returns is not used.
//
// transitionedBy is the acting user's UUID (resolveCreatorUserID), or nil when
// the caller has no user row. It must never be an email: transitioned_by is a
// UUID foreign key. revokedBy is the email shown as the requester.
//
// The revocation window is enforced here, not only in validateRevokeToken, so
// the session-authed branch (tryRevokeViaSession) cannot flip an execution
// whose window closed long ago to revocation_requested (issue #260).
//
// Neither revoke path (this one or the token path in revokePurchase) applies
// the 4-eyes RequireDifferentApprover policy: that policy gates approvals
// only, and a revocation records a request that an operator completes with
// AWS Support. Whether revocation should also need a second person is an
// open owner decision (issue #311).
func (h *Handler) revokeViaSession(ctx context.Context, execution *config.PurchaseExecution, revokedBy string, transitionedBy *string) (any, error) {
	if err := checkRevocationWindow(execution); err != nil {
		return nil, err
	}
	_, err := h.config.TransitionExecutionStatus(
		ctx, execution.ExecutionID,
		[]string{"completed", "partially_completed"}, "revocation_requested", transitionedBy)
	if err != nil {
		if errors.Is(err, config.ErrExecutionNotInExpectedStatus) {
			return nil, NewClientError(409, fmt.Sprintf(
				"execution %s cannot be revoked: a concurrent operation changed its status", execution.ExecutionID))
		}
		if errors.Is(err, config.ErrNotFound) {
			return nil, NewClientError(404, "execution not found")
		}
		return nil, fmt.Errorf("failed to record revocation request for execution %s: %w", execution.ExecutionID, err)
	}
	if revokedBy != "" {
		// Use a narrow SetCancelledBy UPDATE rather than a full-row
		// SavePurchaseExecution to avoid clobbering concurrent writes
		// between the TransitionExecutionStatus and this attribution step
		// (Finding #5). The targeted UPDATE only touches the cancellation
		// attribution column so any other concurrent column change (e.g. a
		// background webhook stamping a cloud reference) is preserved.
		if err := h.config.SetCancelledBy(ctx, execution.ExecutionID, revokedBy); err != nil {
			return nil, fmt.Errorf("failed to record revocation requester for execution %s: %w", execution.ExecutionID, err)
		}
	}
	logging.Infof("Revocation requested for execution %s by %s", execution.ExecutionID, redactEmail(revokedBy))
	return map[string]string{
		"status":  "revocation_requested",
		"message": "Revocation request recorded. Contact AWS Support to complete the cancellation within the allowed window.",
	}, nil
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

// requireSession validates the request's session token and returns the
// session, or a 401 ClientError when no/invalid session is present. Unlike
// requirePermission, it does NOT consult the admin-API-key shortcut — the
// session-authed cancel path needs an actual user UUID for the
// cancel-own check; falling through to the API-key admin role would let
// a key impersonate ownership we cannot verify.
func (h *Handler) requireSession(ctx context.Context, req *events.LambdaFunctionURLRequest) (*Session, error) {
	return h.requireSessionPrincipal(ctx, req)
}

// retryThreshold is the number of attempts after which a retry is
// soft-blocked and requires `?force=true` to override (issue #47, Q2).
// Five was picked to be generous enough to absorb transient SES /
// recipient-mailbox blips (which usually clear within a couple of
// attempts) while still surfacing genuinely-stuck deployments before
// they accumulate dozens of dead retry rows.
const retryThreshold = 5

// retryPurchase creates a new execution from a *failed* execution's
// stored Recommendations slice (issue #47). The original failed row
// keeps its `failed` status as a historical record and gains a
// retry_execution_id pointer to the successor; the new row inherits
// retry_attempt_n = predecessor.retry_attempt_n + 1.
//
// Permission gate (mirrors authorizeSessionCancel from #46):
//   - retry-any → may retry any failed row.
//   - retry-own AND failedExec.created_by_user_id == session.user_id
//     → may retry their own failed row.
//   - else → 403.
//
// Constraint gate (SEC-01, issue #1141, adversarial review follow-up to
// #1210): the retried recommendations must ALSO satisfy the retrying
// session's execute:purchases permission Constraints (MaxPurchaseAmount,
// Providers, Services, Regions, AccountIDs), exactly as a fresh
// executePurchase call would enforce. Without this gate a retry-any
// session could replay another user's over-cap failed purchase, and a
// permission tightened after the original submission would never apply
// to a replay.
//
// State gate:
//   - failedExec.Status must be "failed" → 409 otherwise.
//   - a plan-step row (PlanID set) must carry at least one recommendation
//     → 409 otherwise, nothing to buy (platform#609).
//   - every rec must be safe to re-drive per purchase.RetryRefusalReason
//     (a proven-unsent attempt-0 EC2 or Azure savings-plan row is lifted,
//     #1803) → 409 with ops_hint + redrive_unsafe when any is not, NOT
//     overridable by ?force=true (issue #1668).
//   - failedExec.Error must NOT match the persistent-failure map →
//     409 with ops_hint when it does (Q3).
//   - failedExec.RetryAttemptN < retryThreshold OR ?force=true → soft
//     block at threshold (Q2).
//
// Atomicity:
//
//	A single tx writes (a) the new execution row and (b) the linkage
//	on the original failed row. Suppressions for the new execution are
//	created in the same tx. A crash between any two writes leaves
//	either everything or nothing — no orphaned successor without a
//	predecessor pointer, no dangling pointer to a non-existent row.
func (h *Handler) retryPurchase(ctx context.Context, req *events.LambdaFunctionURLRequest, execID string) (any, error) {
	failedExec, session, err := h.loadAndValidateRetryRequest(ctx, req, execID)
	if err != nil {
		return nil, err
	}

	// A plan step with no recommendations buys nothing and never can until plan
	// steps carry recommendations (platform#609); retrying it would only mint a
	// successor that fails the same way.
	if failedExec.PlanID != "" && len(failedExec.Recommendations) == 0 {
		return nil, NewClientError(409,
			"this plan step has no recommendations attached, so retrying it would buy nothing")
	}

	totalUpfront, totalSavings, err := validateAndTotalRecommendations(failedExec.Recommendations)
	if err != nil {
		return nil, err
	}

	// Enforce the per-permission Constraints (MaxPurchaseAmount, Providers,
	// Services, Regions, AccountIDs) configured on the retrying session's
	// execute:purchases permission (SEC-01, issue #1141), mirroring the gate
	// in validateExecutePurchaseRequest. Without this, retry-any could
	// relaunch another user's over-cap failed purchase that the retrying
	// session's OWN constraints would deny, and constraints tightened after
	// the original submission would never apply on replay (adversarial
	// review follow-up to #1210). failedExec.Recommendations carry the
	// store-derived prices stamped at submit time
	// (priceRecommendationsFromStore), so this re-check never sees
	// client-supplied numbers.
	if constraintErr := h.enforcePurchaseConstraints(ctx, session, "execute", failedExec.Recommendations); constraintErr != nil {
		return nil, constraintErr
	}

	newExecution, rawApprovalToken, err := h.persistRetryExecution(ctx, failedExec, session, totalUpfront, totalSavings)
	if err != nil {
		return nil, err
	}

	// Send the approval email outside the tx — same pattern as
	// executePurchase. Email failures flip the new execution to
	// `failed` so the user sees the reason in History; the linkage on
	// the original row is unaffected (it points at the failed-again
	// successor, which is exactly the audit trail we want).
	return h.sendRetryApprovalAndFinalize(ctx, req, failedExec, newExecution, rawApprovalToken, totalUpfront, totalSavings)
}

// sendRetryApprovalAndFinalize is retryPurchase's counterpart of
// sendApprovalAndFinalize: it sends the successor's approval email, records a
// send failure on the row, and builds the retry response.
func (h *Handler) sendRetryApprovalAndFinalize(ctx context.Context, req *events.LambdaFunctionURLRequest, failedExec, newExecution *config.PurchaseExecution, rawApprovalToken string, totalUpfront, totalSavings float64) (any, error) {
	emailSent, emailReason, recipient := h.sendPurchaseApprovalEmail(ctx, req, newExecution, rawApprovalToken, failedExec.Recommendations, totalUpfront, totalSavings)
	status, err := h.finalizePurchaseStatus(ctx, newExecution, emailSent, emailReason)
	if err != nil {
		return nil, err
	}

	resp := map[string]any{
		"execution_id":         newExecution.ExecutionID,
		"original_execution":   failedExec.ExecutionID,
		"status":               status,
		"recommendation_count": len(failedExec.Recommendations),
		"total_upfront_cost":   totalUpfront,
		"estimated_savings":    totalSavings,
		"email_sent":           emailSent,
		"retry_attempt_n":      newExecution.RetryAttemptN,
	}
	if emailReason != "" {
		resp["email_reason"] = emailReason
	}
	if recipient != "" {
		resp["approval_recipient"] = recipient
	}
	return resp, nil
}

// loadAndValidateRetryRequest fetches the failed execution, the
// session, and runs every gate (status, already-retried, RBAC,
// persistent-failure, threshold) before returning a green-light pair.
// Extracted from retryPurchase to keep that function under the
// cyclomatic-complexity ceiling; the gates remain in the order
// documented on retryPurchase so the security boundary is the same.
func (h *Handler) loadAndValidateRetryRequest(ctx context.Context, req *events.LambdaFunctionURLRequest, execID string) (*config.PurchaseExecution, *Session, error) {
	if err := validateUUID(execID); err != nil {
		return nil, nil, err
	}

	failedExec, err := h.config.GetExecutionByID(ctx, execID)
	if errors.Is(err, config.ErrNotFound) {
		return nil, nil, NewClientError(404, "execution not found")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get execution: %w", err)
	}

	session, err := h.requireSession(ctx, req)
	if err != nil {
		return nil, nil, err
	}

	if err := h.authorizeRetrySession(ctx, session, failedExec); err != nil {
		return nil, nil, err
	}

	// Already-retried guard: a row with retry_execution_id set was
	// already retried into a successor. Retrying it AGAIN would
	// silently overwrite the linkage pointer and orphan the previous
	// chain, breaking History attribution. Surfaced only after RBAC
	// so the descendant ID isn't a cross-user info leak.
	if failedExec.RetryExecutionID != nil && *failedExec.RetryExecutionID != "" {
		return nil, nil, NewClientErrorWithDetails(409,
			fmt.Sprintf("execution %s was already retried; act on its descendant instead", failedExec.ExecutionID),
			map[string]any{"retry_execution_id": *failedExec.RetryExecutionID})
	}

	if err := checkRetryEligibilityGates(failedExec, req); err != nil {
		return nil, nil, err
	}
	if err := h.checkRedshiftRetryGate(ctx, failedExec); err != nil {
		return nil, nil, err
	}

	return failedExec, session, nil
}

// authorizeRetrySession runs the account-scope gate (issue #92), the status
// guard, and the retry-any/retry-own RBAC matrix, in that order. Extracted
// from loadAndValidateRetryRequest to keep that function under the
// cyclomatic-complexity ceiling; the order is preserved from the original
// inline sequence, so the security boundary is unchanged.
func (h *Handler) authorizeRetrySession(ctx context.Context, session *Session, failedExec *config.PurchaseExecution) error {
	// Account-scope gate (issue #92): retry relied only on the retry-any/
	// retry-own RBAC matrix below and never consulted allowed_accounts. Runs
	// before the status check so an out-of-scope caller learns nothing about
	// the execution, matching the 404 a scoped-out session already gets from
	// the History endpoint for the same row.
	if err := h.requireExecutionAccess(ctx, session, failedExec.ExecutionID); err != nil {
		return err
	}
	if failedExec.Status != "failed" {
		return NewClientError(409, fmt.Sprintf("execution %s cannot be retried (status=%s)", failedExec.ExecutionID, failedExec.Status))
	}
	// Authorize BEFORE the already-retried guard so an unauthorized
	// caller can't enumerate descendant execution IDs by probing
	// failed-row UUIDs (CR #168 review). The status check above is
	// allowed pre-RBAC because non-admins can already see the row's
	// status via the History endpoint they're entitled to read.
	return h.authorizeSessionRetry(ctx, session, failedExec)
}

// checkRetryEligibilityGates runs the provider re-drive-safety (issue
// #1668), persistent-failure (Q3) and retry-attempt-threshold (Q2)
// gates and returns the appropriate 409 ClientError when any fires.
// Extracted from loadAndValidateRetryRequest to keep that function
// under the cyclomatic-complexity ceiling without flattening the gate
// sequence.
func checkRetryEligibilityGates(failedExec *config.PurchaseExecution, req *events.LambdaFunctionURLRequest) error {
	// Provider re-drive safety (issue #1668). A "failed" row may in fact
	// have landed its commitment at the provider (a timeout, a lost
	// response, a post-purchase write that failed), so every retry is
	// potentially a re-drive. purchase.RetryRefusalReason is the reaper's
	// RedriveRefusalReason minus the EC2 and Azure savings-plans refusals for a
	// row proven to have sent nothing (no_submission at attempt 0). The
	// remaining reasons exist exactly when the provider offers nothing that
	// would collapse the second attempt onto the first: AWS EC2 (no client
	// token; a lost response is reported as outcome unknown), Azure
	// savings-plans (no server-side idempotency key, no tag-based lookup) plus
	// any provider the predicate does not recognize. A failed EC2 or Azure
	// savings-plans row that may have sent therefore cannot be retried; the
	// operator checks the account and starts a new purchase. Redshift is gated
	// by checkRedshiftRetryGate.
	// If plan steps ever carry recommendations (platform#609), a failed EC2
	// step will be un-retryable too and needs its own recovery path.
	//
	// This gate runs FIRST and, unlike the threshold below, ?force=true
	// does NOT override it: a savings plan cannot be canceled, so there
	// is no recovery from getting this wrong, and a duplicate is not what
	// the operator clicking Retry is asking for. An operator who genuinely
	// wants a second commitment can submit a fresh purchase, which is an
	// explicit buy rather than a retry of one that may already exist.
	//
	// A refusal here is permanent: nothing about the row can change to make
	// it retryable. So the gate must fire only where the duplicate hazard is
	// real. An execution carrying no recommendations buys nothing and cannot
	// double-buy, so the predicate stays silent on the empty case. The
	// rec-less plan steps that createPurchaseExecutionsTx (handler_plans.go) and
	// getOrCreateExecution (purchase/notifications.go) used to create are
	// refused earlier in this handler instead (platform#609), because a retry of
	// one can only fail the same way. Empty here always means empty as created,
	// never "we could not load them": GetExecutionByID propagates a
	// recommendations unmarshal failure as an error (config/store_postgres.go),
	// which this handler has already turned into a 500 well before this gate.
	if reason := purchase.RetryRefusalReason(failedExec); reason != "" {
		return NewClientErrorWithDetails(409,
			"this purchase cannot be retried safely: "+reason,
			// ops_hint reuses the key the History UI already renders in
			// place of the Retry button, so the reason reaches the operator
			// today. redrive_unsafe distinguishes this PERMANENT refusal from
			// the operator-fixable hints below, which clear once the
			// configuration is fixed.
			//
			// History also sets redrive_unsafe on the row (handler_history.go)
			// to hide Retry. That is presentation only. This refusal is
			// enforced here, server-side, and does not depend on any client
			// honoring it.
			map[string]any{"ops_hint": reason, "redrive_unsafe": true})
	}

	// Persistent-failure block (Q3). Surfaces the ops_hint via the
	// API for stale-cache callers; the History UI already shows it
	// inline in place of the Retry button.
	if hint := purchase.ResolveOpsHint(failedExec.Error); hint != "" {
		return NewClientErrorWithDetails(409,
			"this failure is operator-fixable; retrying without changing configuration will fail again",
			map[string]any{"ops_hint": hint, "failure_reason": purchase.SanitizeStoredError(failedExec.Error)})
	}

	// Threshold soft-block (Q2). force=true (set by the frontend
	// confirm-with-warning) skips the block but still increments the
	// chain so the next retry is gated by the same threshold against
	// the new attempt count.
	force := strings.EqualFold(req.QueryStringParameters["force"], "true")
	if !force && failedExec.RetryAttemptN >= retryThreshold {
		return NewClientErrorWithDetails(409,
			fmt.Sprintf("execution %s has been retried %d times already; pass ?force=true to override", failedExec.ExecutionID, failedExec.RetryAttemptN),
			map[string]any{"retry_attempt_n": failedExec.RetryAttemptN, "threshold": retryThreshold})
	}

	return nil
}

// clonePtr returns a pointer to a copy of *p, or nil when p is nil, so a
// value copied between two records never leaves them aliasing the same
// pointee. nil is preserved rather than collapsed to a zero value: on
// PurchaseExecution.CloudAccountID nil means "no account scope" (a root or
// ambient-credentials execution) and is not interchangeable with "".
func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// persistRetryExecution builds the successor PurchaseExecution and
// writes it + the predecessor linkage + suppressions in a single tx.
// Returns the populated newExecution so the caller can send the
// approval email and synthesize the response. Extracted from
// retryPurchase to keep that function under the cyclomatic-complexity
// ceiling; tx ordering and the slice-aliasing fix live here.
//
// Also returns the RAW approval token: newExecution.ApprovalToken holds
// only the SHA-256 hash that gets persisted (issue #103), so the caller's
// synchronous approval email (sendPurchaseApprovalEmail) must take the raw
// value from this second return instead.
func (h *Handler) persistRetryExecution(ctx context.Context, failedExec *config.PurchaseExecution, session *Session, totalUpfront, totalSavings float64) (*config.PurchaseExecution, string, error) {
	// Defensive deep copy of the recommendations slice. Sharing the
	// backing array with failedExec.Recommendations would let the
	// downstream purchase pipeline (purchase.Manager.purchaseRecommendations
	// at internal/purchase/execution.go) mutate per-element fields
	// (.Error / .Purchased / .PurchaseID) on the *original failed row's*
	// in-memory representation as well — corrupting the historical
	// record any caller still holding the failedExec pointer would see.
	// The DB rows are isolated (each row JSON-marshals its own copy),
	// but in-process aliasing across a "historical" row and its
	// "successor" is a footgun we want to remove at the source.
	copiedRecs := append([]config.RecommendationRecord(nil), failedExec.Recommendations...)

	// The successor is stamped with the retrying session's creator below. A
	// creator-less successor under 4-eyes could never be approved, and the
	// linkage written in the same tx would also block retrying the original
	// again, so refuse before anything is written (issue #677).
	if err := h.requireAttributablePurchaseCreator(ctx, session); err != nil {
		return nil, "", err
	}

	// Generate a crypto/rand-backed approval token for the retry execution
	// (issue #408). uuid.New().String() was used here previously, which only
	// provides 122 bits of entropy in a known format; common.GenerateApprovalToken
	// provides 256 bits of uniform randomness, matching every other creation site.
	approvalToken, err := common.GenerateApprovalToken()
	if err != nil {
		return nil, "", fmt.Errorf("generate approval token for retry: %w", err)
	}
	tokenExpiresAt := time.Now().Add(config.ApprovalTokenTTL)

	newExecutionID := uuid.New().String()
	// Carry the predecessor's stable idempotency lineage key onto the retry
	// successor VERBATIM (issue #1012) so the per-rec provider token is
	// reproduced and a re-drive of a "failed" execution whose commitment
	// actually landed short-circuits at the provider instead of double-buying.
	// Legacy predecessors (persisted before migration 000066) carry no key —
	// seed the successor's key from the predecessor's ExecutionID, which is what
	// the original attempt's token derived from, so the match still holds.
	idempotencyKey := failedExec.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = failedExec.ExecutionID
	}
	// PlanID + StepNumber propagate from the predecessor so a retried
	// planned execution stays attributed to its plan + ramp step (CR
	// #168 review). For ad-hoc executions PlanID is "" and StepNumber
	// is 0, so propagation is a no-op for the non-plan case.
	//
	// CloudAccountID propagates for the same reason, and it is load-bearing on
	// the money path (issue #1537). A multi-account plan fans out into one
	// purchase_executions row per cloud account, each stamped with
	// CloudAccountID plus a per-account idempotency key "<root-key>:<acctID>"
	// (purchase.executeForAccount). Those per-account rows reach
	// status="failed" on their own and are therefore retryable. Dropping
	// CloudAccountID here left the successor with a nil account scope, which
	// purchase.executePurchase reads as "root execution" and re-fans-out across
	// EVERY account on the plan: the accounts that already committed were
	// purchased a SECOND time, under re-suffixed keys
	// "<root-key>:<failedAcctID>:<acctID>" whose derived tokens miss the AWS
	// ClientToken, the EC2 RI tag-guard and the Azure two-step lookup alike.
	// Carrying the scope keeps a per-account retry a single-account re-drive
	// against the identical lineage key, which is the precondition for the
	// provider-side dedupe to engage at all.
	//
	// Whether it then DOES engage is per-provider and not universal: most AWS
	// services, Azure reservations and GCP CUDs reproduce the token and
	// dedupe, but AWS EC2 takes no client token and Azure savings-plans has no
	// tag-based lookup, so a re-drive of a landed order of either may still
	// duplicate it. Scope propagation is therefore necessary for dedupe
	// everywhere and sufficient nowhere for those two.
	// purchase.RetryRefusalReason gates this handler on that exclusion
	// (checkRetryEligibilityGates, issue #1668) and lifts it only for a
	// proven-unsent first attempt, which cannot have landed.
	//
	// Copied by value rather than by pointer so the successor and the
	// historical failed row never share a *string, matching the defensive
	// deep copy of Recommendations above.
	cloudAccountID := clonePtr(failedExec.CloudAccountID)
	newExecution := &config.PurchaseExecution{
		ExecutionID:            newExecutionID,
		IdempotencyKey:         idempotencyKey,
		PlanID:                 failedExec.PlanID,
		StepNumber:             failedExec.StepNumber,
		CloudAccountID:         cloudAccountID,
		Status:                 "pending",
		ScheduledDate:          time.Now(),
		Recommendations:        copiedRecs,
		TotalUpfrontCost:       totalUpfront,
		EstimatedSavings:       totalSavings,
		ApprovalToken:          config.HashApprovalToken(approvalToken),
		ApprovalTokenExpiresAt: &tokenExpiresAt,
		Source:                 common.PurchaseSourceWeb,
		CapacityPercent:        failedExec.CapacityPercent,
		CreatedByUserID:        resolveCreatorUserID(session),
		RetryAttemptN:          failedExec.RetryAttemptN + 1,
		FirstDispatchAt:        retrySuccessorFirstDispatchAt(failedExec),
	}

	graceCfg, err := h.gracePeriodConfig(ctx)
	if err != nil {
		return nil, "", err
	}
	suppressions := config.BuildSuppressions(failedExec.Recommendations, newExecutionID, graceCfg, time.Now())

	if err := h.persistRetrySuccessorTx(ctx, failedExec, newExecution, suppressions); err != nil {
		return nil, "", err
	}

	return newExecution, approvalToken, nil
}

// persistRetrySuccessorTx writes the three retry-tx changes: the new
// successor execution, the linkage pointer on the original failed row, and
// the successor's suppression rows. Extracted from persistRetryExecution to
// keep that function under the cyclomatic-complexity ceiling.
//
// Order matters: the FK on retry_execution_id requires the successor to
// exist before the original can point at it.
//
// The linkage write is an atomic CAS (LinkUnsentRetryExecutionAtomic when the
// no-guard refusal was lifted by the proven-unsent proof, LinkRetryExecutionAtomic
// otherwise; conditional on status='failed' AND retry_execution_id IS NULL), not a full-row
// upsert of a stale in-memory copy of the failed row. This is the
// authoritative, race-safe guard: the pre-tx already-retried check in
// loadAndValidateRetryRequest is only a fast-path optimization, since two
// concurrent retries of the same row can both read RetryExecutionID == nil
// before either commits. Whichever request's UPDATE runs second here
// matches zero rows and the tx rolls back (issue #220) instead of both
// persisting an approvable successor and reverting whatever the other
// writer changed on the original row.
//
// The successor is a web-source pending row for the retrying user, so it
// also goes through the submit dedupe (issue #760): see writeRetrySuccessorTx.
func (h *Handler) persistRetrySuccessorTx(ctx context.Context, failedExec, newExecution *config.PurchaseExecution, suppressions []config.PurchaseSuppression) error {
	if err := h.requireAZConfigSchema(ctx); err != nil {
		return err
	}
	err := h.config.WithTx(ctx, func(tx pgx.Tx) error {
		return h.writeRetrySuccessorTx(ctx, tx, failedExec, newExecution, suppressions)
	})
	if err == nil {
		return nil
	}
	var dup *retryDuplicateSubmitError
	switch {
	case errors.Is(err, errRetryLadderTrancheSettled):
		return NewClientErrorWithDetails(409,
			fmt.Sprintf("execution %s belongs to a ladder tranche that is already settled; retrying it could buy the same commitment twice", failedExec.ExecutionID),
			map[string]any{})
	case errors.Is(err, errRetryFamilyClaimed):
		return retryFamilyClaimedError(failedExec)
	case errors.Is(err, errRetryLinkRaceLost):
		return NewClientErrorWithDetails(409,
			fmt.Sprintf("execution %s was already retried or settled; reload it", failedExec.ExecutionID),
			map[string]any{})
	case errors.As(err, &dup):
		return NewClientErrorWithDetails(409,
			fmt.Sprintf("an identical purchase was submitted moments ago as execution %s; act on it instead of retrying %s", dup.existingID, failedExec.ExecutionID),
			map[string]any{"duplicate_execution_id": dup.existingID})
	}
	return fmt.Errorf("failed to save retry execution: %w", err)
}

// writeRetrySuccessorTx is persistRetrySuccessorTx's transaction body.
//
// Its first statement takes the retrying user's per-creator submit lock
// (ListRecentSubmitsTx), the same lock at the same point as
// persistExecutionAndSuppressions, so a retry and an identical fresh submit
// by that user serialize instead of both inserting a pending row. Taking it
// before the ladder tranche lock keeps one order everywhere: the submit path
// takes only this lock, and the reconciler never takes it. The rows read
// under the lock are checked for a duplicate of the successor only after the
// link CAS, so a lost retry race still reports "already retried".
func (h *Handler) writeRetrySuccessorTx(ctx context.Context, tx pgx.Tx, failedExec, newExecution *config.PurchaseExecution, suppressions []config.PurchaseSuppression) error {
	creatorID := derefStringOrEmpty(newExecution.CreatedByUserID)
	now := time.Now()
	recent, listErr := h.config.ListRecentSubmitsTx(ctx, tx, creatorID, now.Add(-purchaseIdempotencyWindow))
	if listErr != nil {
		return listErr
	}
	if err := h.guardLadderTrancheForRetry(ctx, tx, failedExec.IdempotencyKey); err != nil {
		return err
	}
	if err := h.guardRetryFamily(ctx, tx, failedExec); err != nil {
		return err
	}
	if err := h.config.SavePurchaseExecutionTx(ctx, tx, newExecution); err != nil {
		return err
	}
	linked, linkErr := h.linkRetrySuccessor(ctx, tx, failedExec, newExecution.ExecutionID)
	if linkErr != nil {
		return linkErr
	}
	if !linked {
		return errRetryLinkRaceLost
	}
	if err := checkRetryDuplicateSubmit(recent, creatorID, newExecution, now); err != nil {
		return err
	}
	for i := range suppressions {
		if err := h.config.CreateSuppressionTx(ctx, tx, &suppressions[i]); err != nil {
			return err
		}
	}
	return nil
}

// linkRetrySuccessor links the failed row to its successor. A row retryable
// only on its no-submission proof links through the CAS that re-checks the
// proof, so a row that stopped being one after the gate read it is not linked.
func (h *Handler) linkRetrySuccessor(ctx context.Context, tx pgx.Tx, failedExec *config.PurchaseExecution, successorID string) (bool, error) {
	if purchase.RedriveRefusalReason(failedExec) != "" && purchase.RetryRefusalReason(failedExec) == "" {
		return h.config.LinkUnsentRetryExecutionAtomic(ctx, tx, failedExec.ExecutionID, successorID)
	}
	return h.config.LinkRetryExecutionAtomic(ctx, tx, failedExec.ExecutionID, successorID)
}

// checkRetryDuplicateSubmit returns a *retryDuplicateSubmitError when recent
// (the creator's live web submits, read under the submit lock) already holds
// an execution with the successor's submit fingerprint. The successor's own
// row is not in recent: it was read before the successor was inserted. A
// fingerprint that cannot be derived fails the retry rather than skipping
// the check, since a skipped check is a possible double purchase.
func checkRetryDuplicateSubmit(recent []config.PurchaseExecution, creatorID string, successor *config.PurchaseExecution, now time.Time) error {
	key, err := purchaseIdempotencyKey(creatorID, successor.Recommendations, successor.CapacityPercent)
	if err != nil {
		return fmt.Errorf("derive retry idempotency key: %w", err)
	}
	if dup := matchDuplicateInList(recent, creatorID, key, now); dup != nil {
		return &retryDuplicateSubmitError{existingID: dup.ExecutionID}
	}
	return nil
}

// retryDuplicateSubmitError rolls back persistRetrySuccessorTx's tx when the
// retrying user already has an identical live submit inside the idempotency
// window, so the retry would mint a second approvable row for the same
// purchase. persistRetrySuccessorTx translates it into a 409.
type retryDuplicateSubmitError struct{ existingID string }

func (e *retryDuplicateSubmitError) Error() string {
	return "identical purchase already submitted as execution " + e.existingID
}

// errRetryLinkRaceLost is returned from persistRetrySuccessorTx's tx
// closure to force a rollback (via WithTx's error path) when
// the link CAS matches zero rows -- a concurrent retry already
// claimed the failed row between the pre-tx already-retried check and this
// tx's UPDATE. Never returned to a caller directly; persistRetrySuccessorTx
// translates it into a 409 ClientError.
var errRetryLinkRaceLost = errors.New("retry link race lost")

// guardLadderTrancheForRetry locks the ladder tranche named by the execution's
// idempotency key (if any) before any execution row in the retry tx (only the
// per-creator submit advisory lock comes first), the same order the reconciler
// uses (tranche, then execution rows), and refuses when the
// tranche is settled. A failed tranche no longer nets, so an approvable
// successor would buy the same slice a second time.
func (h *Handler) guardLadderTrancheForRetry(ctx context.Context, tx pgx.Tx, idempotencyKey string) error {
	status, isLadder, err := h.config.LockLadderTrancheForRetryTx(ctx, tx, idempotencyKey)
	if err != nil {
		return err
	}
	if isLadder && config.LadderTrancheSettled(status) {
		return errRetryLadderTrancheSettled
	}
	return nil
}

// errRetryLadderTrancheSettled rolls back persistRetrySuccessorTx's tx when the
// execution's ladder tranche is already completed, failed or canceled.
var errRetryLadderTrancheSettled = errors.New("ladder tranche is settled")

// authorizeSessionRetry is the retry-side mirror of
// authorizeSessionCancel. Returns nil when the session is permitted to
// retry the given failed execution under the retry-any / retry-own
// rules (issue #47); 403 ClientError otherwise. Admins short-circuit;
// non-admins must hold retry-any (allowed for any execution) or
// retry-own (allowed only when the execution's CreatedByUserID matches
// the session UserID — legacy NULL-creator rows are out of reach for
// non-admins, same as the cancel path).
func (h *Handler) authorizeSessionRetry(ctx context.Context, session *Session, execution *config.PurchaseExecution) error {
	// Stateless admin API key: full access, no user row. Administrators-group
	// users pass via the retry-any check below.
	if session.UserID == apiKeyAdminUserID {
		return nil
	}
	if h.auth == nil {
		return NewClientError(500, "authentication service not configured")
	}

	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionRetryAny)
	if err != nil {
		return err
	}
	if hasAny {
		return nil
	}

	hasOwn, err := h.sessionHoldsVerb(ctx, session, auth.ActionRetryOwn)
	if err != nil {
		return err
	}
	if !hasOwn {
		return NewClientError(403, "permission denied: requires retry-any or retry-own on purchases")
	}

	if execution.CreatedByUserID == nil || *execution.CreatedByUserID != session.UserID {
		return NewClientError(403, "permission denied: cannot retry another user's failed purchase")
	}
	return nil
}

// tryResolveActorEmail returns the email of the session-authenticated user
// who made the request, or "" when the request carries no valid session.
// Best-effort: the approve/cancel routes are AuthPublic (token-only), so
// we don't require a session; we just capture it when present so the
// auth-gated deep-link flow (frontend /purchases/{action}/:id → login
// → session-authed call with ?token=…) can record per-user attribution
// without changing the token-only fallback path used by message workers.
func (h *Handler) tryResolveActorEmail(ctx context.Context, req *events.LambdaFunctionURLRequest) string {
	if s := h.tryGetSession(ctx, req); s != nil {
		return s.Email
	}
	return ""
}

// constraintDeniedError marks a 403 produced by requirePermissionConstraints
// (a granted verb whose own per-permission Constraints reject the request)
// so isPermissionDenied's strict, unwrapped *clientError assertion does NOT
// recognize it (issue #60 review follow-up, same mechanism as errCSRFRejected
// above). This matters because approvePurchase's session-vs-token dispatch
// treats any isPermissionDenied 403 as "no approve-* grant at all, fall
// through to the token/contact_email flow" -- correct for a bare missing-verb
// denial, but wrong for a Constraints-exceeded denial: approveViaToken never
// evaluates Constraints, so a session holding a capped approve-any/
// approve-own would otherwise bypass its own cap by using the email
// deep-link (which forces a login and therefore always carries a session)
// instead of the dashboard Approve button. Wrapping forces the dispatch's
// `default:` branch (propagate, terminal) instead of the fall-through
// branch. IsClientError still resolves this to the right HTTP code and
// message via errors.As + Unwrap, so response mapping is unaffected.
type constraintDeniedError struct {
	*clientError
}

func (e *constraintDeniedError) Unwrap() error { return e.clientError }

// wrapConstraintDenied marks a non-nil requirePermissionConstraints /
// enforcePurchaseConstraints error as terminal (see constraintDeniedError).
// A nil err, or an err that isn't a *clientError (e.g. an auth-service
// failure wrapped with fmt.Errorf), passes through unchanged -- the latter
// is already un-recognized by isPermissionDenied's own strict assertion, so
// it already hits the propagate branch without needing to be wrapped here.
func wrapConstraintDenied(err error) error {
	if err == nil {
		return nil
	}
	ce, ok := err.(*clientError) //nolint:errorlint // wrapping the concrete constructor's return; see constraintDeniedError doc
	if !ok {
		return err
	}
	return &constraintDeniedError{clientError: ce}
}

// fallsThroughToToken reports whether a session-authorization failure should
// hand the request to the email-token branch: an RBAC denial (403) or an
// account-scope miss (404), and only when a token is present. The token
// branch's contact_email gate is deliberately not account-scoped (issue #92).
// Without a token the denial is final (issue #173): nothing else can authorize
// the request, and the 404 must not let later status guards reveal an
// out-of-scope execution. Both matches are strict, like isPermissionDenied,
// so a wrapped error from deeper in the chain propagates.
func fallsThroughToToken(err error, token string) bool {
	return token != "" && (isPermissionDenied(err) || err == errNotFound) //nolint:errorlint // strict sentinel identity is deliberate
}

// isPermissionDenied reports whether err is *directly* a 403 ClientError
// (not merely something that wraps one). Used by the cancel-from-email
// session pre-check to distinguish a legitimate "your session lacks
// cancel-* permission" answer (fall through to the token branch's
// contact_email gate) from a transient auth-service failure (propagate so
// the caller sees the real cause). CR feedback on PR #216.
//
// Strict (un-wrapped) type assertion is deliberate: if a future caller
// wraps a 403 to add context (fmt.Errorf("permission check failed: %w",
// ...)), that wrapped error represents the *outer* failure mode (the
// wrapper's intent), not "this is still a 403". errors.As-style unwrapping
// would erase that distinction and silently route wrapped backend
// failures into the contact_email gate — exactly the misclassification
// the propagate-vs-fall-through split is meant to prevent. constraintDeniedError
// above deliberately exploits the same strictness from the other direction.
func isPermissionDenied(err error) bool {
	ce, ok := err.(*clientError) //nolint:errorlint // strict (unwrapped) assertion is deliberate; see comment above
	return ok && ce.code == 403
}

// tryGetSession returns the validated session for the request, or nil when
// the request carries no Bearer token, the auth service isn't configured,
// or session validation fails. Mirrors tryResolveActorEmail's silent
// best-effort semantics so AuthPublic callers can opt into session-aware
// behavior without forcing a 401 on tokenless flows.
func (h *Handler) tryGetSession(ctx context.Context, req *events.LambdaFunctionURLRequest) *Session {
	if req == nil || h.auth == nil {
		return nil
	}
	bearer := h.extractBearerToken(req)
	if bearer == "" {
		return nil
	}
	session, err := h.auth.ValidateSession(ctx, bearer)
	if err != nil || session == nil {
		return nil
	}
	return session
}

// sanitizedRecommendationErrors copies recs with provider text in Error
// replaced, leaving the stored slice untouched.
func sanitizedRecommendationErrors(recs []config.RecommendationRecord) []config.RecommendationRecord {
	out := make([]config.RecommendationRecord, len(recs))
	copy(out, recs)
	for i := range out {
		out[i].Error = purchase.SanitizeStoredError(out[i].Error)
	}
	return out
}

// buildPurchaseDetailsResponse builds the response map for a purchase execution.
func buildPurchaseDetailsResponse(execution *config.PurchaseExecution, planName string) map[string]any {
	response := map[string]any{
		"execution_id":       execution.ExecutionID,
		"plan_id":            execution.PlanID,
		"plan_name":          planName,
		"status":             execution.Status,
		"step_number":        execution.StepNumber,
		"scheduled_date":     execution.ScheduledDate.Format("2006-01-02"),
		"total_upfront_cost": execution.TotalUpfrontCost,
		"estimated_savings":  execution.EstimatedSavings,
		"recommendations":    sanitizedRecommendationErrors(execution.Recommendations),
	}

	if execution.NotificationSent != nil {
		response["notification_sent"] = execution.NotificationSent.Format("2006-01-02T15:04:05Z")
	}
	if execution.CompletedAt != nil {
		response["completed_at"] = execution.CompletedAt.Format("2006-01-02T15:04:05Z")
	}
	if execution.Error != "" {
		response["error"] = purchase.SanitizeStoredError(execution.Error)
	}
	return response
}

// getPurchaseDetails returns details about a specific purchase execution.
func (h *Handler) getPurchaseDetails(ctx context.Context, req *events.LambdaFunctionURLRequest, executionID string) (any, error) {
	if err := validateUUID(executionID); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}

	execution, err := h.config.GetExecutionByID(ctx, executionID)
	if errors.Is(err, config.ErrNotFound) {
		return nil, NewClientError(404, "execution not found")
	}
	if err != nil {
		// Real DB failure (connection error, timeout, etc.) - surface as 500
		// so infrastructure issues are visible to operators and not silently
		// masked as 404s (issue #976).
		return nil, fmt.Errorf("failed to get execution: %w", err)
	}

	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if err := h.requireExecutionInScope(ctx, scope, execution, nil); err != nil {
		return nil, err
	}

	return buildPurchaseDetailsResponse(execution, h.purchasePlanName(ctx, execution.PlanID)), nil
}

// purchasePlanName is the display name of the execution's plan, or "" when
// it has no plan or the plan cannot be read (the details still render).
func (h *Handler) purchasePlanName(ctx context.Context, planID string) string {
	if planID == "" {
		return ""
	}
	plan, err := h.config.GetPurchasePlan(ctx, planID)
	if err == nil && plan != nil {
		return plan.Name
	}
	return ""
}

// ExecutePurchaseRequest represents the request to execute purchases.
type ExecutePurchaseRequest struct {
	// Recommendations are matched against the stored recommendation set on
	// (provider, cloud_account_id, service, region, resource_type, engine,
	// term, payment). Only count, recommended_count and selected are taken
	// from the request; id, details and every cost field are replaced from
	// the stored row scaled by count (priceRecommendationsFromStore, #1905).
	Recommendations []config.RecommendationRecord `json:"recommendations"`
	// CapacityPercent is what fraction (1..100) of the originally-
	// recommended counts the user chose in the bulk Purchase flow.
	// Audit-only: the Recommendations slice already carries scaled
	// counts, so backend math ignores this field for purchase work.
	// 0 / absent defaults to 100 ("full capacity").
	CapacityPercent int `json:"capacity_percent,omitempty"`
	// ExecuteMode controls whether this request bypasses the approval
	// email and executes the purchase immediately. The only accepted
	// non-empty value is "direct"; any other value is treated as the
	// default approval-required flow. The handler re-checks the
	// execute-any/execute-own RBAC gate before honoring "direct",
	// even if the session already passed the execute:purchases gate in
	// validateExecutePurchaseRequest, so a client that sets this field
	// without the privilege receives a 403 rather than silent fallback.
	ExecuteMode string `json:"execute_mode,omitempty"`
}

// validateExecutePurchaseRequest handles the permission check, body parse,
// and recommendation-list bounds + scope checks. Extracted so executePurchase
// itself stays linear and under the gocyclo threshold. The returned
// PaymentAdjustment slice carries any payment-option coercions performed by
// the per-rec validation, for the response to surface to the caller (#1503
// follow-up); it is empty/nil when every rec was already canonical.
func (h *Handler) validateExecutePurchaseRequest(ctx context.Context, req *events.LambdaFunctionURLRequest) (ExecutePurchaseRequest, *Session, []PaymentAdjustment, error) {
	// Refuses a creator-less submit under 4-eyes before anything is
	// persisted, on both the approval-email and direct-execute paths (#677).
	session, err := h.requirePurchaseCreatingPermission(ctx, req, "execute", "purchases")
	if err != nil {
		return ExecutePurchaseRequest{}, nil, nil, err
	}
	execReq, err := parseExecutePurchaseBody(req.Body)
	if err != nil {
		return ExecutePurchaseRequest{}, nil, nil, err
	}
	// Scope: reject the whole request if any recommendation targets an
	// account outside the session's allowed_accounts. Safer than silently
	// dropping the out-of-scope ones — the user explicitly chose those
	// recommendations; a partial execution would misrepresent intent.
	// Runs before per-rec content validation so an out-of-scope request is
	// rejected as 403 regardless of the rec's Term/Payment/Count contents.
	if err := h.validatePurchaseRecommendationScope(ctx, session, execReq.Recommendations); err != nil {
		return ExecutePurchaseRequest{}, nil, nil, err
	}
	// Per-rec Provider/Service/Term/Payment/Count validation at the API
	// boundary so a malformed client-supplied rec (e.g. Term:7, Payment:"foo",
	// negative Count) is rejected here rather than reaching the cloud SDK at
	// execute time (#643). This is scoped to the web execute path only — the
	// retry path replays recs from an already-validated execution and must
	// not be re-gated by the same rules.
	adjustments, recErr := validateExecutePurchaseRecommendations(execReq.Recommendations)
	if recErr != nil {
		return ExecutePurchaseRequest{}, nil, nil, recErr
	}
	// Cross-check the audit-only capacity_percent against the scaled rec
	// counts so the persisted execution can't claim a capacity that
	// disagrees with what was actually purchased (#647). Skipped per-rec
	// when the rec carries no recommended_count.
	if err := validateCapacityConsistency(execReq.Recommendations, execReq.CapacityPercent); err != nil {
		return ExecutePurchaseRequest{}, nil, nil, err
	}
	// Price every rec from the stored recommendation set, then enforce the
	// per-permission Constraints (MaxPurchaseAmount, Providers, Services,
	// Regions, AccountIDs) on the granting execute:purchases permission
	// (SEC-01, issue #1141) against those store-derived values. Runs after
	// the per-rec validation above so provider and payment tokens are
	// canonical before the identity match, and after the scope check so an
	// out-of-scope account is refused as 403 before any pricing lookup. The
	// client's own upfront_cost / monthly_cost / savings / details are never
	// used (issue #1905): a purchaser under a MaxPurchaseAmount cap could
	// otherwise state any number and buy at list price.
	if err := h.priceAndEnforcePurchaseConstraints(ctx, session, execReq.Recommendations); err != nil {
		return ExecutePurchaseRequest{}, nil, nil, err
	}
	return execReq, session, adjustments, nil
}

// parseExecutePurchaseBody decodes an execute request, bounds its
// recommendation count to 1..1000 and normalizes capacity_percent. It runs
// after the permission check and before every scope and content check.
func parseExecutePurchaseBody(body string) (ExecutePurchaseRequest, error) {
	var execReq ExecutePurchaseRequest
	if err := json.Unmarshal([]byte(body), &execReq); err != nil {
		return ExecutePurchaseRequest{}, NewClientError(400, "invalid request body")
	}
	const maxRecommendations = 1000
	if len(execReq.Recommendations) == 0 {
		return ExecutePurchaseRequest{}, NewClientError(400, "no recommendations provided")
	}
	if len(execReq.Recommendations) > maxRecommendations {
		return ExecutePurchaseRequest{}, NewClientError(400, fmt.Sprintf("too many recommendations: %d (max %d)", len(execReq.Recommendations), maxRecommendations))
	}
	if err := normalizeCapacityPercent(&execReq); err != nil {
		return ExecutePurchaseRequest{}, err
	}
	return execReq, nil
}

// enforcePurchaseConstraints builds the per-recommendation
// auth.PermissionConstraints sets (purchaseConstraintSets) and enforces them
// against session's action:purchases permission, first rejecting a batch
// whose total commitment computes to zero (requireNonZeroCommitment).
// Shared by every money-mutating purchases call site (direct-execute request
// validation, the retry path, execute-any/own, approve-any/own and
// runPlannedPurchase) so all of them re-derive and check the exact same
// Constraints against the exact same zero-commitment guard (SEC-01, issue
// #1141; adversarial review follow-up to #1210 -- the retry path previously
// skipped this check entirely; issue #60 review follow-up -- approve-any/own
// and runPlannedPurchase previously skipped requireNonZeroCommitment, so an
// execution whose recs summed to $0 read as an uncapped MaxPurchaseAmount).
// action is threaded through (not hardcoded) because callers check
// different verbs: "execute" for the web submit and retry paths and
// runPlannedPurchase, "execute-any"/"execute-own" for direct-execute,
// "approve-any"/"approve-own" for session approve. recs carry store-derived
// or persisted-at-submit-time costs depending on the caller; none of these
// call sites ever price from client-supplied numbers. MaxPurchaseAmount is
// USD, so a batch with any rec not provably priced in USD is checked by
// requireUncappedPurchaseGrant instead of by its raw amount (issue #361).
func (h *Handler) enforcePurchaseConstraints(ctx context.Context, session *Session, action string, recs []config.RecommendationRecord) error {
	constraintSets := purchaseConstraintSets(recs)
	if err := requireNonZeroCommitment(constraintSets); err != nil {
		return err
	}
	if currency := nonUSDPurchaseCurrency(recs); currency != "" {
		return h.requireUncappedPurchaseGrant(ctx, session, action, constraintSets, currency)
	}
	return h.requirePermissionConstraints(ctx, session, action, "purchases", constraintSets)
}

// purchaseConstraintSets builds one auth.PermissionConstraints per
// recommendation in a web execute request, for the SEC-01 constraint
// enforcement in validateExecutePurchaseRequest. Single-value Provider/
// Service/Region/AccountIDs lists make the auth service's any-overlap
// matcher equivalent to strict containment, so a batch cannot pass on the
// strength of one in-scope rec while another rec is out of scope.
// MaxPurchaseAmount carries the batch's TOTAL commitment (upfront cost plus
// the full recurring obligation over the term, see recTotalCommitment) on
// every set, not just the upfront cost. A no-upfront or partial-upfront
// commitment's UpfrontCost is legitimately $0 or small, so capping on
// upfront alone let a batch with an arbitrarily large recurring commitment
// evade the cap using entirely honest data (adversarial review follow-up
// to #1210). AccountIDs is ALWAYS populated: a rec without a CloudAccountID
// carries unattributedAccountConstraint so an AccountIDs-constrained
// permission denies it (the auth matcher treats an empty request-side list
// as satisfied, so omitting the dimension would fail open). Session-level
// allowed_accounts scoping is independently enforced by
// validatePurchaseRecommendationScope.
func purchaseConstraintSets(recs []config.RecommendationRecord) []auth.PermissionConstraints {
	var totalCommitment float64
	for i := range recs {
		totalCommitment += recTotalCommitment(&recs[i])
	}
	sets := make([]auth.PermissionConstraints, 0, len(recs))
	for i := range recs {
		rec := &recs[i]
		accountID := unattributedAccountConstraint
		if rec.CloudAccountID != nil && *rec.CloudAccountID != "" {
			accountID = *rec.CloudAccountID
		}
		sets = append(sets, auth.PermissionConstraints{
			Providers:         []string{rec.Provider},
			Services:          []string{rec.Service},
			Regions:           []string{rec.Region},
			AccountIDs:        []string{accountID},
			MaxPurchaseAmount: totalCommitment,
		})
	}
	return sets
}

// recTotalCommitment returns a single recommendation's full committed
// spend: the upfront cost plus the recurring monthly cost multiplied by
// the term length in months. UpfrontCost and MonthlyCost are both totals
// for the rec's Count (AWS Cost Explorer reports them per-batch, not
// per-instance -- see recommendationToOffering), so they sum directly
// with no per-instance scaling needed. rec.Term is in years (AWS/Azure/GCP
// RI/SP standard; see exchange_lookup.go), converted to months with the
// same *12 convention used throughout this package. A nil MonthlyCost
// (all-upfront commitments have no recurring charge) or a non-positive
// Term (can't be converted to months) contribute nothing to the recurring
// leg, matching the Term<=0 handling elsewhere in this package. On the web
// execute path the two cost fields are the store-derived values written by
// priceRecommendationsFromStore.
func recTotalCommitment(rec *config.RecommendationRecord) float64 {
	return rec.TotalCommitment()
}

// requireNonZeroCommitment guards against a purchase batch whose computed
// total commitment (see recTotalCommitment) is exactly zero even though it
// targets real, count>0 resources. A genuine RI/Savings Plan/commitment
// purchase always carries SOME committed spend -- either an upfront cost
// (all/partial-upfront) or a recurring monthly charge (no-upfront); a batch
// that totals to exactly $0 is not "unconstrained", it is malformed or
// adversarial data (e.g. a no-upfront rec submitted with monthly_cost
// omitted or zeroed out to slip under a MaxPurchaseAmount cap -- the same
// evasion shape recTotalCommitment was introduced to close). Per this
// repo's no-silent-fallback-on-money-paths convention, a zero total must
// fail loud rather than be silently treated as "no constraint applies"
// (MaxPurchaseAmount==0 reads as uncapped to matchPurchaseAmountConstraint).
// sets is assumed non-empty whenever recs is (callers already reject empty
// recommendation batches before reaching this check).
func requireNonZeroCommitment(sets []auth.PermissionConstraints) error {
	if len(sets) > 0 && sets[0].MaxPurchaseAmount == 0 {
		return NewClientError(400, "recommendation batch has zero total commitment (no upfront or recurring cost); refusing to evaluate purchase constraints against it")
	}
	return nil
}

// normalizeCapacityPercent defaults an absent/zero capacity_percent to 100
// and rejects anything outside [1, 100]. capacity_percent is audit-only but
// still bounded: a value outside the range is a client bug worth surfacing
// rather than silently clamping. Extracted so validateExecutePurchaseRequest
// stays under the gocyclo threshold.
func normalizeCapacityPercent(execReq *ExecutePurchaseRequest) error {
	if execReq.CapacityPercent == 0 {
		execReq.CapacityPercent = 100
	}
	if execReq.CapacityPercent < 1 || execReq.CapacityPercent > 100 {
		return NewClientError(400, fmt.Sprintf("capacity_percent must be between 1 and 100, got %d", execReq.CapacityPercent))
	}
	return nil
}

// validateExecutePurchaseRecommendations runs the per-rec #643 boundary
// validation over every rec in a web execute request, then the one-account-
// per-batch rule (#1902), returning the first failure. On success it also
// returns the payment-option coercions that occurred (nil-free, in rec order)
// so the response can surface them to the caller (#1503 follow-up). Extracted
// so validateExecutePurchaseRequest stays under the gocyclo threshold.
func validateExecutePurchaseRecommendations(recs []config.RecommendationRecord) ([]PaymentAdjustment, error) {
	var adjustments []PaymentAdjustment
	for i := range recs {
		adjustment, err := validatePurchaseRecommendation(&recs[i], i)
		if err != nil {
			return nil, err
		}
		if adjustment != nil {
			adjustments = append(adjustments, *adjustment)
		}
	}
	// One plan-less execution targets exactly one cloud account (#1902).
	// Same rule the executor enforces in resolveSingleAccountProvider, applied
	// here so the batch is refused before an execution row exists, an
	// approval email goes out, or a History row lands as failed.
	if _, err := purchase.SingleCloudAccountIDFromRecs(recs); err != nil {
		return nil, NewClientError(400, err.Error())
	}
	return adjustments, nil
}

// finalizePurchaseStatus flips an execution's stored status to "failed" if
// the approval email couldn't send, and returns the status string the API
// response should carry. Returns "pending" when email_sent is true.
//
// The write is guarded on the row still being "pending" (issue #1232). A
// session approve, cancel or pause can land while the email is being sent;
// an unguarded write would then stamp "failed" over a row that was already
// approved and bought, which makes it eligible for retry and a second
// purchase. A lost guard is a no-op: the row keeps the status the other
// writer gave it, and that status is what the response carries.
//
// Any other save error is returned as a 500 (issue #54). The row then
// still reads "pending" with no error text and a valid approval token while
// no approval email exists, so answering "pending" would make it look like
// a healthy request waiting on its approver. The error names the execution
// so the caller can cancel it.
func (h *Handler) finalizePurchaseStatus(ctx context.Context, execution *config.PurchaseExecution, emailSent bool, emailReason string) (string, error) {
	if emailSent {
		return "pending", nil
	}
	failed := *execution
	failed.Status = "failed"
	failed.Error = emailReason
	err := h.config.SaveExecutionIfStatus(ctx, &failed, []string{"pending"})
	switch {
	case err == nil:
		execution.Status, execution.Error = failed.Status, failed.Error
		return "failed", nil
	case errors.Is(err, config.ErrExecutionNotInExpectedStatus):
		logging.Warnf("execution %s left pending while its approval email was being sent; not marking it failed: %v", execution.ExecutionID, err)
		return h.currentExecutionStatus(ctx, execution.ExecutionID), nil
	default:
		logging.Errorf("AUDIT LOSS: failed to mark execution %s as failed after email send error (%s): %v", execution.ExecutionID, emailReason, err)
		return "", emailFailureNotRecordedError(execution.ExecutionID, emailReason, err)
	}
}

// emailFailureNotRecordedError is the 500 finalizePurchaseStatus returns when
// the approval email failed and the write recording that failure failed too
// (issue #54). The body names the execution and carries the email reason so
// the caller can find the row and cancel it. The store error stays out of
// the body but is wrapped so it remains reachable with errors.Is.
func emailFailureNotRecordedError(executionID, emailReason string, saveErr error) error {
	ce := NewClientErrorWithDetails(500,
		fmt.Sprintf("AUDIT LOSS: the approval email for execution %s could not be sent, and saving that failure on the execution also failed; "+
			"it may still show as pending with no approval email on the way, so cancel it from History", executionID),
		map[string]any{
			"execution_id": executionID,
			"email_sent":   false,
			"email_reason": emailReason,
		})
	return fmt.Errorf("%w: %w", ce, saveErr)
}

// currentExecutionStatus re-reads an execution's stored status for a response
// whose in-memory copy went stale. Falls back to "pending" (the status the row
// was created with) when the read fails.
func (h *Handler) currentExecutionStatus(ctx context.Context, executionID string) string {
	row, err := h.config.GetExecutionByID(ctx, executionID)
	if err != nil || row == nil {
		logging.Errorf("failed to re-read execution %s status: %v", executionID, err)
		return "pending"
	}
	return row.Status
}

// validateAndTotalRecommendations validates each recommendation and returns
// totals. Runs on store-derived recs for the web path and on the persisted
// row for retry; the negative and $10M guards stay as the sanity check for
// both.
func validateAndTotalRecommendations(recs []config.RecommendationRecord) (upfront, savings float64, err error) {
	const maxAmount = 10_000_000 // $10M sanity cap
	for i := range recs {
		rec := recs[i]
		if rec.UpfrontCost < 0 {
			return 0, 0, NewClientError(400, fmt.Sprintf("recommendation %d has negative upfront cost: %.2f", i, rec.UpfrontCost))
		}
		if rec.Savings < 0 {
			return 0, 0, NewClientError(400, fmt.Sprintf("recommendation %d has negative savings: %.2f", i, rec.Savings))
		}
		upfront += rec.UpfrontCost
		savings += rec.Savings
	}
	if upfront > maxAmount {
		return 0, 0, NewClientError(400, fmt.Sprintf("total upfront cost %.2f exceeds maximum allowed (%.2f)", upfront, float64(maxAmount)))
	}
	if savings > maxAmount {
		return 0, 0, NewClientError(400, fmt.Sprintf("total estimated savings %.2f exceeds maximum allowed (%.2f)", savings, float64(maxAmount)))
	}
	return upfront, savings, nil
}

// derefStringOrEmpty returns the string a pointer points to, or "" when nil.
// Used to convert *string creator IDs to the bare string needed for hashing
// without adding a branch to the caller.
func derefStringOrEmpty(s *string) string {
	if s != nil {
		return *s
	}
	return ""
}

// resolveCreatorUserID returns a pointer to the session user's UUID for
// stamping onto purchase_executions.created_by_user_id, or nil for
// non-user sessions whose UserID isn't a real UUID. This keeps the
// cancel-own RBAC check (issue #46) honest:
//   - Real human session → pointer to UUID; future cancel-own match works.
//   - Admin-API-key session → UserID is the literal "admin-api-key"
//     sentinel, which fails validateUUID; store NULL so the FK to users
//     stays valid and the row is reachable only via cancel-any/email-token.
//   - nil session (defensive — current callers gate on requirePermission
//     so this shouldn't happen) → NULL.
func resolveCreatorUserID(session *Session) *string {
	if session == nil {
		return nil
	}
	if validateUUID(session.UserID) != nil {
		return nil
	}
	uid := session.UserID
	return &uid
}

// requireAttributablePurchaseCreator refuses, while 4-eyes mode is on, any
// request that would mint a pending purchase execution with no recorded
// creator (issue #677). resolveCreatorUserID stores NULL for a session whose
// UserID is not a user UUID, which in practice is the stateless admin API
// key. Under 4-eyes, requireDifferentApprover and the manager's
// checkDifferentApprover deny a NULL-creator row that is not
// created_by_system for every approver, so such a row could only sit pending
// until it expired (and, on the direct-execute path, be refused after it was
// already written). Refusing up front gives the caller a clear error instead.
// The key is never stamped with a human creator on its behalf: that would let
// that human's colleague "approve" a purchase no human requested, or let the
// key holder approve their own request.
//
// Sessions with a user UUID (signed-in users and per-user API keys) pass
// without a config read. A config-load failure denies (fail closed), matching
// requireDifferentApprover. A nil config means 4-eyes is off, also matching it.
func (h *Handler) requireAttributablePurchaseCreator(ctx context.Context, session *Session) error {
	if resolveCreatorUserID(session) != nil {
		return nil
	}
	cfg, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		return fmt.Errorf("4-eyes policy check: failed to load global config: %w", err)
	}
	if cfg == nil || !cfg.RequireDifferentApprover {
		return nil
	}
	logging.Warnf("4-eyes: mode on; refusing to create a purchase execution with no attributable creator")
	return NewClientError(403, "4-eyes approval mode is enabled and this request has no attributable user (for example the admin API key), so the purchase it creates could never be approved; sign in or use a per-user API key instead")
}

// requirePurchaseCreatingPermission is requirePermission for endpoints that
// mint pending purchase executions: after the RBAC check it applies
// requireAttributablePurchaseCreator so no NULL-creator row is created while
// 4-eyes mode is on (issue #677).
func (h *Handler) requirePurchaseCreatingPermission(ctx context.Context, req *events.LambdaFunctionURLRequest, action, resource string) (*Session, error) {
	session, err := h.requirePermission(ctx, req, action, resource)
	if err != nil {
		return nil, err
	}
	if err := h.requireAttributablePurchaseCreator(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

// fourEyesActorIdentity returns the identity string passed as the actor to
// purchase.Manager.ApproveAndExecute for the 4-eyes dual-control comparison
// (issue #1005 / PR #1500 adversarial review). Real human sessions always
// carry a non-empty Email. The stateless admin API key session
// (apiKeyAdminUserID sentinel) does not — falling back to session.Email alone
// there would make the manager's enforceFourEyesPolicy treat every API-key-
// driven approve/direct-execute as "actor identity unknown" and fail closed,
// even when the row was created by a different real user (a legitimate,
// non-self-approval case the RBAC layer already permits via the
// apiKeyAdminUserID short-circuit in authorizeSessionApprove /
// authorizeSessionExecuteDirect). Falling back to the sentinel itself is
// safe: it is never a valid email, so it can never collide with a real
// user's resolved email in the 4-eyes comparison, and a row CREATED via the
// API key (CreatedByUserID stays nil per resolveCreatorUserID above) is
// still denied through the existing NULL-creator fail-closed branch.
func fourEyesActorIdentity(session *Session) string {
	if session.Email != "" {
		return session.Email
	}
	return session.UserID
}

// executePurchase handles direct purchase execution from recommendations
// matchDuplicateInList scans a slice of recent executions for one that
// matches creatorID + idempotencyKey within the idempotency window.
// Returns the first match, or nil when there is no duplicate.
// Extracted from persistExecutionAndSuppressions to keep cyclomatic
// complexity under the project gocyclo threshold.
func matchDuplicateInList(pending []config.PurchaseExecution, creatorID, idempotencyKey string, now time.Time) *config.PurchaseExecution {
	cutoff := now.Add(-purchaseIdempotencyWindow)
	for i := range pending {
		ex := &pending[i]
		if ex.Source != common.PurchaseSourceWeb || ex.ScheduledDate.Before(cutoff) {
			continue
		}
		exCreator := ""
		if ex.CreatedByUserID != nil {
			exCreator = *ex.CreatedByUserID
		}
		if exCreator != creatorID {
			continue
		}
		exKey, err := purchaseIdempotencyKey(exCreator, ex.Recommendations, ex.CapacityPercent)
		if err != nil {
			logging.Errorf("purchase idempotency: skipping pending execution %s, cannot derive its key: %v", ex.ExecutionID, err)
			continue
		}
		if exKey == idempotencyKey {
			return ex
		}
	}
	return nil
}

// persistExecutionAndSuppressions saves the execution + its suppression
// records in a single transaction. It also performs the duplicate-execution
// check inside the same transaction, behind a per-creator advisory lock, so
// that the read and the insert are atomic: a plain SELECT ... FOR UPDATE
// cannot see a concurrent transaction's uncommitted insert (#643, MON-01).
//
// Return values:
//   - (nil, nil)          — no duplicate found; execution was inserted.
//   - (existing, nil)     — duplicate found; execution was NOT inserted; caller
//     should collapse onto existing.
//   - (nil, err)          — store error; caller should surface it.
func (h *Handler) persistExecutionAndSuppressions(
	ctx context.Context,
	execution *config.PurchaseExecution,
	suppressions []config.PurchaseSuppression,
	creatorID, idempotencyKey string,
) (dup *config.PurchaseExecution, err error) {
	if err := h.requireAZConfigSchema(ctx); err != nil {
		return nil, err
	}
	txErr := h.config.WithTx(ctx, func(tx pgx.Tx) error {
		// Duplicate check inside the tx, after taking the creator's submit
		// lock: a concurrent identical submit waits for this tx to commit,
		// then finds the row. Atomic with the insert below.
		now := time.Now()
		recent, err := h.config.ListRecentSubmitsTx(ctx, tx, creatorID, now.Add(-purchaseIdempotencyWindow))
		if err != nil {
			return err
		}
		if dup = matchDuplicateInList(recent, creatorID, idempotencyKey, now); dup != nil {
			return nil // found duplicate — skip insert, commit tx (read-only)
		}

		if err := h.config.SavePurchaseExecutionTx(ctx, tx, execution); err != nil {
			return err
		}
		for i := range suppressions {
			if err := h.config.CreateSuppressionTx(ctx, tx, &suppressions[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		return nil, fmt.Errorf("failed to save execution: %w", txErr)
	}
	return dup, nil
}

// purchaseIdempotencyWindow is how long a freshly-created pending execution
// "absorbs" an identical resubmission. A double-click or a retried network
// call within this window resolves to the original execution instead of
// minting a second approvable row (double-spend). Sized to comfortably cover
// a stuck request retry without masking a genuine intentional re-purchase.
const purchaseIdempotencyWindow = 2 * time.Minute

// purchaseIdempotencyKey derives a stable fingerprint of a submit so two
// identical submissions (same actor, same scaled rec set, same capacity)
// collapse to one execution. The recs are normalized and sorted so map/slice
// ordering can't change the hash. Account scope is implicit: each rec carries
// its CloudAccountID, so the same recs targeting a different account hash
// differently. Closes issue #644.
func purchaseIdempotencyKey(creatorID string, recs []config.RecommendationRecord, capacityPercent int) (string, error) {
	tuples := make([]string, 0, len(recs))
	for i := range recs {
		r := recs[i]
		acct := ""
		if r.CloudAccountID != nil {
			acct = *r.CloudAccountID
		}
		az, err := recDetailsAvailabilityZone(&r)
		if err != nil {
			return "", err
		}
		ci, err := r.ComputeIdentity()
		if err != nil {
			return "", err
		}
		// Single-AZ and Multi-AZ RDS buys of the same class are different
		// purchases and must not dedupe into one (issue #408).
		azConfig, err := r.IdentityAZConfig()
		if err != nil {
			return "", err
		}
		tuples = append(tuples, fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d|%d|%s|%s|%s|%s|%s",
			strings.ToLower(strings.TrimSpace(r.Provider)),
			r.Service, r.Region, r.ResourceType, r.Engine, acct,
			r.Count, r.Term, strings.ToLower(strings.TrimSpace(r.Payment)), az,
			ci.Platform, ci.Tenancy, azConfig))
	}
	sort.Strings(tuples)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x1f%d\x1f%s", creatorID, capacityPercent, strings.Join(tuples, "\x1e"))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// recDetailsAvailabilityZone returns the zonal EC2 AZ carried in the rec's
// Details blob, "" for non-compute recs and for absent or legacy Details.
// The AZ is read from Details, not RecommendationRecord.AvailabilityZone,
// because pending executions written before issue #792 carry it only there
// and the idempotency key is recomputed from them (issue #792).
func recDetailsAvailabilityZone(r *config.RecommendationRecord) (string, error) {
	if r.Service != string(common.ServiceEC2) && r.Service != string(common.ServiceCompute) {
		return "", nil
	}
	details, err := common.DecodeServiceDetailsFor(r.Service, r.Details)
	if err != nil {
		return "", fmt.Errorf("decode %s details of %s/%s: %w", r.Service, r.Region, r.ResourceType, err)
	}
	if cd, ok := details.(*common.ComputeDetails); ok && cd != nil {
		return strings.TrimSpace(cd.AvailabilityZone), nil
	}
	return "", nil
}

// buildDuplicatePurchaseResponse returns the executePurchase response body for
// a submit that collapsed onto an existing pending execution (#644). It points
// at the original row so the client lands on the same approvable execution
// instead of a freshly-minted duplicate. duplicate=true lets the UI explain
// why no new approval email was sent.
func buildDuplicatePurchaseResponse(ex *config.PurchaseExecution) map[string]any {
	resp := map[string]any{
		"execution_id":         ex.ExecutionID,
		"status":               ex.Status,
		"recommendation_count": len(ex.Recommendations),
		"total_upfront_cost":   ex.TotalUpfrontCost,
		"estimated_savings":    ex.EstimatedSavings,
		"duplicate":            true,
		"message":              "Duplicate submission collapsed onto the existing execution (see status); no new execution or approval request was created.",
	}
	// An approval email only exists for a row still awaiting approval. A row
	// that already moved on (direct execute, approved, completed) never had
	// one, and email_sent=false would make the client report a failure.
	if ex.Status == "pending" || ex.Status == "notified" {
		resp["email_sent"] = ex.NotificationSent != nil
	}
	return resp
}

// newPendingExecution builds a fresh pending PurchaseExecution with a
// crypto/rand-backed approval token. Extracted from executePurchase to keep
// that function under the gocyclo threshold.
//
// Returns the RAW token alongside the execution: the struct's ApprovalToken
// field holds only the SHA-256 hash that gets persisted (issue #103), so the
// caller's synchronous approval email (sendPurchaseApprovalEmail) must take
// the raw value from the second return, not from execution.ApprovalToken.
func newPendingExecution(req *ExecutePurchaseRequest, totalUpfront, totalSavings float64) (*config.PurchaseExecution, string, error) {
	approvalToken, err := common.GenerateApprovalToken()
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate approval token: %w", err)
	}
	tokenExpiresAt := time.Now().Add(config.ApprovalTokenTTL)
	return &config.PurchaseExecution{
		ExecutionID:            uuid.New().String(),
		Status:                 "pending",
		ScheduledDate:          time.Now(),
		Recommendations:        req.Recommendations,
		TotalUpfrontCost:       totalUpfront,
		EstimatedSavings:       totalSavings,
		ApprovalToken:          config.HashApprovalToken(approvalToken),
		ApprovalTokenExpiresAt: &tokenExpiresAt,
		Source:                 common.PurchaseSourceWeb,
		CapacityPercent:        req.CapacityPercent,
	}, approvalToken, nil
}

func (h *Handler) executePurchase(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	execReq, session, paymentAdjustments, err := h.validateExecutePurchaseRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	totalUpfront, totalSavings, err := validateAndTotalRecommendations(execReq.Recommendations)
	if err != nil {
		return nil, err
	}

	// Submit-time idempotency (#644/#643): a double-click or retried POST with
	// an identical actor + scaled rec set + capacity within a short window must
	// resolve to the original pending execution rather than minting a second
	// approvable row (double-spend). The atomic guard lives inside
	// persistExecutionAndSuppressions (per-creator lock + read + INSERT in one tx)
	// which closes the TOCTOU race that a pre-tx read alone cannot prevent.
	creator := resolveCreatorUserID(session)
	creatorID := derefStringOrEmpty(creator)
	idempotencyKey, err := purchaseIdempotencyKey(creatorID, execReq.Recommendations, execReq.CapacityPercent)
	if err != nil {
		return nil, fmt.Errorf("derive purchase idempotency key: %w", err)
	}

	execution, rawApprovalToken, err := newPendingExecution(&execReq, totalUpfront, totalSavings)
	if err != nil {
		return nil, err
	}
	// CreatedByUserID is set after construction so newPendingExecution stays
	// signature-stable. The cancel-own RBAC path in cancelPurchase relies on
	// this stamp to identify the creator on later cancellation; legacy rows
	// pre-dating issue #46 can carry NULL here, so always go through the
	// resolveCreatorUserID helper rather than dereferencing the session.
	execution.CreatedByUserID = creator
	executionID := execution.ExecutionID

	// Load the grace-period config once before entering the tx.
	graceCfg, err := h.gracePeriodConfig(ctx)
	if err != nil {
		return nil, err
	}
	suppressions := config.BuildSuppressions(execReq.Recommendations, executionID, graceCfg, time.Now())
	dupExec, err := h.persistExecutionAndSuppressions(ctx, execution, suppressions, creatorID, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if dupExec != nil {
		logging.Infof("concurrent duplicate purchase submit collapsed to existing execution %s", dupExec.ExecutionID)
		return withPaymentAdjustments(buildDuplicatePurchaseResponse(dupExec), paymentAdjustments), nil
	}

	// Direct-execute path (issue #289): a session with execute-any or
	// execute-own on purchases can request execute_mode="direct" to bypass
	// the approval email and commit the purchase immediately.
	//
	// authorizeSessionExecuteDirect is a hard gate — any check failure
	// returns a 403 rather than falling through to the email path. This
	// prevents a client that sets execute_mode="direct" but only holds the
	// base execute:purchases verb from silently degrading to the email flow.
	if execReq.ExecuteMode == "direct" {
		if err := h.authorizeSessionExecuteDirect(ctx, session, creatorID, execReq.Recommendations); err != nil {
			return nil, err
		}
		return h.directExecutePurchase(ctx, req, execution, session, paymentAdjustments)
	}

	// Send approval email synchronously so the response can surface the
	// actual outcome. The DB write above is the source of truth — email is
	// best-effort and never blocks the response body; the returned
	// email_sent / email_reason fields let the UI tell the user whether they
	// should wait for an inbox or cancel/retry manually. The one exception is
	// a send failure that cannot be recorded on the row: that answers 500
	// (issue #54).
	return h.sendApprovalAndFinalize(ctx, req, execution, rawApprovalToken, execReq.Recommendations, totalUpfront, totalSavings, paymentAdjustments)
}

// gracePeriodConfig loads the global config for BuildSuppressions' grace
// periods, read before any tx opens. A read error is returned and fails the
// purchase before anything is persisted (audit A01-016, issue #111): falling
// back to the default grace period would write suppressions for a provider
// the operator switched off (grace 0) or with the wrong expiry. This matches
// internal/purchase (AttachResolvedRecommendations) and the other config
// reads on these paths.
func (h *Handler) gracePeriodConfig(ctx context.Context) (*config.GlobalConfig, error) {
	g, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read grace periods for suppressions: %w", err)
	}
	return g, nil
}

// sendApprovalAndFinalize sends the approval email for a freshly persisted
// pending execution, records a send failure on the row through
// finalizePurchaseStatus, and builds the approval-pending response. A failed
// failure write comes back as an error rather than a response (issue #54).
// Extracted from executePurchase to keep it under the gocyclo budget.
func (h *Handler) sendApprovalAndFinalize(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution, rawApprovalToken string, recs []config.RecommendationRecord, totalUpfront, totalSavings float64, paymentAdjustments []PaymentAdjustment) (any, error) {
	emailSent, emailReason, recipient := h.sendPurchaseApprovalEmail(ctx, req, execution, rawApprovalToken, recs, totalUpfront, totalSavings)
	status, err := h.finalizePurchaseStatus(ctx, execution, emailSent, emailReason)
	if err != nil {
		return nil, err
	}
	return withPaymentAdjustments(buildApprovalPendingResponse(execution.ExecutionID, status, len(recs), totalUpfront, totalSavings, emailSent, emailReason, recipient), paymentAdjustments), nil
}

// withPaymentAdjustments attaches the payment-option coercion notices to an
// executePurchase response body under "payment_adjustments" (#1503 follow-up).
// The key is omitted entirely when no coercion happened, so existing clients
// see an unchanged response on the common canonical-input case.
func withPaymentAdjustments(resp map[string]any, adjustments []PaymentAdjustment) map[string]any {
	if len(adjustments) > 0 {
		resp["payment_adjustments"] = adjustments
	}
	return resp
}

// buildApprovalPendingResponse assembles the JSON-serialisable response body
// for the approval-pending path of executePurchase. Extracted to keep
// executePurchase cyclomatic complexity within the project limit.
func buildApprovalPendingResponse(
	executionID string,
	status string,
	recCount int,
	totalUpfront, totalSavings float64,
	emailSent bool,
	emailReason, recipient string,
) map[string]any {
	message := "Purchase execution created and pending approval"
	if !emailSent {
		message = "Purchase execution created but approval email could not be sent - see email_reason"
	}
	resp := map[string]any{
		"execution_id":         executionID,
		"status":               status,
		"recommendation_count": recCount,
		"total_upfront_cost":   totalUpfront,
		"estimated_savings":    totalSavings,
		"email_sent":           emailSent,
		"message":              message,
	}
	if emailReason != "" {
		resp["email_reason"] = emailReason
	}
	if recipient != "" {
		resp["approval_recipient"] = recipient
	}
	return resp
}

// directExecutePurchase is the direct-execute branch of executePurchase
// (issue #289). It is called after the execution row has been persisted as
// "pending" and after authorizeSessionExecuteDirect has confirmed the session
// holds execute-any or execute-own on purchases.
//
// Steps:
//  1. Delegate to purchase.Manager.DirectExecute, which atomically
//     transitions the row to "approved" (stamping ApprovedBy), stamps the
//     three audit fields (executed_by_user_id, executed_at,
//     pre_approval_skip_reason) only once that claim is won, and then runs the
//     purchase synchronously. A refused or lost direct execute therefore
//     leaves no executed_at on a row that never ran, and failures map through
//     executeFailureError (403 four-eyes, 409 lost claim, 502 after the claim).
//  2. Send the best-effort post-execution notification email (issue #291).
//     This is the only email the direct-execute flow emits -- no approval
//     email precedes it -- so it is the path where the executed-notification
//     matters most.
//  3. Return a "completed" status to the caller, carrying any payment-option
//     coercion notices (paymentAdjustments, from the request validation) so
//     the direct-execute response surfaces them like the approval path does.
//
// The audit invariant is that a non-nil executed_by_user_id always co-occurs
// with a non-nil pre_approval_skip_reason; both are stamped in the same save.
func (h *Handler) directExecutePurchase(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution, session *Session, paymentAdjustments []PaymentAdjustment) (any, error) {
	t0 := time.Now()
	executionID := execution.ExecutionID
	logging.Infof("purchase[%s]: directExecutePurchase entry (auth=session)", executionID)

	// Human session direct-execute: stamp the session user's UUID onto
	// transitioned_by (FK-safe via validUUIDPtrOrNil) so the audit trail
	// records who flipped the row to "approved".
	//
	// revocationToken is the raw token DirectExecute mints for this
	// approval (issue #103); only the token hash is stored, so this
	// return value is the only place a raw, emailable token exists after
	// the call returns.
	// The direct-execute Constraints ran on the request's recommendations; the
	// funnel re-runs them on the row its claim returns (issue #415).
	final, revocationToken, err := h.purchase.DirectExecute(ctx, executionID, fourEyesActorIdentity(session), validUUIDPtrOrNil(&session.UserID), h.directExecuteClaimCheck(session))
	if err != nil {
		logging.Errorf("purchase[%s]: directExecutePurchase failed after %s: %v",
			executionID, time.Since(t0), err)
		return nil, h.executeFailure(ctx, req, executionID, "direct-executed", final, revocationToken, session.Email, err)
	}

	logging.Infof("purchase[%s]: directExecutePurchase completed in %s", executionID, time.Since(t0))
	// Best-effort post-execution notification email (issue #291). The
	// direct-execute path sends no approval email (the whole point is to skip
	// the approval round-trip), so this is the only notification the recipients
	// receive -- making the executed-notification the most valuable here.
	// Mirrors the approve-path call sites (see approvePurchase and
	// approvePurchaseViaSession); recipient resolution and the nil-notifier
	// guard live inside sendPurchaseExecutedEmail. session.Email is the actor
	// who direct-executed, matching the actor passed to ApproveAndExecute above.
	h.sendPurchaseExecutedEmail(ctx, req, execution, revocationToken, session.Email)
	return withPaymentAdjustments(map[string]any{
		"execution_id":         executionID,
		"status":               "completed",
		"recommendation_count": len(execution.Recommendations),
		"total_upfront_cost":   execution.TotalUpfrontCost,
		"estimated_savings":    execution.EstimatedSavings,
		"direct_execute":       true,
		"message":              "Purchase executed immediately (direct-execute permission).",
	}, paymentAdjustments), nil
}

// archeraEducationURL returns dashboardBase + "/archera-insurance", or "" when
// dashboardBase is empty so email templates omit the block safely.
func archeraEducationURL(dashboardBase string) string {
	if dashboardBase == "" {
		return ""
	}
	return dashboardBase + "/archera-insurance"
}

// approvalResponseRecipient returns the email address to surface in the
// approval_recipient API response field (and therefore in the post-submit toast).
// It returns globalNotify when set (after trimming whitespace), matching the
// address the History handler shows for pending rows (resolvePendingApproverEmail
// also returns globalNotify first). Falls back to to (the per-account
// contact_email) when globalNotify is empty or whitespace-only.
// Extracted to keep sendPurchaseApprovalEmail under the cyclomatic-complexity ceiling.
func approvalResponseRecipient(globalNotify, to string) string {
	if trimmed := strings.TrimSpace(globalNotify); trimmed != "" {
		return trimmed
	}
	return to
}

// sendPurchaseApprovalEmail sends an approval-request email for a newly created
// execution and returns a structured outcome:
//   - (true, "", recipient) on successful send
//   - (false, "<reason>", "") on any preflight gate or send error
//   - (false, "<reason>", recipient) when send failed AFTER recipient resolution
//     (so the response can still surface who would have been notified)
//
// `recipient` is the address surfaced in the post-submit toast. It is the
// Admin notification email (Settings -> General) when configured, matching the
// value the History UI shows for pending rows via resolvePendingApproverEmail.
// When no notification email is set, it falls back to the per-account
// contact_email (the actual To address). This fixes issue #735 where the toast
// named the per-account contact_email instead of the Admin notification email,
// creating a discrepancy with the History "awaiting approval from X" display.
//
// Errors are also logged at Errorf level so they show up in CloudWatch, but
// the reason string is what the API response surfaces to the UI.
func (h *Handler) sendPurchaseApprovalEmail(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution, rawApprovalToken string, recs []config.RecommendationRecord, totalUpfront, totalSavings float64) (bool, string, string) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	if h.emailNotifier == nil {
		return false, "email notifier not configured for this deployment", ""
	}
	globalCfg, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		logging.Errorf("Failed to load global config for approval email: %v", err)
		return false, fmt.Sprintf("failed to load settings: %v", err), ""
	}
	globalNotify := ""
	if globalCfg.NotificationEmail != nil {
		globalNotify = *globalCfg.NotificationEmail
	}
	to, cc, approvers, err := h.resolveApprovalRecipients(ctx, recs, globalNotify)
	if err != nil {
		logging.Errorf("Failed to resolve approval recipients: %v", err)
		return false, fmt.Sprintf("failed to resolve recipients: %v", err), ""
	}
	if to == "" {
		return false, "no notification email set in Settings → General and no account contact emails configured", ""
	}
	// responseRecipient is the email address surfaced in the UI toast (approval_recipient
	// API field). It matches what the History handler shows for pending rows via
	// resolvePendingApproverEmail, which always returns globalNotify when set. Using
	// globalNotify here keeps both displays consistent. When globalNotify is empty,
	// fall back to to (the per-account contact_email). See issue #735.
	responseRecipient := approvalResponseRecipient(globalNotify, to)
	dashboardBase := h.resolveDashboardURL(req)
	data := email.NotificationData{
		DashboardURL:        dashboardBase,
		ApprovalToken:       rawApprovalToken,
		ExecutionID:         execution.ExecutionID,
		TotalSavings:        totalSavings,
		TotalUpfrontCost:    totalUpfront,
		Recommendations:     approvalRecommendationSummaries(recs),
		RecipientEmail:      to,
		CCEmails:            cc,
		AuthorizedApprovers: approvers,
	}
	data.ArcheraEducationURL = archeraEducationURL(dashboardBase)
	if err := h.emailNotifier.SendPurchaseApprovalRequest(ctx, data); err != nil {
		logging.Errorf("Failed to send purchase approval email: %v", err)
		reason, recipient := approvalSendFailure(err, responseRecipient)
		return false, reason, recipient
	}
	return true, "", responseRecipient
}

// approvalRecommendationSummaries is the per-rec table of the approval email.
func approvalRecommendationSummaries(recs []config.RecommendationRecord) []email.RecommendationSummary {
	summaries := make([]email.RecommendationSummary, 0, len(recs))
	for i := range recs {
		rec := recs[i]
		summaries = append(summaries, email.RecommendationSummary{
			Service:        rec.Service,
			ResourceType:   rec.ResourceType,
			Engine:         rec.Engine,
			Region:         rec.Region,
			Count:          rec.Count,
			MonthlySavings: rec.Savings,
		})
	}
	return summaries
}

// approvalSendFailure maps a failed approval send to the response's
// email_reason and approval_recipient (none when no recipient was found).
func approvalSendFailure(err error, responseRecipient string) (reason, recipient string) {
	switch {
	case errors.Is(err, email.ErrNoRecipient):
		return "no notification email set in Settings → General", ""
	case errors.Is(err, email.ErrNoFromEmail):
		return "FROM_EMAIL not configured for this deployment", responseRecipient
	default:
		return fmt.Sprintf("send failed: %v", err), responseRecipient
	}
}

// sendPurchaseExecutedEmail sends a post-execution notification to the
// configured recipients after a purchase completes successfully. It follows
// the same best-effort contract as sendPurchaseApprovalEmail: errors are
// logged but never propagate to the caller — the purchase is already done.
//
// Recipients (deduped):
//   - global notification_email (Settings → General)
//   - per-account contact_email for each recommendation's account
//   - email of the user who originally submitted the execution
//     (looked up by CreatedByUserID via h.auth.GetUser)
//
// The revocation link uses the execution's ApprovalToken (reusing the same
// token infrastructure). When the sibling "AWS RI/SP revocation via support
// case" issue lands and adds a dedicated revocation token + window field,
// this method should be updated to use those fields instead.
// revocationToken is the RAW token to embed as the email's revoke link
// (issue #103: execution.ApprovalToken holds only the hash, and a fresh
// token is minted for every successful approve -- see
// purchase.Manager.ApproveAndExecute). Empty when minting failed
// best-effort; the template's {{if .RevocationToken}} then omits the panel.
func (h *Handler) sendPurchaseExecutedEmail(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution, revocationToken, executedByEmail string) {
	if h.emailNotifier == nil {
		logging.Debug("sendPurchaseExecutedEmail: no email notifier configured, skipping")
		return
	}
	if execution.ExecutionID == "" {
		logging.Warn("sendPurchaseExecutedEmail: empty execution ID, skipping")
		return
	}

	globalCfg, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		logging.Errorf("sendPurchaseExecutedEmail: failed to load global config: %v", err)
		return
	}
	globalNotify := globalNotifyEmail(globalCfg)

	// Gather per-account contact emails for the recommendations.
	contactEmails, err := h.gatherAccountContactEmails(ctx, execution.Recommendations)
	if err != nil {
		logging.Errorf("sendPurchaseExecutedEmail: failed to gather contact emails: %v", err)
		contactEmails = nil
	}

	// Look up the requester's email via their user ID (if available).
	requesterEmail := h.lookupRequesterInfo(ctx, execution)

	// Build the deduplicated To / Cc list.
	// Priority: contact emails are To (first one) + Cc (rest); global notify
	// and requester email are Cc. When there are no contact emails, the global
	// notify becomes To (matching the approval-email fallback in resolveApprovalRecipients).
	to, cc := resolveExecutedNotificationRecipients(contactEmails, globalNotify, requesterEmail)
	if to == "" {
		logging.Warn("sendPurchaseExecutedEmail: no recipients resolved, skipping")
		return
	}

	summaries, totalUpfront, totalSavings := executedEmailCommitments(execution)

	dashboardBase := h.resolveDashboardURL(req)
	data := email.NotificationData{
		DashboardURL:     dashboardBase,
		ExecutionID:      execution.ExecutionID,
		TotalSavings:     totalSavings,
		TotalUpfrontCost: totalUpfront,
		Recommendations:  summaries,
		PartialPurchase:  execution.Status == config.StatusPartiallyCompleted,
		RecipientEmail:   to,
		CCEmails:         cc,
		// A fresh, dedicated revocation token minted by
		// purchase.Manager.ApproveAndExecute for this specific approval
		// (issue #103) so the recipient can trigger a post-execution cancel
		// via the /revoke route.
		RevocationToken: revocationToken,
		// Populate the revocation window deadline so the email copy matches
		// the enforced 24-hour window in validateRevokeToken (Finding #6).
		RevocationWindowClosesAt: revocationWindowClosesAt(execution),
		RequestedByEmail:         requesterEmail,
		// api.User has no display-name field yet; leave RequestedByName empty
		// until a name source lands (the email template tolerates "").
		RequestedByName: "",
		RequestedAt:     executionTimestamp(execution),
		ExecutedBy:      executedByEmail,
		ExecutedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	if dashboardBase != "" {
		data.ArcheraEducationURL = dashboardBase + "/archera-insurance"
	}

	if sendErr := h.emailNotifier.SendPurchaseExecutedNotification(ctx, data); sendErr != nil {
		logging.Errorf("sendPurchaseExecutedEmail: send failed for execution %s: %v", execution.ExecutionID, sendErr)
	}
}

// executedEmailCommitments lists the commitments the executed email reports,
// with their totals. A partially_completed row lists only the recs it bought
// when it records them per rec (issue #532), so the email does not present a
// failed rec as purchased. A multi-account root has no per-rec Purchased
// flags (its per-account rows do), so it lists every rec and the template's
// partial notice says some did not go through.
func executedEmailCommitments(execution *config.PurchaseExecution) (summaries []email.RecommendationSummary, upfront, savings float64) {
	onlyPurchased := execution.Status == config.StatusPartiallyCompleted && anyPurchased(execution.Recommendations)
	if !onlyPurchased {
		upfront, savings = execution.TotalUpfrontCost, execution.EstimatedSavings
	}
	summaries = make([]email.RecommendationSummary, 0, len(execution.Recommendations))
	for i := range execution.Recommendations {
		rec := &execution.Recommendations[i]
		if onlyPurchased && !rec.Purchased {
			continue
		}
		if onlyPurchased {
			upfront += rec.UpfrontCost
			savings += rec.Savings
		}
		summaries = append(summaries, email.RecommendationSummary{
			Service:        rec.Service,
			ResourceType:   rec.ResourceType,
			Engine:         rec.Engine,
			Region:         rec.Region,
			Count:          rec.Count,
			MonthlySavings: rec.Savings,
			Term:           rec.Term,
			Payment:        rec.Payment,
			UpfrontCost:    rec.UpfrontCost,
		})
	}
	return summaries, upfront, savings
}

// anyPurchased reports whether any rec records a committed purchase.
func anyPurchased(recs []config.RecommendationRecord) bool {
	for i := range recs {
		if recs[i].Purchased {
			return true
		}
	}
	return false
}

// globalNotifyEmail returns the trimmed notification email from a GlobalConfig,
// or "" when the config is nil or the field is unset. Extracted from
// sendPurchaseExecutedEmail to keep that function under the cyclomatic limit.
func globalNotifyEmail(globalCfg *config.GlobalConfig) string {
	if globalCfg != nil && globalCfg.NotificationEmail != nil {
		return strings.TrimSpace(*globalCfg.NotificationEmail)
	}
	return ""
}

// lookupRequesterInfo resolves the email for the user who originally submitted
// the execution. The lookup is non-fatal: when auth is unavailable or the user
// cannot be found, "" is returned and the notification is sent without it.
// Extracted from sendPurchaseExecutedEmail to keep that function under the
// cyclomatic limit. Only the email is returned -- api.User carries no display
// name field, so the notification's RequestedByName is populated separately if
// and when a name source lands.
func (h *Handler) lookupRequesterInfo(ctx context.Context, execution *config.PurchaseExecution) (requesterEmail string) {
	if h.auth == nil || execution.CreatedByUserID == nil || *execution.CreatedByUserID == "" {
		return ""
	}
	u, err := h.auth.GetUser(ctx, *execution.CreatedByUserID)
	if err != nil {
		logging.Warnf("lookupRequesterInfo: GetUser(%s) failed: %v", *execution.CreatedByUserID, err)
		return ""
	}
	if u == nil {
		logging.Warnf("lookupRequesterInfo: GetUser(%s) returned nil user", *execution.CreatedByUserID)
		return ""
	}
	return u.Email
}

// resolveExecutedNotificationRecipients builds the To / Cc pair for the
// post-execution notification from the three input email sets. The logic
// mirrors resolveApprovalRecipients: first contact email is To; remaining
// contact emails, globalNotify, and requesterEmail are Cc (deduped). When
// no contact emails are present, globalNotify becomes To and requesterEmail
// becomes Cc.
func resolveExecutedNotificationRecipients(contactEmails []string, globalNotify, requesterEmail string) (to string, cc []string) {
	seen := map[string]bool{}
	addCc := func(addr string) {
		norm := strings.ToLower(strings.TrimSpace(addr))
		if norm == "" || seen[norm] {
			return
		}
		seen[norm] = true
		cc = append(cc, addr)
	}

	if len(contactEmails) > 0 {
		to = contactEmails[0]
		seen[strings.ToLower(strings.TrimSpace(to))] = true
		for _, addr := range contactEmails[1:] {
			addCc(addr)
		}
		addCc(globalNotify)
		addCc(requesterEmail)
		return to, cc
	}

	// No contact emails: fall back to globalNotify as To.
	if globalNotify != "" {
		to = globalNotify
		seen[strings.ToLower(strings.TrimSpace(to))] = true
		addCc(requesterEmail)
		return to, cc
	}

	// Last resort: requester email only.
	to = strings.TrimSpace(requesterEmail)
	return to, nil
}

// executionTimestamp returns a human-readable timestamp for when the execution
// was submitted (ScheduledDate for web-submitted rows). Returns empty string
// when the execution has no timestamp (shouldn't happen in practice but
// guards against zero-value time panics in templates).
func executionTimestamp(exec *config.PurchaseExecution) string {
	if exec.ScheduledDate.IsZero() {
		return ""
	}
	return exec.ScheduledDate.UTC().Format(time.RFC3339)
}

// resolveDashboardURL returns the absolute base URL to embed in email
// approval/cancel links. Preference order matches the OIDC issuer helper's
// strategy for the same underlying problem (Lambda's Function URL can't be
// wired into its own env via Terraform without a cycle, so the canonical
// URL has to be discovered at request time):
//
//  1. h.dashboardURL — set from CUDLY_DASHBOARD_URL when the operator has
//     a custom domain (frontend_domain_names populated in Terraform).
//  2. The inbound request's trusted DomainName (`https://<lambda-url-host>`)
//     — the fallback for bare-Function-URL deployments so emails carry
//     clickable absolute links instead of relative paths.
//
// When both are empty the template renders relative links — still valid if
// the user clicks them from the dashboard itself, but broken in email
// clients. That case is a deployment misconfiguration (no domain, no
// Function URL) and deserves to surface.
func (h *Handler) resolveDashboardURL(req *events.LambdaFunctionURLRequest) string {
	if h.dashboardURL != "" {
		return strings.TrimRight(h.dashboardURL, "/")
	}
	if req != nil && req.RequestContext.DomainName != "" {
		return "https://" + req.RequestContext.DomainName
	}
	return ""
}

// resolveApprovalRecipients computes the To / Cc / authorized-approver sets
// for a purchase approval email based on the recommendations' account
// contact emails and the global Settings → General notification email.
//
// Intent: the account's contact_email is the party accountable for a
// purchase in that account; the global notification inbox is informed for
// visibility. So we direct the email at the contact email(s) as To, list
// any *other* contact emails plus the global notification email as Cc,
// and the approve/cancel token is only honored for session holders whose
// email matches one of the authorized approvers (case-insensitive).
//
// **Authorisation policy** (post-security-hardening): the authorized-
// approver set is ALWAYS the per-account contact_email list, never the
// global notification email. The global notify mailbox is informed of the
// purchase via Cc but cannot itself approve. If no recommendation has a
// per-account contact_email, the approver set is empty and the caller
// must reject the approval with a clear error directing the operator to
// set a contact email on the account. This closes the loophole where a
// catch-all inbox could authorize spend on accounts it doesn't own.
//
// Returns ("", nil, nil, nil) when neither contact_email nor globalNotify
// is configured — the caller surfaces a user-facing error.
func (h *Handler) resolveApprovalRecipients(ctx context.Context, recs []config.RecommendationRecord, globalNotify string) (to string, cc, approvers []string, err error) {
	contactEmails, err := h.gatherAccountContactEmails(ctx, recs)
	if err != nil {
		return "", nil, nil, err
	}

	globalNotify = strings.TrimSpace(globalNotify)

	if len(contactEmails) == 0 {
		// No per-account contact_email anywhere — the global notify mailbox
		// can still receive the email (it's "To" so something gets sent),
		// but it is NOT in the approvers set. authorizeApprovalAction will
		// reject the approve/cancel because approvers is empty.
		if globalNotify == "" {
			return "", nil, nil, nil
		}
		return globalNotify, nil, nil, nil
	}

	to = contactEmails[0]
	approvers = append([]string(nil), contactEmails...)

	// Cc: remaining contact emails + global notification email, deduped
	// against the To address (the SES layer dedupes again as a belt-and-
	// braces guard, but keeping the list tidy here makes logs cleaner).
	seen := map[string]bool{strings.ToLower(to): true}
	appendIfNew := func(addr string) {
		norm := strings.ToLower(strings.TrimSpace(addr))
		if norm == "" || seen[norm] {
			return
		}
		seen[norm] = true
		cc = append(cc, addr)
	}
	for _, addr := range contactEmails[1:] {
		appendIfNew(addr)
	}
	appendIfNew(globalNotify)
	return to, cc, approvers, nil
}

// gatherAccountContactEmails returns the deduped (case-insensitive),
// insertion-ordered list of contact emails for the unique accounts
// referenced by recs. Accounts without a CloudAccountID or without a
// contact_email are silently skipped — they're not an error, just not a
// contribution to the authorized-approver set. A real DB error from
// lookupContactEmail is propagated so the caller surfaces it as a
// retriable failure instead of silently degrading to a globalNotify
// fallback (which would be wrong: a transient DB blip should not change
// who is allowed to approve).
func (h *Handler) gatherAccountContactEmails(ctx context.Context, recs []config.RecommendationRecord) ([]string, error) {
	orderedIDs := uniqueAccountIDsFromRecs(recs)
	if len(orderedIDs) == 0 {
		return nil, nil
	}
	emailsSeen := map[string]bool{}
	var out []string
	for _, id := range orderedIDs {
		addr, err := h.lookupContactEmail(ctx, id)
		if err != nil {
			// Do NOT wrap err with the raw account UUID: this error is
			// returned up to the caller which surfaces it through the
			// router's logging.Errorf("API error: %v") path, leaking the
			// account UUID and the raw DB error string (issue #965, same
			// shape as the validatePlanAccountProviders fix). Return a
			// generic 500 ClientError; lookupContactEmail has already logged
			// the failure point without PII.
			return nil, NewClientError(500, "failed to resolve contact emails")
		}
		if addr == "" {
			continue
		}
		norm := strings.ToLower(addr)
		if emailsSeen[norm] {
			continue
		}
		emailsSeen[norm] = true
		out = append(out, addr)
	}
	return out, nil
}

// uniqueAccountIDsFromRecs returns the account IDs referenced by recs in
// insertion order, with duplicates and blanks stripped.
func uniqueAccountIDsFromRecs(recs []config.RecommendationRecord) []string {
	seen := map[string]bool{}
	var out []string
	for i := range recs {
		rec := recs[i]
		if rec.CloudAccountID == nil || *rec.CloudAccountID == "" {
			continue
		}
		id := *rec.CloudAccountID
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// lookupContactEmail resolves the account's ContactEmail. The caller
// must distinguish three outcomes:
//
//   - real DB error → return ("", err). The caller propagates as a
//     retriable failure rather than silently treating the actor as
//     unauthorized or falling through to globalNotify; a transient
//     blip should not change who is allowed to approve.
//   - account-not-found (GetCloudAccount returns nil, nil per pgx
//     ErrNoRows handling in the postgres store) → return ("", nil).
//     Semantically equivalent to "no contact email configured".
//   - account found but ContactEmail is empty/whitespace →
//     return ("", nil). Same fall-through as above.
//
// Trimming happens here so the caller sees only normalised values.
func (h *Handler) lookupContactEmail(ctx context.Context, id string) (string, error) {
	acct, err := h.config.GetCloudAccount(ctx, id)
	if err != nil {
		// Do NOT log id or err verbatim: id is the raw account UUID and err
		// can be a raw DB error string (issue #965, same shape as the
		// validatePlanAccountProviders fix). Keep the message generic.
		//
		// Log at error level: the caller (gatherAccountContactEmails) maps
		// this into a generic 500 ClientError, which the router emits via the
		// non-error-logging ClientError branch (handler.go: it returns the
		// ce.code/body before reaching logging.Errorf("API error: %v")). This
		// line is therefore the only error-level breadcrumb for the resulting
		// user-visible 500, so it must stay at Errorf to preserve alerting.
		logging.Errorf("lookupContactEmail: GetCloudAccount failed")
		return "", err
	}
	if acct == nil {
		return "", nil
	}
	return strings.TrimSpace(acct.ContactEmail), nil
}

// authorizeApprovalAction returns the actor email to record on an
// approve/cancel action, after enforcing that the session-authenticated
// user's email is on the authorized-approver list for the given execution.
// Returns a 403 ClientError when the session is missing or the email
// doesn't match. The returned actor is stored as approved_by/canceled_by
// on the execution.
//
// Rationale: the approve/cancel API routes are AuthPublic (token-only) for
// backwards compatibility with the email-link flow, but the web UI now
// redirects through login before calling this endpoint (see frontend
// /purchases/{action}/:id). Requiring the session's email to match the
// account's contact_email closes the gap where anyone holding the token
// (forwarded email, shared inbox, stolen link) could act on the purchase.
func (h *Handler) authorizeApprovalAction(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution) (string, error) {
	actor := h.tryResolveActorEmail(ctx, req)
	if actor == "" {
		return "", NewClientError(401, "sign in with the account's contact email to approve or cancel this purchase")
	}

	globalNotify := ""
	if cfg, err := h.config.GetGlobalConfig(ctx); err == nil && cfg != nil && cfg.NotificationEmail != nil {
		globalNotify = *cfg.NotificationEmail
	}
	_, _, approvers, err := h.resolveApprovalRecipients(ctx, execution.Recommendations, globalNotify)
	if err != nil {
		return "", fmt.Errorf("failed to resolve approvers: %w", err)
	}
	if len(approvers) == 0 {
		// No per-account contact_email set on any of this execution's
		// recommendations. Per the authorisation policy in
		// resolveApprovalRecipients, the global notify mailbox is NOT a
		// valid approver — only per-account contact emails are. Direct
		// the operator to set the account's contact_email before approval
		// can proceed.
		return "", NewClientError(403, "no per-account contact email configured for this execution; set the cloud account's contact_email before approving")
	}
	if email.ContainsAddress(approvers, actor) {
		return actor, nil
	}
	return "", NewClientError(403, "your session email is not the authorized approver for this purchase")
}
