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
