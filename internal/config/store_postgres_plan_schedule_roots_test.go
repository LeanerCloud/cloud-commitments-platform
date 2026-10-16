package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCapturePlanScheduleWindowsCopiesFireTimesAndHandlesNewPlans(t *testing.T) {
	require.Empty(t, capturePlanScheduleWindows(nil))
	fire := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	captured := fire
	before := &PlanEditorSnapshot{Executions: []PurchaseExecution{{ExecutionID: "a", ScheduledExecutionAt: &fire}, {ExecutionID: "b"}}}
	windows := capturePlanScheduleWindows(before)
	*before.Executions[0].ScheduledExecutionAt = fire.Add(-time.Hour)
	require.True(t, captured.Equal(*windows["a"].execution.ScheduledExecutionAt), "a build callback must not change the guard input")
	require.Nil(t, windows["b"].execution.ScheduledExecutionAt)
}
