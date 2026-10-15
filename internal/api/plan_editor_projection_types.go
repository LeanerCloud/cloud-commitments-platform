package api

import "errors"

type PlanEditorCompletionSource string

const (
	PlanEditorCompletionPlanProgress PlanEditorCompletionSource = "plan_progress"
	PlanEditorProgressOrBaseline     PlanEditorReason           = "progress_or_baseline"
	PlanEditorCompletedPrefix        PlanEditorReason           = "completed_prefix"
	PlanEditorCompletionUnavailable  PlanEditorReason           = "completion_unavailable"
	PlanEditorDateOrder              PlanEditorReason           = "date_order"
)

var ErrPlanEditorUnsupportedSchedule = errors.New("plan editor schedule is unsupported")

type PlanEditorProjection struct {
	PlanID                       string                `json:"plan_id"`
	Revision                     string                `json:"revision"`
	CurrentStep                  int                   `json:"current_step"`
	TotalSteps                   int                   `json:"total_steps"`
	StructuralShapeChangeAllowed bool                  `json:"structural_shape_change_allowed"`
	ShapeLockReason              *PlanEditorReason     `json:"shape_lock_reason"`
	Steps                        []PlanEditorSlot      `json:"steps"`
	History                      []PlanEditorExecution `json:"history"`
}

type PlanEditorSlot struct {
	StepNumber              int                        `json:"step_number"`
	LogicalCompleted        bool                       `json:"logical_completed"`
	LogicalCompletionSource PlanEditorCompletionSource `json:"logical_completion_source"`
	ExecutionID             *string                    `json:"execution_id"`
	ScheduledDate           *string                    `json:"scheduled_date"`
	Status                  *string                    `json:"status"`
	CumulativeTargetPercent float64                    `json:"cumulative_target_percent"`
	RootState               PlanEditorRootState        `json:"root_state"`
	ApprovalState           PlanEditorApprovalState    `json:"approval_state"`
	Capability              PlanEditorCapability       `json:"capability"`
	LockReason              *PlanEditorReason          `json:"lock_reason"`
	Editable                bool                       `json:"editable"`
	RequiresApprovedEdits   bool                       `json:"requires_approved_edits"`
	Display                 *PlanEditorDisplay         `json:"display"`
}
