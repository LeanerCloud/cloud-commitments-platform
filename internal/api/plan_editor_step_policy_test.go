package api

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

var approvalNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func TestPlanEditorApprovalState(t *testing.T) {
	t.Parallel()
	future, past, zero := approvalNow.Add(time.Hour), approvalNow.Add(-time.Hour), time.Time{}
	cases := []struct {
		name, status string
		stamp        *time.Time
		date         time.Time
		want         PlanEditorApprovalState
		prior        bool
	}{
		{"pending", "pending", nil, future, "not_approved", false},
		{"notified", "notified", nil, future, "not_approved", false},
		{"paused", "paused", nil, future, "not_approved", false},
		{"future", "scheduled", &future, future, "approved_future", false},
		{"past", "scheduled", &past, future, "approved_due", false},
		{"equal", "scheduled", &approvalNow, future, "approved_due", false},
		{"nil delay", "scheduled", nil, future, "legacy_uncertain", false},
		{"zero delay", "scheduled", &zero, future, "legacy_uncertain", false},
		{"legacy", "approved", nil, future, "legacy_uncertain", false},
		{"completed", config.StatusCompleted, nil, future, "not_applicable", false},
		{"partial", config.StatusPartiallyCompleted, nil, future, "not_applicable", false},
		{"canceled", config.StatusCanceled, &future, future, "not_applicable", false},
		{"legacy canceled", config.LegacyStatusCanceled, &future, future, "not_applicable", false},
		{"expired", config.StatusExpired, nil, future, "not_applicable", false},
		{"running", "running", nil, future, "unknown", false},
		{"submitted", "submitted", nil, future, "unknown", false},
		{"failed", config.StatusFailed, nil, future, "unknown", false},
		{"unrecognized", "new-status", nil, future, "unknown", false},
		{"pending zero stamp", "pending", &zero, future, "unknown", true},
		{"notified nonzero stamp", "notified", &past, future, "unknown", true},
		{"paused nonzero stamp", "paused", &future, future, "unknown", true},
		{"past date future delay", "scheduled", &future, past, "approved_future", false},
		{"zero date future delay", "scheduled", &future, zero, "approved_future", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := config.PurchaseExecution{Status: tc.status, ScheduledExecutionAt: tc.stamp, ScheduledDate: tc.date}
			require.Equal(t, tc.want, planEditorApprovalState(&row, approvalNow))
			require.Equal(t, tc.prior, planEditorPriorAction(&row))
			require.Equal(t, tc.want, planEditorApprovalState(&row, approvalNow.In(time.FixedZone("offset", -7*3600))))
			require.Equal(t, tc.status, row.Status)
		})
	}
}

func TestPlanEditorPriorAction(t *testing.T) {
	t.Parallel()
	past := approvalNow.Add(-time.Hour)
	actions := []struct {
		name   string
		change func(*config.PurchaseExecution)
		want   bool
	}{
		{"none", func(*config.PurchaseExecution) {}, false},
		{"selected not purchased", func(e *config.PurchaseExecution) { e.Recommendations = []config.RecommendationRecord{{Selected: true}} }, false},
		{"unselected purchased later", func(e *config.PurchaseExecution) {
			e.Recommendations = []config.RecommendationRecord{{Selected: true}, {Purchased: true}}
		}, true},
		{"executed zero", func(e *config.PurchaseExecution) { e.ExecutedAt = new(time.Time{}) }, true},
		{"executed nonzero", func(e *config.PurchaseExecution) { e.ExecutedAt = &past }, true},
		{"completed zero", func(e *config.PurchaseExecution) { e.CompletedAt = new(time.Time{}) }, true},
		{"completed nonzero", func(e *config.PurchaseExecution) { e.CompletedAt = &past }, true},
		{"empty skip", func(e *config.PurchaseExecution) { e.PreApprovalSkipReason = new("") }, false},
		{"known skip", func(e *config.PurchaseExecution) { e.PreApprovalSkipReason = new("run-now") }, true},
		{"unknown skip", func(e *config.PurchaseExecution) { e.PreApprovalSkipReason = new("unexpected") }, true},
		{"actors only", func(e *config.PurchaseExecution) {
			e.ApprovedBy, e.CancelledBy, e.CreatedByUserID, e.ExecutedByUserID = new("actor"), new("actor"), new("actor"), new("actor")
		}, false},
	}
	statuses := []struct {
		status string
		base   PlanEditorApprovalState
	}{
		{"pending", "not_approved"}, {"notified", "not_approved"}, {"paused", "not_approved"}, {"scheduled", "approved_future"},
		{config.StatusCanceled, "not_applicable"}, {config.LegacyStatusCanceled, "not_applicable"},
		{config.StatusCompleted, "not_applicable"}, {config.StatusPartiallyCompleted, "not_applicable"}, {config.StatusExpired, "not_applicable"},
	}
	for _, status := range statuses {
		for _, action := range actions {
			t.Run(status.status+"/"+action.name, func(t *testing.T) {
				row := config.PurchaseExecution{Status: status.status}
				if status.status == "scheduled" {
					row.ScheduledExecutionAt = new(approvalNow.Add(time.Hour))
				}
				action.change(&row)
				before, err := json.Marshal(row)
				require.NoError(t, err)
				require.Equal(t, action.want, planEditorPriorAction(&row))
				want := status.base
				if action.want && status.base != "not_applicable" {
					want = PlanEditorApprovalUnknown
				}
				require.Equal(t, want, planEditorApprovalState(&row, approvalNow))
				require.Equal(t, want, planEditorApprovalState(&row, approvalNow))
				after, err := json.Marshal(row)
				require.NoError(t, err)
				require.Equal(t, before, after, "classification must not alter captured evidence")
				permuted := row
				permuted.Recommendations = slices.Clone(row.Recommendations)
				slices.Reverse(permuted.Recommendations)
				require.Equal(t, action.want, planEditorPriorAction(&permuted))
				require.Equal(t, want, planEditorApprovalState(&permuted, approvalNow))
			})
		}
	}
}
