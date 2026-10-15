package api

import (
	"maps"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

func graphFixture() (*config.PlanEditorSnapshot, PlanEditorAuthority, time.Time) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	s := &config.PlanEditorSnapshot{Plan: &config.PurchasePlan{ID: "plan", RampSchedule: config.RampSchedule{Type: "weekly", TotalSteps: 3, PercentPerStep: 33}}}
	a := PlanEditorAuthority{AddMissingRoot: true, Rows: map[string]PlanEditorRowAuthority{}}
	for i, id := range []string{"a", "b", "c"} {
		s.Executions = append(s.Executions, config.PurchaseExecution{ExecutionID: id, StepNumber: i + 1, Status: "pending", ScheduledDate: now.AddDate(0, 0, i+2)})
		a.Rows[id] = PlanEditorRowAuthority{ReplacePending: true, ReplaceScheduled: true}
	}
	return s, a, now
}

func graphResult(t *testing.T, s *config.PlanEditorSnapshot, a PlanEditorAuthority, now time.Time) *PlanEditorProjection {
	t.Helper()
	g, err := buildPlanEditorProjection(s, a, now)
	require.NoError(t, err)
	return g
}

func TestPlanEditorProjectionPrefix(t *testing.T) {
	for _, current := range []int{0, 1, 2, 3} {
		for _, kind := range []string{"completed", "partially_completed", "missing", "ambiguous", "child", "nil_stamp", "zero_stamp", "zero_completed", "zero_partially_completed"} {
			t.Run(kind+string(rune('0'+current)), func(t *testing.T) {
				s, a, now := graphFixture()
				s.Plan.RampSchedule.CurrentStep = current
				row := &s.Executions[0]
				row.Status, row.CompletedAt = kind, &now
				switch kind {
				case "missing":
					s.Executions = s.Executions[1:]
				case "ambiguous":
					duplicate := *row
					duplicate.ExecutionID = "other"
					s.Executions = append(s.Executions, duplicate)
				case "child":
					row.CloudAccountID = new("child")
				case "nil_stamp":
					row.Status, row.CompletedAt = "completed", nil
				case "zero_stamp":
					row.Status, row.CompletedAt = "completed", new(time.Time)
				case "zero_completed", "zero_partially_completed":
					row.Status, row.ScheduledDate = strings.TrimPrefix(kind, "zero_"), time.Time{}
				}
				g := graphResult(t, s, a, now)
				require.Len(t, g.Steps, 3)
				require.Equal(t, 99.0, g.Steps[2].CumulativeTargetPercent)
				for i := range g.Steps {
					require.Equal(t, PlanEditorCompletionPlanProgress, g.Steps[i].LogicalCompletionSource)
					require.Equal(t, i < current, g.Steps[i].LogicalCompleted)
				}
				for i := range current {
					require.Equal(t, PlanEditorFixed, g.Steps[i].Capability)
					require.False(t, g.Steps[i].Editable || g.Steps[i].RequiresApprovedEdits)
				}
				if current > 0 {
					proven := kind == "completed" || kind == "partially_completed" || kind == "zero_completed" || kind == "zero_partially_completed"
					require.Equal(t, proven, g.Steps[0].ExecutionID != nil)
					if !proven {
						require.Nil(t, g.Steps[0].Display)
						require.Equal(t, PlanEditorCompletionUnavailable, *g.Steps[0].LockReason)
					} else if row.ScheduledDate.IsZero() {
						require.Nil(t, g.Steps[0].ScheduledDate)
						require.NotNil(t, g.Steps[0].Display)
						require.Equal(t, PlanEditorInvalidDate, *g.Steps[0].LockReason)
					} else {
						require.Equal(t, "2026-05-03", *g.Steps[0].ScheduledDate)
						require.Equal(t, PlanEditorCompletedPrefix, *g.Steps[0].LockReason)
					}
				}
			})
		}
	}
}

