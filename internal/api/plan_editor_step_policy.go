package api

import (
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

type PlanEditorApprovalState string

const (
	PlanEditorApprovalNotApproved     PlanEditorApprovalState = "not_approved"
	PlanEditorApprovalFuture          PlanEditorApprovalState = "approved_future"
	PlanEditorApprovalDue             PlanEditorApprovalState = "approved_due"
	PlanEditorApprovalLegacyUncertain PlanEditorApprovalState = "legacy_uncertain"
	PlanEditorApprovalNotApplicable   PlanEditorApprovalState = "not_applicable"
	PlanEditorApprovalUnknown         PlanEditorApprovalState = "unknown"
)

func planEditorApprovalState(row *config.PurchaseExecution, evaluatedAt time.Time) PlanEditorApprovalState {
	switch row.Status {
	case "pending", "notified", "paused":
		if planEditorPriorAction(row) {
			return PlanEditorApprovalUnknown
		}
		return PlanEditorApprovalNotApproved
	case "scheduled":
		if planEditorPriorAction(row) {
			return PlanEditorApprovalUnknown
		}
		if row.ScheduledExecutionAt == nil || row.ScheduledExecutionAt.IsZero() {
			return PlanEditorApprovalLegacyUncertain
		}
		if row.ScheduledExecutionAt.After(evaluatedAt) {
			return PlanEditorApprovalFuture
		}
		return PlanEditorApprovalDue
	case "approved":
		return PlanEditorApprovalLegacyUncertain
	case config.StatusCompleted, config.StatusPartiallyCompleted, config.StatusCanceled, config.LegacyStatusCanceled, config.StatusExpired:
		return PlanEditorApprovalNotApplicable
	default:
		return PlanEditorApprovalUnknown
	}
}

func planEditorPriorAction(row *config.PurchaseExecution) bool {
	if row.ExecutedAt != nil || row.CompletedAt != nil || row.PreApprovalSkipReason != nil && *row.PreApprovalSkipReason != "" {
		return true
	}
	for i := range row.Recommendations {
		if row.Recommendations[i].Purchased {
			return true
		}
	}
	switch row.Status {
	case "pending", "notified", "paused":
		return row.ScheduledExecutionAt != nil
	}
	return false
}
