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

func stepPolicyRow(id, status string, step int) config.PurchaseExecution {
	return config.PurchaseExecution{ExecutionID: id, Status: status, StepNumber: step, ScheduledDate: approvalNow.Add(24 * time.Hour)}
}

func TestPlanEditorStepPolicyRoots(t *testing.T) {
	t.Parallel()
	root := stepPolicyRow("root", "pending", 1)
	alter := func(change func(*config.PurchaseExecution)) config.PurchaseExecution {
		row := root
		change(&row)
		return row
	}
	canceled := stepPolicyRow("old", config.StatusCanceled, 1)
	bought := canceled
	bought.Recommendations = []config.RecommendationRecord{{Selected: true}, {Purchased: true}}
	claimed := canceled
	claimed.ExecutedAt = new(time.Time{})
	delayed := canceled
	delayed.ScheduledExecutionAt = new(approvalNow.Add(time.Hour))
	incoming := stepPolicyRow("predecessor", config.StatusFailed, 99)
	incoming.RetryExecutionID = new("root")
	legacyCanceled := canceled
	legacyCanceled.Status = config.LegacyStatusCanceled
	completed, skipped := canceled, canceled
	completed.CompletedAt, skipped.PreApprovalSkipReason = new(time.Time{}), new("unexpected")
	sameStep, canceledPredecessor := incoming, incoming
	sameStep.StepNumber, canceledPredecessor.Status = 1, config.StatusCanceled
	cases := []struct {
		name   string
		rows   []config.PurchaseExecution
		state  PlanEditorRootState
		reason PlanEditorReason
	}{
		{"missing", nil, "missing", ""},
		{"other step ignored", []config.PurchaseExecution{stepPolicyRow("other", "pending", 2)}, "missing", ""},
		{"safe canceled only", []config.PurchaseExecution{canceled, legacyCanceled}, "missing", ""},
		{"legacy canceled old", []config.PurchaseExecution{legacyCanceled, root}, "unique", ""},
		{"canceled completed", []config.PurchaseExecution{completed, root}, "unique", "occupied_capacity"},
		{"canceled skip", []config.PurchaseExecution{skipped, root}, "unique", "occupied_capacity"},
		{"canceled bought only", []config.PurchaseExecution{bought}, "missing", "occupied_capacity"},
		{"incoming same step", []config.PurchaseExecution{root, sameStep}, "missing", "occupied_capacity"},
		{"incoming canceled predecessor", []config.PurchaseExecution{root, canceledPredecessor}, "missing", "occupied_capacity"},
		{"three roots", []config.PurchaseExecution{root, stepPolicyRow("second", "pending", 1), stepPolicyRow("third", "pending", 1)}, "ambiguous", "ambiguous_root"},
		{"root bought", []config.PurchaseExecution{alter(func(e *config.PurchaseExecution) { e.Recommendations = bought.Recommendations })}, "unique", "occupied_capacity"},
		{"root claim", []config.PurchaseExecution{alter(func(e *config.PurchaseExecution) { e.ExecutedAt = new(time.Time{}) })}, "unique", "occupied_capacity"},
		{"root actor only", []config.PurchaseExecution{alter(func(e *config.PurchaseExecution) { e.ApprovedBy = new("actor") })}, "unique", ""},
		{"terminal child", []config.PurchaseExecution{root, alter(func(e *config.PurchaseExecution) { e.Status, e.RetryAttemptN = config.StatusCompleted, 1 })}, "unique", "occupied_capacity"},
		{"failed no submission child", []config.PurchaseExecution{root, alter(func(e *config.PurchaseExecution) {
			e.Status, e.RetryAttemptN, e.NoSubmission = config.StatusFailed, 1, true
		})}, "unique", "occupied_capacity"},
		{"unique", []config.PurchaseExecution{root}, "unique", ""},
		{"ambiguous", []config.PurchaseExecution{root, stepPolicyRow("other", "pending", 1)}, "ambiguous", "ambiguous_root"},
		{"account even empty", []config.PurchaseExecution{alter(func(e *config.PurchaseExecution) { e.CloudAccountID = new("") })}, "missing", "occupied_capacity"},
		{"legacy account", []config.PurchaseExecution{alter(func(e *config.PurchaseExecution) { e.CloudAccountID = new("legacy") })}, "missing", "occupied_capacity"},
		{"retry", []config.PurchaseExecution{alter(func(e *config.PurchaseExecution) { e.RetryAttemptN = 1 })}, "missing", "occupied_capacity"},
		{"outgoing retry", []config.PurchaseExecution{alter(func(e *config.PurchaseExecution) { e.RetryExecutionID = new("next") })}, "missing", "occupied_capacity"},
		{"incoming from other step zero attempt", []config.PurchaseExecution{root, incoming}, "missing", "occupied_capacity"},
		{"removed scoped account", []config.PurchaseExecution{alter(func(e *config.PurchaseExecution) { e.IdempotencyKey = "lineage:removed" })}, "missing", "occupied_capacity"},
		{"fallback scoped ID", []config.PurchaseExecution{alter(func(e *config.PurchaseExecution) { e.ExecutionID = "lineage:removed" })}, "missing", "occupied_capacity"},
		{"explicit lineage precedence", []config.PurchaseExecution{alter(func(e *config.PurchaseExecution) { e.ExecutionID, e.IdempotencyKey = "old:scope", "unscoped" })}, "unique", ""},
		{"safe canceled old", []config.PurchaseExecution{canceled, root}, "unique", ""},
		{"safe canceled delayed", []config.PurchaseExecution{delayed, root}, "unique", ""},
		{"canceled unselected bought", []config.PurchaseExecution{bought, root}, "unique", "occupied_capacity"},
		{"canceled claim", []config.PurchaseExecution{claimed, root}, "unique", "occupied_capacity"},
		{"live child", []config.PurchaseExecution{root, alter(func(e *config.PurchaseExecution) { e.ExecutionID = "child"; e.CloudAccountID = new("account") })}, "unique", "occupied_capacity"},
		{"unknown child", []config.PurchaseExecution{root, alter(func(e *config.PurchaseExecution) { e.Status, e.RetryAttemptN = "unknown", 1 })}, "unique", "occupied_capacity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grants := PlanEditorAuthority{AddMissingRoot: true, Rows: map[string]PlanEditorRowAuthority{"root": {true, true}, "old:scope": {true, true}}}
			grantBefore, err := json.Marshal(grants)
			require.NoError(t, err)
			before, err := json.Marshal(tc.rows)
			require.NoError(t, err)
			p := buildPlanEditorStepPolicy(tc.rows, 1, grants, approvalNow, nil)
			if tc.state == "unique" {
				require.NotNil(t, p.Root, "unique pending root must retain its original identity")
			}
			if tc.name == "root bought" || tc.name == "root claim" {
				require.Equal(t, PlanEditorApprovalUnknown, p.ApprovalState)
			}
			require.Equal(t, tc.state, p.RootState)
			require.Equal(t, tc.reason == "", p.StructuralChangeAllowed)
			if tc.reason == "" {
				require.Nil(t, p.Reason)
			} else {
				require.NotNil(t, p.Reason)
				require.Equal(t, tc.reason, *p.Reason)
				require.EqualValues(t, "fixed", p.Capability)
			}
			if tc.state == "unique" {
				found := false
				for i := range tc.rows {
					if p.Root == &tc.rows[i] {
						found = true
					}
				}
				require.True(t, found, "root must point into original slice")
			} else {
				require.Nil(t, p.Root)
				require.EqualValues(t, "unknown", p.ApprovalState)
			}
			after, err := json.Marshal(tc.rows)
			require.NoError(t, err)
			require.Equal(t, before, after)
			grantAfter, err := json.Marshal(grants)
			require.NoError(t, err)
			require.Equal(t, grantBefore, grantAfter)
			require.Equal(t, p, buildPlanEditorStepPolicy(tc.rows, 1, grants, approvalNow, nil))
			reversed := slices.Clone(tc.rows)
			slices.Reverse(reversed)
			require.Equal(t, p, buildPlanEditorStepPolicy(reversed, 1, grants, approvalNow, nil))
		})
	}
}

