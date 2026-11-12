// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/aws/aws-lambda-go/events"
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

// requireSession validates the request's session token and returns the
// session, or a 401 ClientError when no/invalid session is present. Unlike
// requirePermission, it does NOT consult the admin-API-key shortcut — the
// session-authed cancel path needs an actual user UUID for the
// cancel-own check; falling through to the API-key admin role would let
// a key impersonate ownership we cannot verify.
func (h *Handler) requireSession(ctx context.Context, req *events.LambdaFunctionURLRequest) (*Session, error) {
	return h.requireSessionPrincipal(ctx, req)
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

// archeraEducationURL returns dashboardBase + "/archera-insurance", or "" when
// dashboardBase is empty so email templates omit the block safely.
func archeraEducationURL(dashboardBase string) string {
	if dashboardBase == "" {
		return ""
	}
	return dashboardBase + "/archera-insurance"
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
