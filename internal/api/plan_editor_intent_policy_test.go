package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

func editorPolicyBefore() *config.PlanEditorSnapshot {
	limit := 5.0
	return &config.PlanEditorSnapshot{
		AccountIDs: []string{"a", "b"},
		Plan: &config.PurchasePlan{Name: "p", Enabled: true, AutoPurchase: true, NotificationDaysBefore: 3, MaxStepCommitment: &limit,
			RampSchedule: config.RampSchedule{Type: "weekly", PercentPerStep: 25, StepIntervalDays: 7, StartDate: time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)},
			Services: map[string]config.ServiceConfig{"k": {Provider: "aws", Service: "ec2", Enabled: true, Term: 1, Payment: "no-upfront", Coverage: 50,
				RampSchedule: "weekly", IncludeEngines: []string{"x"}, IncludeRegions: []string{"r"}, ExcludeRegions: []string{"r"}, MinCount: 2}},
		},
	}
}

// editorPolicyControl returns an intent that supplies one control at a value
// different from (differ) or equal to (same) the one editorPolicyBefore stores.
func editorPolicyControl(before *config.PlanEditorSnapshot, field string, differ bool) *planEditorIntent {
	plan := before.Plan
	intent := &planEditorIntent{}
	p, r := &intent.Plan, &intent.Ramp
	pick := func(same, other any) any {
		if differ {
			return other
		}
		return same
	}
	switch field {
	case "name":
		p.Name = new(pick(plan.Name, "other").(string))
	case "enabled":
		p.Enabled = new(pick(plan.Enabled, !plan.Enabled).(bool))
	case "auto_purchase":
		p.AutoPurchase = new(pick(plan.AutoPurchase, !plan.AutoPurchase).(bool))
	case "notification_days_before":
		p.NotificationDaysBefore = new(pick(plan.NotificationDaysBefore, 9).(int))
	case "max_step_commitment":
		p.CapSupplied = true
		if plan.MaxStepCommitment != nil {
			p.MaxStepCommitment = new(pick(*plan.MaxStepCommitment, 9.0).(float64))
		} else if !differ {
			p.MaxStepCommitment = nil
		} else {
			p.MaxStepCommitment = new(9.0)
		}
	case "ramp_type":
		r.Type = new(pick(plan.RampSchedule.Type, "monthly").(string))
	case "percent_per_step":
		r.PercentPerStep = new(pick(plan.RampSchedule.PercentPerStep, 10.0).(float64))
	case "step_interval_days":
		r.StepIntervalDays = new(pick(plan.RampSchedule.StepIntervalDays, 1).(int))
	case "start_date":
		r.StartDate = new(pick(plan.RampSchedule.StartDate, plan.RampSchedule.StartDate.AddDate(0, 0, 1)).(time.Time))
	case "step_count":
		intent.StepCount = new(4)
	case "target_accounts":
		intent.AccountIDs = new(pick(before.AccountIDs, append([]string{"other"}, before.AccountIDs...)).([]string))
	}
	return intent
}

var editorPolicyControlFields = []string{"name", "enabled", "auto_purchase", "notification_days_before", "max_step_commitment",
	"ramp_type", "percent_per_step", "step_interval_days", "start_date", "step_count", "target_accounts"}

func TestPlanEditorControlChanges(t *testing.T) {
	before := editorPolicyBefore()
	for _, field := range editorPolicyControlFields {
		t.Run(field, func(t *testing.T) {
			changed := planEditorControlChanges(before, editorPolicyControl(before, field, true))
			for name, value := range changed {
				require.Equal(t, name == field, value, "differing %s flagged %s", field, name)
			}
			require.True(t, changed[field])
			if field == "step_count" {
				return // supplying step_count is a shape change even at the stored count
			}
			for name, value := range planEditorControlChanges(before, editorPolicyControl(before, field, false)) {
				require.False(t, value, "equal %s flagged %s", field, name)
			}
		})
	}
	for name, value := range planEditorControlChanges(before, &planEditorIntent{}) {
		require.False(t, value, "nothing supplied flagged %s", name)
	}
	cleared := &planEditorIntent{}
	cleared.Plan.CapSupplied = true
	require.True(t, planEditorControlChanges(before, cleared)["max_step_commitment"], "null clears a stored cap")
	require.Len(t, planEditorControlChanges(before, &planEditorIntent{}), 10)
}

var editorPolicyServiceFields = []string{"provider", "service", "enabled", "term", "payment", "coverage", "ramp_schedule",
	"include_engines", "exclude_engines", "include_regions", "exclude_regions", "include_types", "exclude_types", "min_count"}

