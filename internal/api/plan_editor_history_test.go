package api

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

func editorHistoryFixture() (*config.PurchasePlan, []config.PurchaseExecution, time.Time) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	rows := []config.PurchaseExecution{
		{ExecutionID: "old", StepNumber: 1, Status: config.StatusCanceled},
		{ExecutionID: "replacement", StepNumber: 1, Status: "pending"},
		{ExecutionID: "legacy-cancel", StepNumber: 2, Status: config.LegacyStatusCanceled},
		{ExecutionID: "child", StepNumber: 2, Status: "completed", RetryAttemptN: 1, CloudAccountID: new("sensitive")},
		{ExecutionID: "source", StepNumber: 3, Status: "failed", RetryExecutionID: new("target")},
		{ExecutionID: "target", StepNumber: 3, Status: "running", RetryAttemptN: 0},
		{ExecutionID: "partial", StepNumber: 4, Status: config.StatusPartiallyCompleted},
		{ExecutionID: "future", StepNumber: 5, Status: "scheduled", ScheduledExecutionAt: &future},
		{ExecutionID: "due", StepNumber: 5, Status: "scheduled", ScheduledExecutionAt: &now},
		{ExecutionID: "literal", StepNumber: 6, Status: "approved"},
		{ExecutionID: "unknown", StepNumber: 9, Status: "unknown"},
		{ExecutionID: "negative", StepNumber: -1, Status: "notified"},
	}
	return &config.PurchasePlan{ID: "plan"}, rows, now
}

func TestPlanEditorHistoryComplete(t *testing.T) {
	plan, rows, now := editorHistoryFixture()
	history, err := buildPlanEditorHistory(plan, rows, now)
	require.NoError(t, err)
	ids := make([]string, 0, len(history))
	for _, row := range history {
		ids = append(ids, row.ExecutionID)
	}
	require.Equal(t, []string{"negative", "old", "replacement", "child", "legacy-cancel", "source", "target", "partial", "due", "future", "literal", "unknown"}, ids)
	require.Equal(t, config.StatusCanceled, history[1].Status)
	require.Equal(t, config.LegacyStatusCanceled, history[4].Status)
	require.Equal(t, "completed", history[3].Status)
	require.Equal(t, "target", *history[5].RetryExecutionID)
	require.Equal(t, 0, history[6].RetryAttemptN)
	require.Equal(t, 1, history[3].RetryAttemptN)
	require.Equal(t, PlanEditorApprovalDue, history[8].ApprovalState)
	require.Equal(t, PlanEditorApprovalFuture, history[9].ApprovalState)
	require.Equal(t, PlanEditorApprovalLegacyUncertain, history[10].ApprovalState)
	require.Equal(t, PlanEditorApprovalUnknown, history[11].ApprovalState)
	slices.Reverse(rows)
	permuted, err := buildPlanEditorHistory(plan, rows, now)
	require.NoError(t, err)
	require.Equal(t, history, permuted)
}

func TestPlanEditorHistoryDetachedAndTruthful(t *testing.T) {
	plan, rows, now := editorHistoryFixture()
	date, err := time.Parse(time.RFC3339, "2026-05-03T23:00:00-02:00")
	require.NoError(t, err)
	rows[4].ScheduledDate = date
	rows[4].Recommendations = []config.RecommendationRecord{
		{Selected: true, Purchased: true, Count: 4, Provider: "aws", Service: "ec2"},
		{Purchased: true, Count: 2, Provider: "aws", Service: "ec2"},
	}
	rows[5].Recommendations = []config.RecommendationRecord{{Selected: true, Count: 3, Provider: "aws", Service: "ec2"}}
	before, err := json.Marshal(rows)
	require.NoError(t, err)
	planBefore, err := json.Marshal(plan)
	require.NoError(t, err)
	history, err := buildPlanEditorHistory(plan, rows, now)
	require.NoError(t, err)
	shown := &history[5]
	require.Equal(t, "2026-05-04", *shown.ScheduledDate)
	require.Equal(t, 6, *shown.Display.Quantity)
	require.Equal(t, PlanEditorQuantityInstances, *shown.Display.QuantityUnit)
	require.Equal(t, PlanEditorValueStoredSnapshot, shown.Display.QuantitySource)
	require.Nil(t, shown.Display.FullTermCommitment)
	require.Nil(t, shown.Display.CommitmentCurrency)
	require.Equal(t, PlanEditorValueUnavailable, shown.Display.CommitmentSource)
	*shown.RetryExecutionID, *shown.ScheduledDate = "changed", "changed"
	*shown.Display.Quantity, *shown.Display.QuantityUnit = 99, PlanEditorQuantityVCPU
	after, err := json.Marshal(rows)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
	planAfter, err := json.Marshal(plan)
	require.NoError(t, err)
	require.JSONEq(t, string(planBefore), string(planAfter))
	require.Nil(t, history[0].Display.Quantity)
	require.Equal(t, 3, *history[6].Display.Quantity)
	require.Equal(t, PlanEditorQuantityInstances, *history[6].Display.QuantityUnit)
	rows[4].Recommendations[1].Provider = "gcp"
	mixed, err := buildPlanEditorHistory(plan, rows, now)
	require.NoError(t, err)
	require.Nil(t, mixed[5].Display.Quantity)
	require.Equal(t, PlanEditorValueUnavailable, mixed[5].Display.QuantitySource)
}

