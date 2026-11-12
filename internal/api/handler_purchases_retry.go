package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
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