func TestPlanEditorStepPolicyEligibility(t *testing.T) {
	t.Parallel()
	future, past, zero := approvalNow.Add(time.Hour), approvalNow.Add(-time.Hour), time.Time{}
	cases := []struct {
		name, status string
		date         time.Time
		delay        *time.Time
		reason       PlanEditorReason
		approval     PlanEditorApprovalState
	}{
		{"pending", "pending", future, nil, "", "not_approved"},
		{"notified", "notified", future, nil, "", "not_approved"},
		{"zero date", "pending", zero, nil, "invalid_date", "not_approved"},
		{"past date", "pending", past, nil, "not_future", "not_approved"},
		{"equal date", "pending", approvalNow, nil, "not_future", "not_approved"},
		{"scheduled future", "scheduled", future, &future, "", "approved_future"},
		{"scheduled nil", "scheduled", future, nil, "legacy_uncertain", "legacy_uncertain"},
		{"scheduled zero", "scheduled", future, &zero, "legacy_uncertain", "legacy_uncertain"},
		{"scheduled past", "scheduled", future, &past, "not_future", "approved_due"},
		{"scheduled equal", "scheduled", future, &approvalNow, "not_future", "approved_due"},
		{"past date future delay", "scheduled", past, &future, "not_future", "approved_future"},
		{"legacy", "approved", future, nil, "legacy_uncertain", "legacy_uncertain"},
		{"paused", "paused", future, nil, "status_fixed", "not_approved"},
		{"running", "running", future, nil, "status_fixed", "unknown"},
		{"submitted", "submitted", future, nil, "status_fixed", "unknown"},
		{"completed", config.StatusCompleted, future, nil, "status_fixed", "not_applicable"},
		{"partial", config.StatusPartiallyCompleted, future, nil, "status_fixed", "not_applicable"},
		{"expired", config.StatusExpired, future, nil, "status_fixed", "not_applicable"},
		{"failed no submission", config.StatusFailed, future, nil, "status_fixed", "unknown"},
		{"unknown", "new-status", future, nil, "status_fixed", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := config.PurchaseExecution{ExecutionID: "root", StepNumber: 1, Status: tc.status, ScheduledDate: tc.date, ScheduledExecutionAt: tc.delay, NoSubmission: true}
			rows := []config.PurchaseExecution{row}
			p := buildPlanEditorStepPolicy(rows, 1, PlanEditorAuthority{Rows: map[string]PlanEditorRowAuthority{"root": {true, true}}}, approvalNow, nil)
			require.Same(t, &rows[0], p.Root)
			require.Equal(t, tc.status, p.Root.Status)
			require.Equal(t, tc.approval, p.ApprovalState)
			require.Equal(t, tc.reason == "", p.StructuralChangeAllowed)
			if tc.reason != "" {
				require.NotNil(t, p.Reason)
				require.Equal(t, tc.reason, *p.Reason)
				require.Equal(t, PlanEditorFixed, p.Capability)
				require.False(t, p.Editable)
				require.False(t, p.RequiresApprovedEdits)
			} else if tc.status == "scheduled" {
				require.Equal(t, PlanEditorApprovedToggleRequired, p.Capability)
				require.NotNil(t, p.Reason)
				require.Equal(t, PlanEditorApprovedEditsDisabled, *p.Reason)
				require.True(t, p.RequiresApprovedEdits)
				require.False(t, p.Editable)
			} else {
				require.Equal(t, PlanEditorReplacePending, p.Capability)
				require.True(t, p.Editable)
				require.Nil(t, p.Reason)
			}
		})
	}
}

