package api

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/aws/aws-lambda-go/events"
)

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
