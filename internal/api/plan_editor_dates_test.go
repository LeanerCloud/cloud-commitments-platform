package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

var editorDatesNow = time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)

func editorDay(offset int) *time.Time {
	return new(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, offset))
}

func editorDateSteps(first int, days ...*time.Time) []config.PlanScheduleStep {
	steps := make([]config.PlanScheduleStep, len(days))
	for i, day := range days {
		steps[i] = config.PlanScheduleStep{StepNumber: first + i, ScheduledDate: day}
	}
	return steps
}

// editorDatePolicy stores one slot per date (nil = unknown date).
func editorDatePolicy(dates ...*string) *planEditorFieldPolicyResult {
	graph := &PlanEditorProjection{}
	for i, date := range dates {
		graph.Steps = append(graph.Steps, PlanEditorSlot{StepNumber: i + 1, ScheduledDate: date})
	}
	return &planEditorFieldPolicyResult{Graph: graph}
}

func editorDateCheckAt(now time.Time, current int, policy *planEditorFieldPolicyResult, steps []config.PlanScheduleStep, changed []int, notice int) error {
	set := map[int]bool{}
	for _, n := range changed {
		set[n] = true
	}
	var before *config.PlanEditorSnapshot
	if current > 0 {
		before = &config.PlanEditorSnapshot{Plan: &config.PurchasePlan{RampSchedule: config.RampSchedule{CurrentStep: current}}}
	}
	return validatePlanEditorDates(before, &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Steps: steps}}, set, policy, notice, now)
}

func editorDateCheck(current int, policy *planEditorFieldPolicyResult, steps []config.PlanScheduleStep, changed []int, notice int) error {
	return editorDateCheckAt(editorDatesNow, current, policy, steps, changed, notice)
}

func requireSequenceInvalid(t *testing.T, err error, msg string) {
	t.Helper()
	requireEditorPolicyRefusal(t, err, http.StatusConflict, codeEditorSequenceInvalid, msg)
}

func TestPlanEditorDatesOrder(t *testing.T) {
	changed := []int{1, 2, 3}
	require.NoError(t, editorDateCheck(0, nil, editorDateSteps(1, editorDay(5), editorDay(6), editorDay(9)), changed, 3))
	requireSequenceInvalid(t, editorDateCheck(0, nil, editorDateSteps(1, editorDay(5), editorDay(5)), changed, 3), "equal dates")
	requireSequenceInvalid(t, editorDateCheck(0, nil, editorDateSteps(1, editorDay(6), editorDay(5)), changed, 3), "reversed")
	// 23:00 at UTC-5 on Jan 6 is Jan 7 in UTC, so it collides with Jan 7.
	late := new(time.Date(2030, 1, 6, 23, 0, 0, 0, time.FixedZone("west", -5*3600)))
	requireSequenceInvalid(t, editorDateCheck(0, nil, editorDateSteps(1, late, editorDay(6)), changed, 3), "non-UTC time converts to UTC before comparing")
	require.NoError(t, editorDateCheck(0, nil, editorDateSteps(1, late, editorDay(7)), changed, 3))
}

func TestPlanEditorDatesFixedNeighbours(t *testing.T) {
	fixed := editorDatePolicy(new("2030-01-10"))
	require.NoError(t, editorDateCheck(1, fixed, editorDateSteps(2, editorDay(10)), []int{2}, 3), "day 11 follows fixed day 10")
	requireSequenceInvalid(t, editorDateCheck(1, fixed, editorDateSteps(2, editorDay(9)), []int{2}, 3), "same day as the fixed step")
	requireSequenceInvalid(t, editorDateCheck(1, editorDatePolicy(nil), editorDateSteps(2, editorDay(10)), []int{2}, 3), "unknown fixed date blocks retiming")
	// A changed step also orders the step after it, even if that step is untouched.
	requireSequenceInvalid(t, editorDateCheck(0, nil, editorDateSteps(1, editorDay(8), nil), []int{1}, 3), "changed step with an unknown follower")
	require.NoError(t, editorDateCheck(0, nil, editorDateSteps(1, nil, nil, editorDay(9)), nil, 3), "untouched unknown dates are tolerated")
	require.NoError(t, editorDateCheck(0, nil, editorDateSteps(1, nil, editorDay(8), editorDay(9)), []int{3}, 3), "unknown date not adjacent to a change")
}

func TestPlanEditorDatesWindow(t *testing.T) {
	steps := func(date *time.Time) []config.PlanScheduleStep { return editorDateSteps(1, date) }
	for _, notice := range []int{0, 31} {
		requireSequenceInvalid(t, editorDateCheck(0, nil, steps(editorDay(60)), []int{1}, notice), "notice out of 1..30 with a change")
		require.NoError(t, editorDateCheck(0, nil, steps(editorDay(60)), nil, notice), "notice is irrelevant when nothing changed")
	}
	for _, notice := range []int{1, 30} {
		require.NoError(t, editorDateCheck(0, nil, steps(editorDay(60)), []int{1}, notice))
	}
	date := editorDay(3)
	requireSequenceInvalid(t, editorDateCheckAt(date.Add(-72*time.Hour), 0, nil, steps(date), []int{1}, 3), "exactly now + notice")
	require.NoError(t, editorDateCheckAt(date.Add(-72*time.Hour-time.Second), 0, nil, steps(date), []int{1}, 3), "one second later than the window")
	requireSequenceInvalid(t, editorDateCheck(0, nil, steps(nil), []int{1}, 3), "null date for a changed step")
	requireSequenceInvalid(t, editorDateCheck(0, nil, steps(editorDay(-1)), []int{1}, 3), "past date")
	require.NoError(t, editorDateCheck(0, nil, steps(editorDay(-1)), nil, 3), "an unchanged past date is not re-judged")
}

func TestPlanEditorDatesRefuseMissingStoredSlot(t *testing.T) {
	steps := editorDateSteps(3, editorDay(20))
	for _, policy := range []*planEditorFieldPolicyResult{nil, {}, editorDatePolicy(new("2030-01-02"))} {
		err := editorDateCheck(2, policy, steps, nil, 3)
		requireEditorPolicyRefusal(t, err, http.StatusConflict, codeEditorStateUnsupported, "no slot for CurrentStep")
	}
	require.Nil(t, planEditorExistingSlot(editorDatePolicy(nil), 0))
	require.Equal(t, 2, planEditorExistingSlot(editorDatePolicy(nil, nil), 2).StepNumber)
}
