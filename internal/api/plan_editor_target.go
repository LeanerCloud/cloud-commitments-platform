package api

import (
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

func planEditorTarget(before *config.PlanEditorSnapshot, planID string, intent *planEditorIntent, now time.Time) (*config.PurchasePlan, error) {
	target := &config.PurchasePlan{ID: planID, CreatedAt: now, UpdatedAt: now, Services: make(map[string]config.ServiceConfig)}
	if before != nil {
		*target = *before.Plan
		target.Services = maps.Clone(before.Plan.Services)
		if before.Plan.MaxStepCommitment != nil {
			target.MaxStepCommitment = new(*before.Plan.MaxStepCommitment)
		}
	}
	storedCap := target.MaxStepCommitment
	if err := applyPlanEditorPatch(target, intent.Plan); err != nil {
		return nil, err
	}
	if !intent.Plan.CapSupplied {
		target.MaxStepCommitment = storedCap
	}
	if err := applyPlanEditorServices(target, intent.Services, before == nil); err != nil {
		return nil, err
	}
	if err := applyPlanEditorShape(target, intent, before); err != nil {
		return nil, err
	}
	return target, nil
}

func applyPlanEditorShape(target *config.PurchasePlan, intent *planEditorIntent, before *config.PlanEditorSnapshot) error {
	ramp := &target.RampSchedule
	if before == nil && intent.StepCount == nil && intent.Ramp.PercentPerStep == nil {
		return planEditorInvalid("a new plan needs step_count or percent_per_step")
	}
	started := before != nil && (before.Plan.RampSchedule.CurrentStep > 0 || before.Baseline != nil)
	if intent.Ramp.PercentPerStep != nil && intent.StepCount != nil {
		return planEditorInvalid("percent_per_step conflicts with step_count")
	}
	captured := *ramp
	if err := applyPlanEditorPatch(ramp, intent.Ramp); err != nil {
		return err
	}
	if intent.StepCount != nil {
		return applyPlanEditorStepCount(ramp, *intent.StepCount)
	}
	return applyPlanEditorCumulative(ramp, captured, intent, started)
}

func applyPlanEditorStepCount(ramp *config.RampSchedule, count int) error {
	if count < max(1, ramp.CurrentStep) || count > config.MaxTotalSteps {
		return planEditorInvalid("step_count is outside the allowed range")
	}
	ramp.TotalSteps = count
	ramp.PercentPerStep = 100 / float64(count)
	ramp.AllocationMode = config.RampAllocationEqualCount
	return nil
}

// applyPlanEditorCumulative mirrors the store's cumulative merge: steps are derived only when the percent changed or an equal_count plan converts.
func applyPlanEditorCumulative(ramp *config.RampSchedule, captured config.RampSchedule, intent *planEditorIntent, started bool) error {
	percent := intent.Ramp.PercentPerStep
	if percent == nil {
		return nil
	}
	derived := config.CumulativeStepCount(*percent)
	if derived == 0 || !config.RederivesCumulativeSteps(captured, intent.PlanScheduleIntent) {
		return nil
	}
	if started {
		return planEditorInvalid("percent_per_step cannot reshape a plan with progress or a baseline")
	}
	if derived > config.MaxTotalSteps {
		return planEditorInvalid("percent_per_step needs more steps than allowed")
	}
	ramp.TotalSteps, ramp.AllocationMode = derived, ""
	return nil
}

func applyPlanEditorServices(target *config.PurchasePlan, patches map[string]config.PlanScheduleServicePatch, create bool) error {
	original := maps.Clone(target.Services)
	for _, source := range slices.Sorted(maps.Keys(patches)) {
		service, exists := original[source]
		if !exists && !create {
			return planEditorInvalid("unknown service patch")
		}
		key, service, err := planEditorPatchedService(source, service, patches[source])
		if err != nil {
			return err
		}
		_, oldCollision := original[key]
		_, newCollision := target.Services[key]
		if (key != source || !exists) && (oldCollision || newCollision) {
			return planEditorInvalid("service identity collision")
		}
		if target.Services == nil {
			target.Services = make(map[string]config.ServiceConfig)
		}
		delete(target.Services, source)
		target.Services[key] = service
	}
	return nil
}

func planEditorPatchedService(source string, service config.ServiceConfig, patch config.PlanScheduleServicePatch) (string, config.ServiceConfig, error) {
	service.IncludeEngines, service.ExcludeEngines = slices.Clone(service.IncludeEngines), slices.Clone(service.ExcludeEngines)
	service.IncludeRegions, service.ExcludeRegions = slices.Clone(service.IncludeRegions), slices.Clone(service.ExcludeRegions)
	service.IncludeTypes, service.ExcludeTypes = slices.Clone(service.IncludeTypes), slices.Clone(service.ExcludeTypes)
	previousProvider, previousService := service.Provider, service.Service
	if err := applyPlanEditorPatch(&service, patch); err != nil {
		return "", service, err
	}
	key := source
	if service.Provider != previousProvider || service.Service != previousService {
		key = service.Provider + "/" + service.Service
	}
	return key, service, nil
}

func applyPlanEditorPatch(target, patch any) error {
	raw, err := json.Marshal(patch)
	if err != nil {
		return planEditorInvalid("invalid plan editor changes")
	}
	if err = json.Unmarshal(raw, target); err != nil {
		return planEditorInvalid("invalid plan editor changes")
	}
	return nil
}
