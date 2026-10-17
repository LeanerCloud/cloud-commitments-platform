package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/planstep"
)

// planEditorRecode gives a bare 409 from a shared legacy helper (those are not
// coded) the editor code, so Create, Edit and Precheck report one code.
func planEditorRecode(err error) error {
	var client *clientError
	if errors.As(err, &client) && client.code == http.StatusConflict {
		return planEditorUnsupported(client.message)
	}
	return err
}

// validatePlanEditorScheduling applies the rules for creating or replacing
// roots (handler_plans.go validatePurchaseScheduling, without its Validate
// call and count rules). A zero-root Save is plain metadata and never blocked.
func (h *Handler) validatePlanEditorScheduling(ctx context.Context, target *config.PurchasePlan, accountIDs []string, roots []config.PlanScheduleRootChange) error {
	if len(roots) == 0 {
		return nil
	}
	if !target.Enabled {
		return planEditorUnsupported("plan must be enabled before purchases can be scheduled")
	}
	if err := planEditorRampRules(target); err != nil {
		return err
	}
	if len(accountIDs) == 0 {
		return planEditorUnsupported("plan must have target accounts before purchases can be scheduled")
	}
	if target.AutoPurchase {
		return planEditorRecode(h.requireAutomaticScheduling(ctx, target))
	}
	return nil
}

func planEditorRampRules(target *config.PurchasePlan) error {
	if err := planstep.Precheck(target); err != nil {
		if errors.Is(err, planstep.ErrUnsupportedPlan) {
			return planEditorUnsupported(err.Error())
		}
		return err
	}
	if err := target.RequireVetoWindow(); err != nil {
		return planEditorUnsupported(err.Error())
	}
	if err := planEditorImmediateShape(target); err != nil {
		return err
	}
	if ramp := target.RampSchedule; ramp.Type != "immediate" && ramp.StepIntervalDays <= 0 {
		return planEditorUnsupported("step interval must be positive for scheduled ramp purchases")
	}
	return nil
}
