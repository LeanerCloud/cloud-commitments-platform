package purchase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/google/uuid"
)

// SendUpcomingPurchaseNotifications sends notifications for upcoming automated purchases.
func (m *Manager) SendUpcomingPurchaseNotifications(ctx context.Context) (*NotificationResult, error) {
	logging.Info("Checking for upcoming purchases to notify...")

	plans, err := m.config.ListPurchasePlans(ctx, config.PurchasePlanFilter{})
	if err != nil {
		return nil, fmt.Errorf("failed to list purchase plans: %w", err)
	}

	notified := 0
	for _rvc := range plans {
		plan := plans[_rvc]
		if m.shouldNotifyPlan(plan) {
			if m.sendPlanNotification(ctx, &plan) {
				notified++
			}
		}
	}

	return &NotificationResult{
		Notified: notified,
	}, nil
}

// shouldNotifyPlan checks if a plan should trigger a notification.
func (m *Manager) shouldNotifyPlan(plan config.PurchasePlan) bool {
	if !plan.Enabled || !plan.AutoPurchase {
		return false
	}

	if plan.NextExecutionDate == nil {
		return false
	}

	daysUntil := int(time.Until(*plan.NextExecutionDate).Hours() / config.HoursPerDay)
	if daysUntil < 0 || daysUntil > plan.NotificationDaysBefore {
		return false
	}

	// Check if we already sent notification recently
	if plan.LastNotificationSent != nil {
		hoursSinceNotification := time.Since(*plan.LastNotificationSent).Hours()
		if hoursSinceNotification < config.MinHoursBetweenNotifications {
			return false
		}
	}

	return true
}

// sendPlanNotification sends a notification for a plan and returns true if successful.
func (m *Manager) sendPlanNotification(ctx context.Context, plan *config.PurchasePlan) bool {
	daysUntil := int(time.Until(*plan.NextExecutionDate).Hours() / config.HoursPerDay)
	logging.Infof("Sending notification for plan %s (purchase in %d days)", plan.Name, daysUntil)

	// Create execution record if doesn't exist
	execution, rawApprovalToken, err := m.getOrCreateExecution(ctx, plan)
	if err != nil {
		logging.Errorf("Failed to create execution: %v", err)
		return false
	}

	// Resolve the notification recipient. The rendered body embeds action tokens
	// and must be delivered via targeted SES, not broadcast via SNS. Log a
	// warning and skip rather than broadcasting if no recipient is configured.
	notifyEmail := ""
	if globalCfg, cfgErr := m.config.GetGlobalConfig(ctx); cfgErr == nil && globalCfg != nil && globalCfg.NotificationEmail != nil {
		notifyEmail = *globalCfg.NotificationEmail
	}
	if notifyEmail == "" {
		logging.Warnf("Skipping scheduled-purchase notification for plan %s: no notification email configured in global settings", plan.Name)
		return false
	}

	// Send notification
	data := m.buildNotificationData(*plan, execution, rawApprovalToken, daysUntil, notifyEmail)
	if err := m.email.SendScheduledPurchaseNotification(ctx, data); err != nil {
		logging.Errorf("Failed to send notification: %v", err)
		return false
	}

	// Update notification sent time. If this fails the suppression timestamp is
	// not persisted, so the next run would resend — return false to avoid
	// double-counting and let the caller decide whether to retry.
	now := time.Now()
	plan.LastNotificationSent = &now
	if err := m.config.UpdatePurchasePlan(ctx, plan); err != nil {
		logging.Errorf("Failed to update plan notification timestamp: %v", err)
		return false
	}

	return true
}

