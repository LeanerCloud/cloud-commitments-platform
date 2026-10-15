package api

import (
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

func buildPlanEditorProjection(s *config.PlanEditorSnapshot, authority PlanEditorAuthority, evaluatedAt time.Time) (*PlanEditorProjection, error) {
	if err := planEditorValidateGraph(&s.Plan.RampSchedule); err != nil {
		return nil, err
	}
	history, err := buildPlanEditorHistory(s.Plan, s.Executions, evaluatedAt)
	if err != nil {
		return nil, err
	}
	revision, err := s.Revision()
	if err != nil {
		return nil, err
	}
	ramp := &s.Plan.RampSchedule
	g := &PlanEditorProjection{PlanID: s.Plan.ID, Revision: revision, CurrentStep: ramp.CurrentStep, TotalSteps: ramp.TotalSteps,
		StructuralShapeChangeAllowed: true, Steps: make([]PlanEditorSlot, 0, ramp.TotalSteps), History: history}
	for ordinal := 1; ordinal <= ramp.TotalSteps; ordinal++ {
		p := buildPlanEditorStepPolicy(s.Executions, ordinal, authority, evaluatedAt)
		g.Steps = append(g.Steps, planEditorGraphSlot(s.Plan, ordinal, p))
		if !p.StructuralChangeAllowed && ordinal > ramp.CurrentStep && g.ShapeLockReason == nil {
			g.StructuralShapeChangeAllowed, g.ShapeLockReason = false, p.Reason
		}
	}
	if ramp.CurrentStep > 0 || s.Baseline != nil {
		g.StructuralShapeChangeAllowed, g.ShapeLockReason = false, new(PlanEditorProgressOrBaseline)
	}
	planEditorGraphDateOrder(g)
	return g, nil
}

func planEditorValidateGraph(r *config.RampSchedule) error {
	if r.TotalSteps < 0 || r.TotalSteps > config.MaxTotalSteps || r.CurrentStep < 0 || r.CurrentStep > r.TotalSteps {
		return ErrPlanEditorProjectionState
	}
	if r.TotalSteps == 0 {
		return ErrPlanEditorUnsupportedSchedule
	}
	return nil
}

func planEditorGraphSlot(plan *config.PurchasePlan, ordinal int, p planEditorStepPolicy) PlanEditorSlot {
	slot := PlanEditorSlot{StepNumber: ordinal, LogicalCompletionSource: PlanEditorCompletionPlanProgress, CumulativeTargetPercent: plan.RampSchedule.CumulativePercent(ordinal),
		RootState: p.RootState, ApprovalState: p.ApprovalState, Capability: p.Capability, LockReason: p.Reason,
		Editable: p.Editable, RequiresApprovedEdits: p.RequiresApprovedEdits}
	row := p.Root
	if ordinal <= plan.RampSchedule.CurrentStep {
		slot.LogicalCompleted, slot.LogicalCompletionSource = true, PlanEditorCompletionPlanProgress
		slot.Capability, slot.Editable, slot.RequiresApprovedEdits = PlanEditorFixed, false, false
		slot.LockReason = new(PlanEditorCompletionUnavailable)
		if row == nil || (row.Status != config.StatusCompleted && row.Status != config.StatusPartiallyCompleted) ||
			row.CompletedAt == nil || row.CompletedAt.IsZero() {
			return slot
		}
		slot.LockReason = new(PlanEditorCompletedPrefix)
		if row.ScheduledDate.IsZero() {
			slot.LockReason = new(PlanEditorInvalidDate)
		}
	}
	if row != nil {
		slot.ExecutionID, slot.Status = new(row.ExecutionID), new(row.Status)
		if !row.ScheduledDate.IsZero() {
			slot.ScheduledDate = new(row.ScheduledDate.UTC().Format(time.DateOnly))
		}
		display := buildPlanEditorDisplay(plan, row)
		slot.Display = &display
	}
	return slot
}

func planEditorGraphDateOrder(g *PlanEditorProjection) {
	previous := -1
	for i := range g.Steps {
		if g.Steps[i].ScheduledDate == nil {
			continue
		}
		if previous >= 0 && *g.Steps[previous].ScheduledDate >= *g.Steps[i].ScheduledDate {
			g.StructuralShapeChangeAllowed, g.ShapeLockReason = false, new(PlanEditorDateOrder)
			for j := previous; j <= i; j++ {
				slot := &g.Steps[j]
				slot.Capability, slot.Editable, slot.RequiresApprovedEdits = PlanEditorFixed, false, false
				if slot.LockReason == nil || *slot.LockReason != PlanEditorInvalidDate {
					slot.LockReason = new(PlanEditorDateOrder)
				}
			}
		}
		previous = i
	}
}
