package api

import (
	"context"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

func (h *Handler) planEditorIntentPolicy(ctx context.Context, req *events.LambdaFunctionURLRequest, session *Session, before *config.PlanEditorSnapshot, intent *planEditorIntent, now time.Time) (*planEditorFieldPolicyResult, error) {
	if before == nil {
		return nil, nil
	}
	policy, err := h.buildPlanEditorFieldPolicy(ctx, req, session, before, now)
	if err != nil {
		return nil, err
	}
	if err := planEditorRequireIntent(policy, before, intent); err != nil {
		return nil, err
	}
	return policy, nil
}

// planEditorRequireIntent refuses an intent that supplies a changed field the
// policy does not allow. Without update-plans every changed field is 403,
// checked before any lock so a read-only caller learns nothing about the plan.
func planEditorRequireIntent(policy *planEditorFieldPolicyResult, before *config.PlanEditorSnapshot, intent *planEditorIntent) error {
	var rules []PlanEditorFieldRule
	for _, field := range planEditorChangedFields(planEditorControlChanges(before, intent)) {
		rules = append(rules, policy.Fields.Controls[field])
	}
	for _, key := range slices.Sorted(maps.Keys(intent.Services)) {
		for _, field := range planEditorChangedFields(planEditorServiceChanges(before.Plan.Services[key], intent.Services[key])) {
			rules = append(rules, policy.Fields.Services[key][field])
		}
	}
	if len(rules) > 0 && !policy.CanUpdatePlan {
		return planEditorRefusal(http.StatusForbidden, codePermissionDenied, "plan field permission denied")
	}
	for _, rule := range rules {
		if err := planEditorRequireField(rule, intent.AllowApprovedEdits); err != nil {
			return err
		}
	}
	return nil
}

func planEditorChangedFields(changes map[string]bool) []string {
	var out []string
	for field, changed := range changes {
		if changed {
			out = append(out, field)
		}
	}
	slices.Sort(out)
	return out
}

func planEditorRequireField(rule PlanEditorFieldRule, approved bool) error {
	if rule.Editable || (approved && rule.RequiresApprovedEdits) {
		return nil
	}
	return planEditorRefusal(http.StatusConflict, codeEditorFieldLocked, "plan field is fixed; refresh the editor")
}

func planEditorControlChanges(before *config.PlanEditorSnapshot, intent *planEditorIntent) map[string]bool {
	plan, patch, ramp := before.Plan, intent.Plan, intent.Ramp
	changes := map[string]bool{
		"name":                     planEditorValueChanged(patch.Name, plan.Name),
		"enabled":                  planEditorValueChanged(patch.Enabled, plan.Enabled),
		"auto_purchase":            planEditorValueChanged(patch.AutoPurchase, plan.AutoPurchase),
		"notification_days_before": planEditorValueChanged(patch.NotificationDaysBefore, plan.NotificationDaysBefore),
		"max_step_commitment":      patch.CapSupplied && !reflect.DeepEqual(patch.MaxStepCommitment, plan.MaxStepCommitment),
		"ramp_type":                planEditorValueChanged(ramp.Type, plan.RampSchedule.Type),
		"percent_per_step":         planEditorValueChanged(ramp.PercentPerStep, plan.RampSchedule.PercentPerStep) || config.RederivesCumulativeSteps(plan.RampSchedule, intent.PlanScheduleIntent),
		"step_interval_days":       planEditorValueChanged(ramp.StepIntervalDays, plan.RampSchedule.StepIntervalDays),
		"start_date":               ramp.StartDate != nil && !ramp.StartDate.Equal(plan.RampSchedule.StartDate),
		"step_count":               intent.StepCount != nil,
	}
	if intent.AccountIDs != nil {
		changes["target_accounts"] = !slices.Equal(*intent.AccountIDs, before.AccountIDs)
	}
	return changes
}

func planEditorServiceChanges(old config.ServiceConfig, patch config.PlanScheduleServicePatch) map[string]bool {
	return map[string]bool{
		"provider":        planEditorValueChanged(patch.Provider, old.Provider),
		"service":         planEditorValueChanged(patch.Service, old.Service),
		"enabled":         planEditorValueChanged(patch.Enabled, old.Enabled),
		"term":            planEditorValueChanged(patch.Term, old.Term),
		"payment":         planEditorValueChanged(patch.Payment, old.Payment),
		"coverage":        planEditorValueChanged(patch.Coverage, old.Coverage),
		"ramp_schedule":   planEditorValueChanged(patch.RampSchedule, old.RampSchedule),
		"include_engines": planEditorListChanged(patch.IncludeEngines, old.IncludeEngines),
		"exclude_engines": planEditorListChanged(patch.ExcludeEngines, old.ExcludeEngines),
		"include_regions": planEditorListChanged(patch.IncludeRegions, old.IncludeRegions),
		"exclude_regions": planEditorListChanged(patch.ExcludeRegions, old.ExcludeRegions),
		"include_types":   planEditorListChanged(patch.IncludeTypes, old.IncludeTypes),
		"exclude_types":   planEditorListChanged(patch.ExcludeTypes, old.ExcludeTypes),
		"min_count":       planEditorValueChanged(patch.MinCount, old.MinCount),
	}
}

func planEditorValueChanged[T comparable](value *T, stored T) bool {
	return value != nil && *value != stored
}

func planEditorListChanged(value *[]string, stored []string) bool {
	return value != nil && !reflect.DeepEqual(*value, stored)
}
