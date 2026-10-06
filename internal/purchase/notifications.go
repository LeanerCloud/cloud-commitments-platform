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

// errExecutionNotNotifiable marks an existing execution for the plan's date
// that has already left pending/notified; every notification tick until the
// plan's NextExecutionDate advances hits it, so it is skipped quietly.
var errExecutionNotNotifiable = errors.New("execution is no longer awaiting approval")

// sendPlanNotification sends a notification for a plan and returns true if successful.
func (m *Manager) sendPlanNotification(ctx context.Context, plan *config.PurchasePlan) bool {
	daysUntil := int(time.Until(*plan.NextExecutionDate).Hours() / config.HoursPerDay)
	logging.Infof("Sending notification for plan %s (purchase in %d days)", plan.Name, daysUntil)

	// Resolve the notification recipient before touching any token. The
	// rendered body embeds action tokens and must be delivered via targeted
	// SES, not broadcast via SNS. Log a warning and skip rather than
	// broadcasting if no recipient is configured.
	notifyEmail := m.globalNotificationEmail(ctx)
	if notifyEmail == "" {
		logging.Warnf("Skipping scheduled-purchase notification for plan %s: no notification email configured in global settings", plan.Name)
		return false
	}

	execution, rawApprovalToken, rotationPending, err := m.getOrCreateExecution(ctx, plan)
	if errors.Is(err, errExecutionNotNotifiable) {
		logging.Debugf("Skipping notification for plan %s: %v", plan.Name, err)
		return false
	}
	if err != nil {
		logging.Errorf("Failed to create execution: %v", err)
		return false
	}

	data := m.buildNotificationData(*plan, execution, rawApprovalToken, daysUntil, notifyEmail)
	if err := m.email.SendScheduledPurchaseNotification(ctx, data); err != nil {
		logging.Errorf("Failed to send notification: %v", err)
		return false
	}

	// Persist a rotated token only after the email carrying it went out, so a
	// failed send leaves the previously emailed link live (issue #103). On
	// failure the timestamp below is not written and the next tick resends.
	if rotationPending {
		rotated, rotErr := m.config.RotatePendingApprovalToken(ctx, execution.ExecutionID, execution.ApprovalToken, *execution.ApprovalTokenExpiresAt)
		if rotErr != nil || !rotated {
			logging.Errorf("purchase[%s]: notification sent but its approval token was not persisted (rotated=%t): %v",
				execution.ExecutionID, rotated, rotErr)
			return false
		}
	}

	// Update notification sent time. If this fails the suppression timestamp is
	// not persisted, so the next run would resend — return false to avoid
	// double-counting and let the caller decide whether to retry.
	now := time.Now()
	if err := m.config.StampPlanNotificationSent(ctx, plan.ID, now); err != nil {
		logging.Errorf("Failed to update plan notification timestamp: %v", err)
		return false
	}

	return true
}

// globalNotificationEmail returns the configured global notification
// address, or "" when none is set or the config can't be read.
func (m *Manager) globalNotificationEmail(ctx context.Context) string {
	if globalCfg, cfgErr := m.config.GetGlobalConfig(ctx); cfgErr == nil && globalCfg != nil && globalCfg.NotificationEmail != nil {
		return *globalCfg.NotificationEmail
	}
	return ""
}