func editorPolicyServicePatch(old config.ServiceConfig, field string, differ bool) config.PlanScheduleServicePatch {
	list := func(stored []string, other []string) *[]string {
		if differ {
			return &other
		}
		return &stored
	}
	var patch config.PlanScheduleServicePatch
	switch field {
	case "provider":
		patch.Provider = new(map[bool]string{true: "gcp", false: old.Provider}[differ])
	case "service":
		patch.Service = new(map[bool]string{true: "rds", false: old.Service}[differ])
	case "enabled":
		patch.Enabled = new(old.Enabled != differ)
	case "term":
		patch.Term = new(map[bool]int{true: 3, false: old.Term}[differ])
	case "payment":
		patch.Payment = new(map[bool]string{true: "all-upfront", false: old.Payment}[differ])
	case "coverage":
		patch.Coverage = new(map[bool]float64{true: 80, false: old.Coverage}[differ])
	case "ramp_schedule":
		patch.RampSchedule = new(map[bool]string{true: "immediate", false: old.RampSchedule}[differ])
	case "include_engines":
		patch.IncludeEngines = list(old.IncludeEngines, []string{"y"})
	case "exclude_engines":
		patch.ExcludeEngines = list(old.ExcludeEngines, []string{"y"})
	case "include_regions":
		patch.IncludeRegions = list(old.IncludeRegions, []string{"y"})
	case "exclude_regions":
		patch.ExcludeRegions = list(old.ExcludeRegions, []string{"y"})
	case "include_types":
		patch.IncludeTypes = list(old.IncludeTypes, []string{"y"})
	case "exclude_types":
		patch.ExcludeTypes = list(old.ExcludeTypes, []string{"y"})
	case "min_count":
		patch.MinCount = new(map[bool]int{true: 7, false: old.MinCount}[differ])
	}
	return patch
}

func TestPlanEditorServiceChanges(t *testing.T) {
	old := editorPolicyBefore().Plan.Services["k"]
	for _, field := range editorPolicyServiceFields {
		t.Run(field, func(t *testing.T) {
			changed := planEditorServiceChanges(old, editorPolicyServicePatch(old, field, true))
			for name, value := range changed {
				require.Equal(t, name == field, value, "differing %s flagged %s", field, name)
			}
			for name, value := range planEditorServiceChanges(old, editorPolicyServicePatch(old, field, false)) {
				require.False(t, value, "equal %s flagged %s", field, name)
			}
		})
	}
	require.Len(t, planEditorServiceChanges(old, config.PlanScheduleServicePatch{}), len(editorPolicyServiceFields))
	for name, value := range planEditorServiceChanges(old, config.PlanScheduleServicePatch{}) {
		require.False(t, value, name)
	}
}

func TestPlanEditorListChangedDistinguishesNullFromEmpty(t *testing.T) {
	empty, null := []string{}, []string(nil)
	require.True(t, planEditorListChanged(&empty, null), "[] over a stored null")
	require.True(t, planEditorListChanged(&null, empty), "null over a stored []")
	require.False(t, planEditorListChanged(&null, null))
	require.False(t, planEditorListChanged(nil, []string{"x"}))
}

func TestPlanEditorIntentPolicyWithoutSnapshot(t *testing.T) {
	policy, err := (*Handler)(nil).planEditorIntentPolicy(t.Context(), nil, nil, nil, &planEditorIntent{}, time.Now())
	require.NoError(t, err)
	require.Nil(t, policy)
}

func TestPlanEditorChangedFieldsAreSorted(t *testing.T) {
	changes := map[string]bool{"z": true, "a": true, "m": true, "skip": false}
	for range 20 {
		require.Equal(t, []string{"a", "m", "z"}, planEditorChangedFields(changes))
	}
}

// editorPolicyFor builds a synthetic policy: every control and every service
// field of "k" carries the given rule, with overrides applied on top.
func editorPolicyFor(canUpdate bool, base PlanEditorFieldRule, serviceOverride map[string]PlanEditorFieldRule) *planEditorFieldPolicyResult {
	policy := &planEditorFieldPolicyResult{CanUpdatePlan: canUpdate}
	policy.Fields.Controls = map[string]PlanEditorFieldRule{}
	for _, field := range editorPolicyControlFields {
		policy.Fields.Controls[field] = base
	}
	service := map[string]PlanEditorFieldRule{}
	for _, field := range editorPolicyServiceFields {
		service[field] = base
	}
	for field, rule := range serviceOverride {
		service[field] = rule
	}
	policy.Fields.Services = map[string]map[string]PlanEditorFieldRule{"k": service}
	return policy
}

func requireEditorPolicyRefusal(t *testing.T, err error, status int, code, msg string) {
	t.Helper()
	var client *clientError
	require.ErrorAs(t, err, &client, msg)
	require.Equal(t, status, client.code, msg)
	require.Equal(t, code, client.details["code"], msg)
}

func editorPolicyServiceIntent(before *config.PlanEditorSnapshot, field string, approved bool) *planEditorIntent {
	patch := editorPolicyServicePatch(before.Plan.Services["k"], field, true)
	return &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Services: map[string]config.PlanScheduleServicePatch{"k": patch}, AllowApprovedEdits: approved}}
}

