package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/aws/aws-lambda-go/events"
)

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