// getOrCreateExecution gets an existing execution or creates a new one, and
// returns the RAW approval token to embed in the notification about to be
// sent. Only the token's hash is stored (issue #103), so an existing row
// never carries an emailable token: a fresh one is minted into the returned
// copy (hash + ApprovalTokenTTL expiry) but NOT persisted, and
// rotationPending tells the caller to persist it via
// RotatePendingApprovalToken once the email has been sent. A new row is
// saved with its hash up front, since nothing was emailed for it yet.
func (m *Manager) getOrCreateExecution(ctx context.Context, plan *config.PurchasePlan) (execution *config.PurchaseExecution, rawToken string, rotationPending bool, err error) {
	// Check for existing execution for this date to prevent duplicates.
	// GetExecutionByPlanAndDate wraps ErrNotFound on zero rows; any other
	// error is a real store failure and must propagate.
	existing, err := m.config.GetExecutionByPlanAndDate(ctx, plan.ID, *plan.NextExecutionDate)
	switch {
	case err == nil && existing != nil:
		logging.Debugf("Found existing execution %s for plan %s on %s", existing.ExecutionID, plan.ID, plan.NextExecutionDate)
		// GetExecutionByPlanAndDate filters only on plan_id + scheduled_date,
		// not status, so this row can already be approved/completed/canceled
		// by the time a later notification tick re-runs for the same date.
		// Rotating then would kill whichever link (approval or post-approve
		// revoke) is currently live.
		if existing.Status != "pending" && existing.Status != "notified" {
			return nil, "", false, fmt.Errorf("%w: %s is %s", errExecutionNotNotifiable, existing.ExecutionID, existing.Status)
		}
		if len(existing.Recommendations) == 0 {
			return nil, "", false, m.failExistingBarePlanStep(ctx, existing)
		}
		tok, genErr := common.GenerateApprovalToken()
		if genErr != nil {
			return nil, "", false, fmt.Errorf("failed to generate approval token: %w", genErr)
		}
		expiry := time.Now().Add(config.ApprovalTokenTTL)
		existing.ApprovalToken = config.HashApprovalToken(tok)
		existing.ApprovalTokenExpiresAt = &expiry
		return existing, tok, true, nil
	case err != nil && !errors.Is(err, config.ErrNotFound):
		return nil, "", false, fmt.Errorf("failed to check for existing execution: %w", err)
	}
	// ErrNotFound (or nil error with nil row): no existing execution for this
	// plan+date. Nothing attaches recommendations to a plan step yet
	// (platform#609), so the row is recorded as failed instead of emailing a $0
	// approval link. The failed row also makes every later tick for this date
	// return errExecutionNotNotifiable quietly rather than minting another.
	//
	// step_number names the step this row will COMPLETE, not the count already
	// completed, matching api.createPurchaseExecutionsTx (CurrentStep + 1). The
	// ramp advance is keyed on this value since issue #1669.
	execution = &config.PurchaseExecution{
		PlanID:        plan.ID,
		ExecutionID:   uuid.New().String(),
		Status:        "pending",
		StepNumber:    plan.RampSchedule.CurrentStep + 1,
		ScheduledDate: *plan.NextExecutionDate,
	}
	return nil, "", false, m.recordFailedBarePlanStep(ctx, execution)
}

// failExistingBarePlanStep fails a pending/notified plan-step row that carries
// no recommendations. The status change is a compare-and-set, so a human
// approving the row between this tick's read and now wins: the row is no
// longer awaiting approval and the tick skips it, instead of an upsert from the
// stale read overwriting the approval. A won CAS returns the fresh row, which
// is the one the error text is written onto.
func (m *Manager) failExistingBarePlanStep(ctx context.Context, exec *config.PurchaseExecution) error {
	failed, err := m.config.TransitionExecutionStatus(ctx, exec.ExecutionID, []string{"pending", "notified"}, "failed", nil)
	if errors.Is(err, config.ErrNotFound) || errors.Is(err, config.ErrExecutionNotInExpectedStatus) {
		return fmt.Errorf("%w: %s changed state before it could be failed", errExecutionNotNotifiable, exec.ExecutionID)
	}
	if err != nil {
		return fmt.Errorf("failed to fail plan step %d of plan %s: %w", exec.StepNumber, exec.PlanID, err)
	}
	return m.recordFailedBarePlanStep(ctx, failed)
}

// recordFailedBarePlanStep stamps the failure reason onto exec (already failed,
// or a new row), saves it, logs it once, and returns errExecutionNotNotifiable
// so the notification tick sends nothing for it.
func (m *Manager) recordFailedBarePlanStep(ctx context.Context, exec *config.PurchaseExecution) error {
	exec.Status = "failed"
	exec.Error = ErrPlanStepNoRecommendations.Error()
	if err := m.config.SavePurchaseExecution(ctx, exec); err != nil {
		return fmt.Errorf("failed to record plan step %d of plan %s as failed: %w", exec.StepNumber, exec.PlanID, err)
	}
	logging.Errorf("purchase[%s]: plan %s step %d has no recommendations and was marked failed: %v",
		exec.ExecutionID, exec.PlanID, exec.StepNumber, ErrPlanStepNoRecommendations)
	return fmt.Errorf("%w: %s has no recommendations", errExecutionNotNotifiable, exec.ExecutionID)
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
