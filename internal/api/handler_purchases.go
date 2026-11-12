// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/aws/aws-lambda-go/events"
)

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