func editorHistoryJSONKeys(t *testing.T, data []byte, expected []string) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &object))
	require.ElementsMatch(t, expected, slices.Collect(maps.Keys(object)))
	return object
}

func TestPlanEditorHistoryJSONBoundary(t *testing.T) {
	plan, rows, now := editorHistoryFixture()
	row := &rows[0]
	row.Error, row.ApprovalToken, row.IdempotencyKey = "sensitive", "sensitive", "sensitive"
	row.ApprovedBy, row.CancelledBy, row.CreatedByUserID, row.ExecutedByUserID = new("sensitive"), new("sensitive"), new("sensitive"), new("sensitive")
	row.CloudAccountID = new("sensitive")
	row.Recommendations = []config.RecommendationRecord{{CloudAccountID: new("sensitive"), Details: json.RawMessage(`{"secret":"sensitive"}`)}}
	history, err := buildPlanEditorHistory(plan, rows, now)
	require.NoError(t, err)
	encoded, err := json.Marshal(history)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "sensitive")
	var objects []json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &objects))
	keys := []string{"execution_id", "status", "step_number", "retry_attempt_n", "scheduled_date", "retry_execution_id", "approval_state", "display"}
	object := editorHistoryJSONKeys(t, objects[1], keys)
	require.JSONEq(t, "null", string(object["scheduled_date"]))
	require.JSONEq(t, "null", string(object["retry_execution_id"]))
	display := editorHistoryJSONKeys(t, object["display"], []string{"resource_type", "region", "quantity", "quantity_unit", "full_term_commitment", "commitment_currency", "quantity_source", "commitment_source"})
	for _, key := range []string{"quantity", "quantity_unit", "full_term_commitment", "commitment_currency"} {
		require.JSONEq(t, "null", string(display[key]))
	}
}

func TestPlanEditorHistoryLimits(t *testing.T) {
	plan, _, now := editorHistoryFixture()
	empty, err := buildPlanEditorHistory(plan, nil, now)
	require.NoError(t, err)
	encoded, err := json.Marshal(empty)
	require.NoError(t, err)
	require.Equal(t, "[]", string(encoded))
	rows := make([]config.PurchaseExecution, 1001)
	for i := range rows {
		rows[i].ExecutionID = strconv.Itoa(1000 - i)
	}
	full, err := buildPlanEditorHistory(plan, rows[:1000], now)
	require.NoError(t, err)
	require.Len(t, full, 1000)
	expected, actual := make([]string, 1000), make([]string, 1000)
	for i := range full {
		expected[i], actual[i] = rows[i].ExecutionID, full[i].ExecutionID
	}
	slices.Sort(expected)
	require.Equal(t, expected, actual)
	oversize, err := buildPlanEditorHistory(plan, rows, now)
	require.ErrorIs(t, err, config.ErrPlanEditorUnsupportedSize)
	require.Nil(t, oversize)
	rows[1].ExecutionID = rows[0].ExecutionID
	duplicate, err := buildPlanEditorHistory(plan, rows[:2], now)
	require.ErrorIs(t, err, ErrPlanEditorProjectionState)
	require.Nil(t, duplicate)
}