// getOrCreateExecution gets an existing execution or creates a new one, and
// returns the RAW approval token to embed in the notification about to be
// sent. approval_token is hashed at rest (issue #103), so a row fetched from
// the store (the "existing" branch) never carries a raw, emailable token --
// getOrCreateExecution rotates it via rotateApprovalToken (mint + hash +
// persist + return raw, applied to the row already in hand -- see that
// function's doc comment for why it must take the row directly rather than
// re-fetching it) immediately before this notification, with the full
// ApprovalTokenTTL rather than the shorter RevocationWindow the post-approve
// path uses -- but only when the row is still pending/notified; see the
// status guard below for why rotating any other status is unsafe. The
// "create new" branch already holds the raw value it just generated and
// returns that directly instead of rotating again.
func (m *Manager) getOrCreateExecution(ctx context.Context, plan *config.PurchasePlan) (*config.PurchaseExecution, string, error) {
	// Check for existing execution for this date to prevent duplicates.
	// GetExecutionByPlanAndDate wraps ErrNotFound on zero rows; any other
	// error is a real store failure and must propagate.
	existing, err := m.config.GetExecutionByPlanAndDate(ctx, plan.ID, *plan.NextExecutionDate)
	switch {
	case err == nil && existing != nil:
		logging.Debugf("Found existing execution %s for plan %s on %s", existing.ExecutionID, plan.ID, plan.NextExecutionDate)
		// GetExecutionByPlanAndDate filters only on plan_id + scheduled_date,
		// not status, so this row can already be approved/completed/canceled
		// by the time a later notification tick re-runs for the same date
		// (e.g. the plan's NextExecutionDate hasn't advanced yet). Rotating
		// unconditionally would overwrite the hash + expiry a "pending
		// approval" email or a post-approve "purchase executed" revoke email
		// already committed to, 403-ing whichever link is currently live.
		// Only pending/notified rows are still awaiting their first (or a
		// repeat) approval notification.
		if existing.Status != "pending" && existing.Status != "notified" {
			return nil, "", fmt.Errorf("existing execution %s is %s; not re-notifying", existing.ExecutionID, existing.Status)
		}
		rawToken, rotateErr := m.rotateApprovalToken(ctx, existing, config.ApprovalTokenTTL)
		if rotateErr != nil {
			return nil, "", fmt.Errorf("failed to rotate approval token for notification: %w", rotateErr)
		}
		return existing, rawToken, nil
	case err != nil && !errors.Is(err, config.ErrNotFound):
		return nil, "", fmt.Errorf("failed to check for existing execution: %w", err)
	}
	// ErrNotFound (or nil error with nil row): no existing execution for this plan+date; create a new one.

	approvalToken, err := common.GenerateApprovalToken()
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate approval token: %w", err)
	}
	tokenExpiresAt := time.Now().Add(config.ApprovalTokenTTL)
	execution := &config.PurchaseExecution{
		PlanID:      plan.ID,
		ExecutionID: uuid.New().String(),
		Status:      "pending",
		// step_number names the step this row will COMPLETE, not the count
		// already completed, matching api.createPurchaseExecutionsTx
		// (CurrentStep + i + 1). The ramp advance is keyed on this value since
		// issue #1669, so stamping the completed count here would have every
		// notification-created row re-complete a counted step, freezing the ramp.
		StepNumber:             plan.RampSchedule.CurrentStep + 1,
		ScheduledDate:          *plan.NextExecutionDate,
		ApprovalToken:          config.HashApprovalToken(approvalToken),
		ApprovalTokenExpiresAt: &tokenExpiresAt,
	}

	if err := m.config.SavePurchaseExecution(ctx, execution); err != nil {
		return nil, "", err
	}

	return execution, approvalToken, nil
}

// buildNotificationData creates notification data from plan and execution.
// notifyEmail is the global notification address from GlobalConfig; it is set
// as RecipientEmail so the token-bearing body routes through targeted SES.
// rawApprovalToken is the RAW token from getOrCreateExecution -- never read
// from exec.ApprovalToken, which holds only the hash (issue #103).
func (m *Manager) buildNotificationData(plan config.PurchasePlan, exec *config.PurchaseExecution, rawApprovalToken string, daysUntil int, notifyEmail string) email.NotificationData {
	data := email.NotificationData{
		DashboardURL:      m.dashboardURL,
		ApprovalToken:     rawApprovalToken,
		ExecutionID:       exec.ExecutionID,
		PlanID:            plan.ID,
		TotalSavings:      exec.EstimatedSavings,
		TotalUpfrontCost:  exec.TotalUpfrontCost,
		PurchaseDate:      exec.ScheduledDate.Format("January 2, 2006"),
		DaysUntilPurchase: daysUntil,
		PlanName:          plan.Name,
		RecipientEmail:    notifyEmail,
	}

	for _rvc := range exec.Recommendations {
		rec := exec.Recommendations[_rvc]
		data.Recommendations = append(data.Recommendations, email.RecommendationSummary{
			Service:        rec.Service,
			ResourceType:   rec.ResourceType,
			Engine:         rec.Engine,
			Region:         rec.Region,
			Count:          rec.Count,
			MonthlySavings: rec.Savings,
		})
	}

	return data
}
