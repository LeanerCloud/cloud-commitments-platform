// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