func TestPlanEditorProjectionDateIntervals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dates   [3]string
		current int
		running bool
		fixed   []int
	}{
		{"ordered", [3]string{"03T00:00:00Z", "04T00:00:00Z", "05T00:00:00Z"}, 0, false, nil},
		{"reversed", [3]string{"05T00:00:00Z", "04T00:00:00Z", "03T00:00:00Z"}, 0, false, []int{0, 1, 2}},
		{"same_day", [3]string{"03T01:00:00Z", "03T20:00:00Z", "05T00:00:00Z"}, 0, false, []int{0, 1}},
		{"utc_rollover", [3]string{"03T23:00:00-02:00", "04T23:00:00+02:00", "05T00:00:00Z"}, 0, false, []int{0, 1}},
		{"prefix_boundary", [3]string{"05T00:00:00Z", "04T00:00:00Z", "06T00:00:00Z"}, 1, false, []int{0, 1}},
		{"fixed_successor", [3]string{"03T00:00:00Z", "05T00:00:00Z", "04T00:00:00Z"}, 0, true, []int{1, 2}},
		{"zero_gap", [3]string{"05T00:00:00Z", "", "03T00:00:00Z"}, 0, false, []int{0, 1, 2}},
		{"zero_prefix_crossing", [3]string{"05T00:00:00Z", "", "03T00:00:00Z"}, 2, false, []int{0, 1, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, now := graphFixture()
			s.Plan.RampSchedule.CurrentStep = tc.current
			for i, value := range tc.dates {
				s.Executions[i].ScheduledDate, _ = time.Parse(time.RFC3339, "2026-05-"+value)
				if i < tc.current {
					s.Executions[i].Status, s.Executions[i].CompletedAt = "completed", &now
				}
			}
			if tc.running {
				s.Executions[2].Status = "running"
			}
			g := graphResult(t, s, a, now)
			for i, slot := range g.Steps {
				if slices.Contains(tc.fixed, i) {
					require.Equal(t, PlanEditorFixed, slot.Capability)
					require.False(t, slot.Editable || slot.RequiresApprovedEdits)
					reason := PlanEditorDateOrder
					if tc.dates[i] == "" {
						reason = PlanEditorInvalidDate
					}
					require.Equal(t, reason, *slot.LockReason)
				} else {
					require.True(t, slot.Editable)
				}
			}
			if len(tc.fixed) > 0 {
				require.Equal(t, PlanEditorDateOrder, *g.ShapeLockReason)
				require.False(t, g.StructuralShapeChangeAllowed)
			}
		})
	}
}

func TestPlanEditorProjectionShapeAndComposition(t *testing.T) {
	s, a, now := graphFixture()
	a.Rows = nil
	g := graphResult(t, s, a, now)
	require.True(t, g.StructuralShapeChangeAllowed)
	require.Nil(t, g.ShapeLockReason)
	require.Equal(t, PlanEditorDenied, g.Steps[0].Capability)
	for _, baseline := range []config.PlanRampBaseline{{}, {"key": {}}} {
		s.Baseline = baseline
		a.Rows = map[string]PlanEditorRowAuthority{"a": {ReplacePending: true}}
		g = graphResult(t, s, a, now)
		require.False(t, g.StructuralShapeChangeAllowed)
		require.Equal(t, PlanEditorProgressOrBaseline, *g.ShapeLockReason)
		require.True(t, g.Steps[0].Editable)
	}
	for _, status := range []string{"pending", "scheduled", "running", "unknown", "missing", "ambiguous"} {
		s, a, now = graphFixture()
		s.Executions[0].Status = status
		if status == "scheduled" {
			future := now.Add(time.Hour)
			s.Executions[0].ScheduledExecutionAt = &future
		}
		if status == "missing" {
			s.Executions = s.Executions[1:]
		}
		if status == "ambiguous" {
			duplicate := s.Executions[0]
			duplicate.ExecutionID = "duplicate"
			s.Executions = append(s.Executions, duplicate)
		}
		g = graphResult(t, s, a, now)
		p := buildPlanEditorStepPolicy(s.Executions, 1, a, now)
		require.Equal(t, p.Capability, g.Steps[0].Capability)
		require.Equal(t, p.RequiresApprovedEdits, g.Steps[0].RequiresApprovedEdits)
		if !p.StructuralChangeAllowed {
			require.Equal(t, p.Reason, g.ShapeLockReason)
		}
		history, err := buildPlanEditorHistory(s.Plan, s.Executions, now)
		require.NoError(t, err)
		require.Equal(t, history, g.History)
	}
}

