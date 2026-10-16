package api

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

func TestPlanEditorFilterFieldRule(t *testing.T) {
	editable := PlanEditorFieldRule{Editable: true, RequiresProposedValueRevalidation: true}
	approved := PlanEditorFieldRule{Reason: new(PlanEditorApprovedEditsDisabled), RequiresApprovedEdits: true}
	denied := planEditorLockedField(PlanEditorPermissionDenied)
	fixed := planEditorLockedField(PlanEditorProgressOrBaseline)
	locked := planEditorLockedField(PlanEditorAffectedRootFixed)
	snapshot := func(current, total int, rows ...config.PurchaseExecution) *config.PlanEditorSnapshot {
		return &config.PlanEditorSnapshot{
			Plan:       &config.PurchasePlan{RampSchedule: config.RampSchedule{CurrentStep: current, TotalSteps: total}},
			Executions: rows,
		}
	}
	row := func(step int, status string) config.PurchaseExecution {
		return config.PurchaseExecution{StepNumber: step, Status: status}
	}
	for _, tc := range []struct {
		name      string
		snapshot  *config.PlanEditorSnapshot
		effective PlanEditorFieldRule
		want      PlanEditorFieldRule
	}{
		{"no steps", snapshot(0, 0), editable, editable},
		{"unscheduled remaining steps", snapshot(0, 3), editable, editable},
		{"pending root", snapshot(0, 3, row(1, "pending")), editable, locked},
		{"scheduled root in a later slot", snapshot(1, 3, row(1, config.StatusCompleted), row(3, "scheduled")), editable, locked},
		{"approved toggle still locks", snapshot(0, 3, row(2, "scheduled")), approved, locked},
		{"completed prefix only", snapshot(1, 3, row(1, config.StatusCompleted)), editable, editable},
		{"root beyond total", snapshot(0, 2, row(3, "pending")), editable, editable},
		{"harmless canceled root", snapshot(0, 3, row(1, config.StatusCanceled)), editable, editable},
		{"permission denied is kept", snapshot(0, 3, row(1, "pending")), denied, denied},
		{"other lock is kept", snapshot(0, 3, row(1, "pending")), fixed, fixed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, planEditorFilterFieldRule(tc.snapshot, tc.effective))
		})
	}
}
