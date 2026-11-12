// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
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