func TestPlanEditorProjectionJSONAndOwnership(t *testing.T) {
	s, a, now := graphFixture()
	for i := range 2 {
		s.Executions[i].Recommendations = []config.RecommendationRecord{{Selected: true, Count: i + 2, Provider: "aws", Service: "ec2"}}
	}
	before, authBefore := historyJSON(t, s), historyJSON(t, a)
	g := graphResult(t, s, a, now)
	object := historyJSON(t, g)
	require.ElementsMatch(t, slices.Collect(maps.Keys(object)), []string{"plan_id", "revision", "current_step", "total_steps", "structural_shape_change_allowed", "shape_lock_reason", "steps", "history"})
	require.Nil(t, object["shape_lock_reason"])
	keys := []string{"step_number", "logical_completed", "logical_completion_source", "execution_id", "scheduled_date", "status", "cumulative_target_percent", "root_state", "approval_state", "capability", "lock_reason", "editable", "requires_approved_edits", "display"}
	slot := historyJSON(t, g.Steps[0])
	require.ElementsMatch(t, keys, slices.Collect(maps.Keys(slot)))
	require.Nil(t, slot["lock_reason"])
	require.Nil(t, g.Steps[0].Display.FullTermCommitment)
	require.Equal(t, PlanEditorValueStoredSnapshot, g.Steps[0].Display.QuantitySource)
	revision, err := s.Revision()
	require.NoError(t, err)
	require.Equal(t, revision, g.Revision)
	slices.Reverse(s.Executions)
	permuted := graphResult(t, s, a, now)
	require.Equal(t, g.Steps, permuted.Steps)
	require.Equal(t, g.History, permuted.History)
	permutedRevision, err := s.Revision()
	require.NoError(t, err)
	require.Equal(t, permutedRevision, permuted.Revision)
	slices.Reverse(s.Executions)
	*g.Steps[0].ExecutionID, *g.Steps[0].Status, *g.Steps[0].ScheduledDate = "changed", "changed", "changed"
	*g.Steps[0].Display.Quantity, *g.Steps[0].Display.QuantityUnit = 99, PlanEditorQuantityVCPU
	require.Equal(t, 3, *g.Steps[1].Display.Quantity)
	require.Equal(t, PlanEditorQuantityInstances, *g.Steps[1].Display.QuantityUnit)
	require.Equal(t, before, historyJSON(t, s))
	require.Equal(t, authBefore, historyJSON(t, a))
	s.Executions = nil
	missing := graphResult(t, s, a, now)
	nulls := historyJSON(t, missing.Steps[0])
	require.ElementsMatch(t, keys, slices.Collect(maps.Keys(nulls)))
	for _, key := range []string{"execution_id", "scheduled_date", "status", "display"} {
		require.Nil(t, nulls[key])
	}
}

func TestPlanEditorProjectionErrors(t *testing.T) {
	rows := make([]config.PurchaseExecution, config.MaxListLimit+1)
	for i := range rows {
		rows[i].ExecutionID = string(rune(i + 1))
	}
	for _, bounds := range [][2]int{{0, 0}, {0, -1}, {0, 1}, {-1, 0}, {101, 0}, {3, -1}, {3, 4}, {100, 100}, {1, 0}} {
		s, a, now := graphFixture()
		s.Plan.RampSchedule.TotalSteps, s.Plan.RampSchedule.CurrentStep = bounds[0], bounds[1]
		s.Plan.RampSchedule.Type, s.Executions = "immediate", rows[:config.MaxListLimit]
		g, err := buildPlanEditorProjection(s, a, now)
		if bounds[0] == 100 || bounds == [2]int{1, 0} {
			require.NoError(t, err)
			require.Len(t, g.Steps, bounds[0])
			require.Len(t, g.History, config.MaxListLimit)
			require.Equal(t, 100.0, g.Steps[0].CumulativeTargetPercent)
		} else {
			expected := ErrPlanEditorProjectionState
			if bounds == [2]int{0, 0} {
				expected = ErrPlanEditorUnsupportedSchedule
			}
			require.ErrorIs(t, err, expected)
			require.Nil(t, g)
		}
	}
	s, a, now := graphFixture()
	s.Executions = append(s.Executions, s.Executions[0])
	g, err := buildPlanEditorProjection(s, a, now)
	require.ErrorIs(t, err, ErrPlanEditorProjectionState)
	require.Nil(t, g)
	s.Executions = rows
	g, err = buildPlanEditorProjection(s, a, now)
	require.ErrorIs(t, err, config.ErrPlanEditorUnsupportedSize)
	require.Nil(t, g)
	s.Executions = nil
	s.Plan.RampSchedule.PercentPerStep = math.NaN()
	g, err = buildPlanEditorProjection(s, a, now)
	require.Error(t, err)
	require.Nil(t, g)
}