// A read-only caller gets 403 for every field even when the rules carry a lock
// reason (progress, shape), which must not leak through.
func TestPlanEditorRequireIntentReadOnlyIsAlways403(t *testing.T) {
	before := editorPolicyBefore()
	for _, reason := range []PlanEditorReason{PlanEditorPermissionDenied, PlanEditorProgressOrBaseline, PlanEditorUnsupportedSchedule} {
		policy := editorPolicyFor(false, PlanEditorFieldRule{Reason: &reason}, nil)
		for _, field := range editorPolicyControlFields {
			err := planEditorRequireIntent(policy, before, editorPolicyControl(before, field, true))
			requireEditorPolicyRefusal(t, err, http.StatusForbidden, codePermissionDenied, string(reason)+" control "+field)
		}
		for _, field := range editorPolicyServiceFields {
			err := planEditorRequireIntent(policy, before, editorPolicyServiceIntent(before, field, true))
			requireEditorPolicyRefusal(t, err, http.StatusForbidden, codePermissionDenied, string(reason)+" service "+field)
		}
	}
}

func TestPlanEditorRequireIntentWriter(t *testing.T) {
	before := editorPolicyBefore()
	locked := PlanEditorFieldRule{Reason: new(PlanEditorAffectedRootFixed)}
	approved := PlanEditorFieldRule{RequiresApprovedEdits: true, Reason: new(PlanEditorApprovedEditsDisabled)}
	policy := editorPolicyFor(true, PlanEditorFieldRule{Editable: true}, map[string]PlanEditorFieldRule{
		"include_regions": locked, "min_count": locked, "coverage": approved})
	require.NoError(t, planEditorRequireIntent(policy, before, editorPolicyServiceIntent(before, "term", false)))
	require.NoError(t, planEditorRequireIntent(policy, before, editorPolicyControl(before, "name", true)))
	for _, field := range []string{"include_regions", "min_count"} {
		for _, consent := range []bool{false, true} {
			err := planEditorRequireIntent(policy, before, editorPolicyServiceIntent(before, field, consent))
			requireEditorPolicyRefusal(t, err, http.StatusConflict, codeEditorFieldLocked, field)
		}
	}
	err := planEditorRequireIntent(policy, before, editorPolicyServiceIntent(before, "coverage", false))
	requireEditorPolicyRefusal(t, err, http.StatusConflict, codeEditorFieldLocked, "coverage without consent")
	require.NoError(t, planEditorRequireIntent(policy, before, editorPolicyServiceIntent(before, "coverage", true)))
	unchanged := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Services: map[string]config.PlanScheduleServicePatch{"k": editorPolicyServicePatch(before.Plan.Services["k"], "min_count", false)}}}
	require.NoError(t, planEditorRequireIntent(policy, before, unchanged), "an equal value on a locked field is not a change")
	typo := editorPolicyServiceIntent(before, "term", true)
	typo.Services["typo"] = typo.Services["k"]
	delete(typo.Services, "k")
	requireEditorPolicyRefusal(t, planEditorRequireIntent(policy, before, typo), http.StatusConflict, codeEditorFieldLocked, "service key with no policy entry")
}

func TestPlanEditorRequireIntentPercentOnEqualCountPlan(t *testing.T) {
	locked := &planEditorFieldPolicyResult{CanUpdatePlan: true, Fields: PlanEditorFieldPolicy{Controls: map[string]PlanEditorFieldRule{}}}
	for _, field := range []string{"percent_per_step", "step_count", "ramp_type"} {
		locked.Fields.Controls[field] = PlanEditorFieldRule{}
	}
	same := func(before *config.PlanEditorSnapshot) *planEditorIntent {
		return &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Ramp: config.PlanScheduleRampPatch{PercentPerStep: new(before.Plan.RampSchedule.PercentPerStep)}}}
	}
	equal := editorPolicyBefore()
	equal.Plan.RampSchedule.AllocationMode = config.RampAllocationEqualCount
	require.True(t, planEditorControlChanges(equal, same(equal))["percent_per_step"], "the same percent converts an equal_count plan")
	requireEditorPolicyRefusal(t, planEditorRequireIntent(locked, equal, same(equal)), http.StatusConflict, codeEditorFieldLocked, "equal_count")

	cumulative := editorPolicyBefore()
	require.False(t, planEditorControlChanges(cumulative, same(cumulative))["percent_per_step"])
	require.NoError(t, planEditorRequireIntent(locked, cumulative, same(cumulative)), "an unchanged percent on a cumulative plan passes a locked policy")

	drift := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Ramp: config.PlanScheduleRampPatch{PercentPerStep: new(cumulative.Plan.RampSchedule.PercentPerStep + 1e-12)}}}
	require.True(t, planEditorControlChanges(cumulative, drift)["percent_per_step"], "a sub-epsilon drift is still a written change")
	requireEditorPolicyRefusal(t, planEditorRequireIntent(locked, cumulative, drift), http.StatusConflict, codeEditorFieldLocked, "drift")
}