func TestPlanEditorStepPolicyAuthority(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"pending", "notified", "scheduled"} {
		for _, grant := range []struct {
			name string
			rows map[string]PlanEditorRowAuthority
		}{
			{"nil", nil}, {"empty", map[string]PlanEditorRowAuthority{}},
			{"other ID", map[string]PlanEditorRowAuthority{"other": {true, true}}},
			{"wrong operation", map[string]PlanEditorRowAuthority{"root": {ReplacePending: status == "scheduled", ReplaceScheduled: status != "scheduled"}}},
		} {
			t.Run(status+"/"+grant.name, func(t *testing.T) {
				rows := []config.PurchaseExecution{{ExecutionID: "root", StepNumber: 1, Status: status, ScheduledDate: approvalNow.Add(time.Hour)}}
				if status == "scheduled" {
					rows[0].ScheduledExecutionAt = new(approvalNow.Add(time.Hour))
				}
				p := buildPlanEditorStepPolicy(rows, 1, PlanEditorAuthority{AddMissingRoot: true, Rows: grant.rows}, approvalNow, nil)
				require.True(t, p.StructuralChangeAllowed)
				require.Equal(t, PlanEditorDenied, p.Capability)
				require.NotNil(t, p.Reason)
				require.Equal(t, PlanEditorPermissionDenied, *p.Reason)
				require.False(t, p.Editable)
				require.False(t, p.RequiresApprovedEdits)
			})
		}
	}
	for _, add := range []bool{false, true} {
		p := buildPlanEditorStepPolicy(nil, 1, PlanEditorAuthority{AddMissingRoot: add}, approvalNow, nil)
		require.True(t, p.StructuralChangeAllowed)
		require.Nil(t, p.Root)
		require.False(t, p.Editable)
		require.False(t, p.RequiresApprovedEdits)
		if add {
			require.Equal(t, PlanEditorNeedsCreation, p.Capability)
			require.Nil(t, p.Reason)
		} else {
			require.Equal(t, PlanEditorDenied, p.Capability)
			require.NotNil(t, p.Reason)
			require.Equal(t, PlanEditorPermissionDenied, *p.Reason)
		}
	}
}

func TestPlanEditorStepPolicyOrdinal(t *testing.T) {
	t.Parallel()
	rows := []config.PurchaseExecution{stepPolicyRow("first", "running", 1), stepPolicyRow("second", "pending", 2)}
	p := buildPlanEditorStepPolicy(rows, 2, PlanEditorAuthority{Rows: map[string]PlanEditorRowAuthority{"second": {ReplacePending: true}}}, approvalNow, nil)
	require.Same(t, &rows[1], p.Root)
	require.True(t, p.StructuralChangeAllowed)
	require.Equal(t, PlanEditorReplacePending, p.Capability)
	require.True(t, p.Editable)
}
