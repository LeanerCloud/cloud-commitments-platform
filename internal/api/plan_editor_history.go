package api

import (
	"errors"
	"sort"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

var ErrPlanEditorProjectionState = errors.New("plan editor state is invalid")

type PlanEditorExecution struct {
	ExecutionID      string                  `json:"execution_id"`
	Status           string                  `json:"status"`
	StepNumber       int                     `json:"step_number"`
	RetryAttemptN    int                     `json:"retry_attempt_n"`
	ScheduledDate    *string                 `json:"scheduled_date"`
	RetryExecutionID *string                 `json:"retry_execution_id"`
	ApprovalState    PlanEditorApprovalState `json:"approval_state"`
	Display          PlanEditorDisplay       `json:"display"`
}

func buildPlanEditorHistory(plan *config.PurchasePlan, rows []config.PurchaseExecution, evaluatedAt time.Time) ([]PlanEditorExecution, error) {
	if len(rows) > config.MaxListLimit {
		return nil, config.ErrPlanEditorUnsupportedSize
	}
	seen := make(map[string]bool, len(rows))
	for i := range rows {
		if seen[rows[i].ExecutionID] {
			return nil, ErrPlanEditorProjectionState
		}
		seen[rows[i].ExecutionID] = true
	}
	out := make([]PlanEditorExecution, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		h := PlanEditorExecution{ExecutionID: row.ExecutionID, Status: row.Status, StepNumber: row.StepNumber,
			RetryAttemptN: row.RetryAttemptN, ApprovalState: planEditorApprovalState(row, evaluatedAt), Display: buildPlanEditorDisplay(plan, row)}
		if !row.ScheduledDate.IsZero() {
			h.ScheduledDate = new(row.ScheduledDate.UTC().Format(time.DateOnly))
		}
		if row.RetryExecutionID != nil {
			h.RetryExecutionID = new(*row.RetryExecutionID)
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StepNumber != out[j].StepNumber {
			return out[i].StepNumber < out[j].StepNumber
		}
		return out[i].ExecutionID < out[j].ExecutionID
	})
	return out, nil
}
