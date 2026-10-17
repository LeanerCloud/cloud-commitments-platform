package api

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

const codeEditorPlanUnsupported = "editor_plan_unsupported"

func planEditorUnsupported(msg string) error {
	return planEditorRefusal(http.StatusConflict, codeEditorPlanUnsupported, msg)
}

// validatePlanEditorTarget checks the proposed plan. Create validates all of
// it. Edit validates only what the intent changed, so an untouched invalid
// legacy value (a disabled service) never blocks an unrelated Edit, which is
// why PurchasePlan.Validate is not called on Edit.
func validatePlanEditorTarget(before *config.PlanEditorSnapshot, target *config.PurchasePlan, intent *planEditorIntent) error {
	if before == nil {
		return planEditorValidation(target.Validate())
	}
	controls := planEditorControlChanges(before, intent)
	if err := planEditorValidation(planEditorProbe(target, controls).Validate()); err != nil {
		return err
	}
	if controls["max_step_commitment"] && intent.Plan.MaxStepCommitment == nil && target.AutoPurchase {
		return planEditorInvalid("max_step_commitment cannot be cleared while auto_purchase is enabled")
	}
	if err := planEditorValidateShape(target, controls); err != nil {
		return err
	}
	servicesChanged, err := validatePlanEditorServices(before.Plan, intent)
	if err != nil {
		return err
	}
	if servicesChanged || planEditorAnyChanged(controls, "enabled", "auto_purchase", "step_count") {
		return planEditorValidation(target.CheckNoMultiStepSavingsPlans())
	}
	return nil
}

// planEditorProbe holds the changed controls' proposed values and valid
// placeholders, so PurchasePlan.Validate (config/validation.go:575) judges only
// what changed; disabled with no services, it skips the service rules.
func planEditorProbe(target *config.PurchasePlan, controls map[string]bool) *config.PurchasePlan {
	probe := &config.PurchasePlan{Name: "x"}
	if controls["name"] {
		probe.Name = target.Name
	}
	if controls["notification_days_before"] {
		probe.NotificationDaysBefore = target.NotificationDaysBefore
	}
	if controls["max_step_commitment"] {
		probe.MaxStepCommitment = target.MaxStepCommitment
	}
	if planEditorAnyChanged(controls, "ramp_type", "percent_per_step", "step_interval_days", "step_count") {
		probe.RampSchedule = target.RampSchedule
	}
	return probe
}

// planEditorImmediateShape is the legacy validateScheduledRamp rule
// (handler_plans.go:573): an immediate ramp has exactly one step.
func planEditorImmediateShape(target *config.PurchasePlan) error {
	if ramp := target.RampSchedule; ramp.Type == "immediate" && ramp.TotalSteps != 1 {
		return planEditorUnsupported("count exceeds the plan's remaining ramp steps")
	}
	return nil
}

func planEditorAnyChanged(changes map[string]bool, fields ...string) bool {
	return slices.ContainsFunc(fields, func(field string) bool { return changes[field] })
}

func planEditorValidateShape(target *config.PurchasePlan, controls map[string]bool) error {
	if controls["enabled"] && target.Enabled && len(target.Services) == 0 {
		return planEditorInvalid("validation error: plan must have at least one service")
	}
	if controls["ramp_type"] && target.RampSchedule.TotalSteps > 0 {
		return planEditorImmediateShape(target)
	}
	return nil
}

func validatePlanEditorServices(stored *config.PurchasePlan, intent *planEditorIntent) (bool, error) {
	changedAny := false
	for _, key := range slices.Sorted(maps.Keys(intent.Services)) {
		patch := intent.Services[key]
		if !slices.Contains(slices.Collect(maps.Values(planEditorServiceChanges(stored.Services[key], patch))), true) {
			continue
		}
		changedAny = true
		_, service, err := planEditorPatchedService(key, stored.Services[key], patch)
		if err != nil {
			return false, err
		}
		if err := service.Validate(); err != nil {
			return false, planEditorValidation(fmt.Errorf("invalid service config '%s': %w", key, err))
		}
	}
	return changedAny, nil
}

// planEditorValidation maps a config validation error to the editor's codes:
// the armed multi-step Savings Plans refusal is a 409 (as Precheck reports it),
// everything else a 400 with the legacy message prefix.
func planEditorValidation(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, config.ErrMultiStepSavingsPlans):
		return planEditorUnsupported(err.Error())
	default:
		return planEditorInvalid(fmt.Sprintf("validation error: %s", err))
	}
}
