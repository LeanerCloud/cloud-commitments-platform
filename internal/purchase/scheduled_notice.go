package purchase

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
)

// ErrNoScheduledNotifier is returned by SendScheduledNotice when no notifier is configured.
var ErrNoScheduledNotifier = errors.New("purchase: no scheduled notifier configured")

// ErrNoExecutionID is returned by SendScheduledNotice for an execution without an ID.
var ErrNoExecutionID = errors.New("purchase: scheduled notice needs an execution ID")

// ScheduledNotifier sends the scheduled-purchase notice.
type ScheduledNotifier interface {
	SendPurchaseScheduledNotification(ctx context.Context, data email.NotificationData) error
}

// BuildScheduledNoticeData constructs the email.NotificationData from the execution
// and global config.
func BuildScheduledNoticeData(dashboardURL string, execution *config.PurchaseExecution, globalCfg *config.GlobalConfig, actor string) email.NotificationData {
	var windowClosesAt string
	if execution.ScheduledExecutionAt != nil {
		windowClosesAt = execution.ScheduledExecutionAt.UTC().Format("2006-01-02 15:04 UTC")
	}

	revokeURL := ""
	if dashboardURL != "" {
		revokeURL = dashboardURL + "/purchases#history?execution=" + execution.ExecutionID
	}

	// Build a minimal summaries slice from the stored recommendations.
	summaries := make([]email.RecommendationSummary, 0, len(execution.Recommendations))
	for i := range execution.Recommendations {
		r := execution.Recommendations[i]
		summaries = append(summaries, email.RecommendationSummary{
			Service:      r.Service,
			ResourceType: r.ResourceType,
			Region:       r.Region,
			Count:        r.Count,
			Term:         r.Term,
			Payment:      r.Payment,
			UpfrontCost:  r.UpfrontCost,
		})
	}

	data := email.NotificationData{
		DashboardURL:             dashboardURL,
		ExecutionID:              execution.ExecutionID,
		TotalUpfrontCost:         execution.TotalUpfrontCost,
		TotalSavings:             execution.EstimatedSavings,
		Recommendations:          summaries,
		RevocationWindowClosesAt: windowClosesAt,
		RevokeURL:                revokeURL,
	}

	// Use the global notification email as the recipient (same as approval).
	var globalNotify string
	if globalCfg.NotificationEmail != nil {
		globalNotify = *globalCfg.NotificationEmail
	}
	if globalNotify != "" {
		data.RecipientEmail = globalNotify
	} else if actor != "" {
		data.RecipientEmail = actor
	}

	return data
}

// SendScheduledNotice builds and sends the scheduled-purchase notice, returning
// the send error wrapped so callers can log or act on it.
func SendScheduledNotice(ctx context.Context, n ScheduledNotifier, globalCfg *config.GlobalConfig, dashboardURL string, exec *config.PurchaseExecution, actor string) error {
	if n == nil {
		return ErrNoScheduledNotifier
	}
	if exec.ExecutionID == "" {
		return ErrNoExecutionID
	}
	data := BuildScheduledNoticeData(dashboardURL, exec, globalCfg, actor)
	if err := n.SendPurchaseScheduledNotification(ctx, data); err != nil {
		return fmt.Errorf("send scheduled notice for execution %s: %w", exec.ExecutionID, err)
	}
	return nil
}

// LadderNoticeFacts are what an automatic ladder purchase's notice states
// beyond the execution itself.
type LadderNoticeFacts struct {
	HourlyCommitment float64
	TermYears        int
	AccountLabel     string
	TrancheID        string
}

// SendLadderNotice sends the scheduled notice of an automatic ladder purchase.
// The ladder variant is selected by facts, never by inspecting the execution,
// so SendScheduledNotice and its callers are unchanged.
func SendLadderNotice(ctx context.Context, n ScheduledNotifier, globalCfg *config.GlobalConfig, dashboardURL string, exec *config.PurchaseExecution, facts LadderNoticeFacts) error {
	if n == nil {
		return ErrNoScheduledNotifier
	}
	if exec.ExecutionID == "" {
		return ErrNoExecutionID
	}
	data := BuildScheduledNoticeData(dashboardURL, exec, globalCfg, "")
	data.Ladder = &email.LadderNoticeData{
		HourlyCommitment: facts.HourlyCommitment,
		AnnualExposure:   facts.HourlyCommitment * common.HoursPerYear,
		TermYears:        facts.TermYears,
		AccountLabel:     facts.AccountLabel,
		TrancheID:        facts.TrancheID,
	}
	if err := n.SendPurchaseScheduledNotification(ctx, data); err != nil {
		return fmt.Errorf("send ladder notice for execution %s: %w", exec.ExecutionID, err)
	}
	return nil
}
