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
)

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
