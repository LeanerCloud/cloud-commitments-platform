package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/aws/aws-lambda-go/events"
)

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

	// A step whose plan was deleted stays refused: the successor a retry
	// mints is web-sourced and would otherwise slip past the fire-time guard (#1048).
	if purchase.IsOrphanedPlanStep(failedExec) {
		return nil, NewClientError(409, purchase.PlanGoneRefusal)
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
