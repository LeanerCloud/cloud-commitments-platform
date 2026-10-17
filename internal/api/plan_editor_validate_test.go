package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

var editorValidateNow = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func editorValidateRun(t *testing.T, before *config.PlanEditorSnapshot, intent *planEditorIntent) error {
	t.Helper()
	planID := ""
	if before != nil {
		planID = "p"
	}
	target, err := planEditorTarget(before, planID, intent, editorValidateNow)
	require.NoError(t, err)
	return validatePlanEditorTarget(before, target, intent)
}

func editorValidatePatch(service string, fn func(*config.PlanScheduleServicePatch)) *planEditorIntent {
	var patch config.PlanScheduleServicePatch
	fn(&patch)
	return &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Services: map[string]config.PlanScheduleServicePatch{service: patch}}}
}

func TestValidatePlanEditorTarget_EachChangedFieldHasARule(t *testing.T) {
	cases := []struct {
		name   string
		intent *planEditorIntent
	}{
		{"name empty", &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{Name: new("")}}}},
		{"name too long", &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{Name: new(strings.Repeat("n", config.MaxPlanNameLength+1))}}}},
		{"notice high", &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{NotificationDaysBefore: new(config.MaxNotificationDaysBefore + 1)}}}},
		{"notice negative", &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{NotificationDaysBefore: new(-1)}}}},
		{"cap below minimum", &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{CapSupplied: true, MaxStepCommitment: new(0.001)}}}},
		{"cap sub-cent", &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{CapSupplied: true, MaxStepCommitment: new(1.005)}}}},
		{"cap cleared while armed", &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{CapSupplied: true}}}},
		{"ramp type", &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Ramp: config.PlanScheduleRampPatch{Type: new("hourly")}}}},
		{"ramp percent zero", &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Ramp: config.PlanScheduleRampPatch{PercentPerStep: new(0.0)}}}},
		{"ramp percent over 100", &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Ramp: config.PlanScheduleRampPatch{PercentPerStep: new(101.0)}}}},
		{"ramp interval high", &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Ramp: config.PlanScheduleRampPatch{StepIntervalDays: new(config.MaxStepIntervalDays + 1)}}}},
		{"ramp interval negative", &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Ramp: config.PlanScheduleRampPatch{StepIntervalDays: new(-1)}}}},
		{"service provider", editorValidatePatch("k", func(p *config.PlanScheduleServicePatch) { p.Provider = new("nope") })},
		{"service empty", editorValidatePatch("k", func(p *config.PlanScheduleServicePatch) { p.Service = new("") })},
		{"service term", editorValidatePatch("k", func(p *config.PlanScheduleServicePatch) { p.Term = new(2) })},
		{"service payment", editorValidatePatch("k", func(p *config.PlanScheduleServicePatch) { p.Payment = new("bogus") })},
		{"service coverage", editorValidatePatch("k", func(p *config.PlanScheduleServicePatch) { p.Coverage = new(101.0) })},
		{"service min_count", editorValidatePatch("k", func(p *config.PlanScheduleServicePatch) { p.MinCount = new(-1) })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := editorPolicyBefore()
			requireEditorPolicyRefusal(t, editorValidateRun(t, before, tc.intent), http.StatusBadRequest, codeInvalidRequest, tc.name)
		})
	}
	t.Run("valid changes pass", func(t *testing.T) {
		intent := editorValidatePatch("k", func(p *config.PlanScheduleServicePatch) { p.Term, p.Coverage = new(3), new(80.0) })
		intent.Plan = config.PlanSchedulePlanPatch{Name: new("renamed"), NotificationDaysBefore: new(5), CapSupplied: true, MaxStepCommitment: new(10.25)}
		intent.Ramp = config.PlanScheduleRampPatch{PercentPerStep: new(50.0), StepIntervalDays: new(14)}
		require.NoError(t, editorValidateRun(t, editorPolicyBefore(), intent))
	})
}

func TestValidatePlanEditorTarget_EnableAndImmediate(t *testing.T) {
	t.Run("enabling a plan with no services is a 400", func(t *testing.T) {
		before := editorPolicyBefore()
		before.Plan.Enabled, before.Plan.AutoPurchase, before.Plan.Services = false, false, map[string]config.ServiceConfig{}
		intent := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{Enabled: new(true)}}}
		err := editorValidateRun(t, before, intent)
		requireEditorPolicyRefusal(t, err, http.StatusBadRequest, codeInvalidRequest, "enable empty")
		require.Contains(t, err.Error(), "at least one service")
	})
	t.Run("an unrelated edit of a stored enabled plan with no services passes", func(t *testing.T) {
		before := editorPolicyBefore()
		before.Plan.AutoPurchase, before.Plan.Services = false, map[string]config.ServiceConfig{}
		rename := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{Name: new("renamed")}}}
		require.NoError(t, editorValidateRun(t, before, rename))
	})
	t.Run("switching a multi-step plan to immediate is unsupported on Edit", func(t *testing.T) {
		before := editorPolicyBefore()
		before.Plan.AutoPurchase, before.Plan.RampSchedule.TotalSteps = false, 4
		intent := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Ramp: config.PlanScheduleRampPatch{Type: new("immediate"), PercentPerStep: new(100.0)}}}
		requireEditorPolicyRefusal(t, editorValidateRun(t, before, intent), http.StatusConflict, codeEditorPlanUnsupported, "immediate")
	})
	t.Run("a step_count above one on an immediate plan is refused by the equal-count rule", func(t *testing.T) {
		before := editorPolicyBefore()
		before.Plan.AutoPurchase, before.Plan.RampSchedule.Type, before.Plan.RampSchedule.TotalSteps = false, "immediate", 1
		intent := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{StepCount: new(2)}}
		requireEditorPolicyRefusal(t, editorValidateRun(t, before, intent), http.StatusBadRequest, codeInvalidRequest, "step_count")
	})
	t.Run("an unrelated edit of a stored multi-step immediate plan is not blocked", func(t *testing.T) {
		before := editorPolicyBefore()
		before.Plan.AutoPurchase, before.Plan.RampSchedule.Type, before.Plan.RampSchedule.TotalSteps = false, "immediate", 4
		rename := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{Name: new("renamed")}}}
		require.NoError(t, editorValidateRun(t, before, rename))
	})
	t.Run("an immediate ramp with no steps yet is not refused on Edit", func(t *testing.T) {
		before := editorPolicyBefore()
		before.Plan.AutoPurchase, before.Plan.RampSchedule.TotalSteps = false, 0
		intent := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Ramp: config.PlanScheduleRampPatch{Type: new("immediate"), PercentPerStep: new(100.0)}}}
		require.NoError(t, editorValidateRun(t, before, intent))
	})
}

func TestValidatePlanEditorTarget_ChangedOnlyAndCreate(t *testing.T) {
	legacy := func() *config.PlanEditorSnapshot {
		before := editorPolicyBefore()
		before.Plan.Enabled, before.Plan.AutoPurchase = false, false
		before.Plan.Services["legacy"] = config.ServiceConfig{Provider: "azure", Service: "vm", Term: 7}
		return before
	}
	t.Run("a cap-only edit does not revalidate services", func(t *testing.T) {
		intent := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{CapSupplied: true, MaxStepCommitment: new(3.0)}}}
		require.NoError(t, editorValidateRun(t, legacy(), intent))
	})
	t.Run("an untouched invalid disabled service passes an unrelated edit", func(t *testing.T) {
		intent := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{Name: new("renamed")}}}
		require.NoError(t, editorValidateRun(t, legacy(), intent))
	})
	t.Run("a patch repeating the stored invalid service value is not a change", func(t *testing.T) {
		intent := editorValidatePatch("legacy", func(p *config.PlanScheduleServicePatch) { p.Term = new(7) })
		require.NoError(t, editorValidateRun(t, legacy(), intent))
	})
	t.Run("an untouched empty legacy name passes an unrelated edit", func(t *testing.T) {
		before := legacy()
		before.Plan.Name = ""
		intent := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{NotificationDaysBefore: new(4)}}}
		require.NoError(t, editorValidateRun(t, before, intent))
	})
	t.Run("clearing a cap that is already absent is not a change", func(t *testing.T) {
		before := legacy()
		before.Plan.AutoPurchase, before.Plan.MaxStepCommitment = true, nil
		intent := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{CapSupplied: true}}}
		require.NoError(t, editorValidateRun(t, before, intent))
	})
	t.Run("touching the invalid service validates it", func(t *testing.T) {
		intent := editorValidatePatch("legacy", func(p *config.PlanScheduleServicePatch) { p.Coverage = new(10.0) })
		requireEditorPolicyRefusal(t, editorValidateRun(t, legacy(), intent), http.StatusBadRequest, codeInvalidRequest, "touched")
	})
	t.Run("Create validates the whole plan, with the legacy prefix", func(t *testing.T) {
		plan := legacy().Plan
		target := *plan
		target.Name = "created"
		err := validatePlanEditorTarget(nil, &target, &planEditorIntent{})
		requireEditorPolicyRefusal(t, err, http.StatusBadRequest, codeInvalidRequest, "create")
		require.Contains(t, err.Error(), "validation error: ")
	})
}

func TestValidatePlanEditorTarget_SavingsPlansMultiStep(t *testing.T) {
	stored := func(armed bool) *config.PlanEditorSnapshot {
		before := editorPolicyBefore()
		before.Plan.AutoPurchase = armed
		before.Plan.RampSchedule.TotalSteps = 3
		before.Plan.Services = map[string]config.ServiceConfig{"k": {Provider: "aws", Service: "savingsplans", Enabled: true, Term: 1, Payment: "no-upfront", Coverage: 50}}
		return before
	}
	arm := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{AutoPurchase: new(true)}}}
	t.Run("arming a stored multi-step plan is unsupported", func(t *testing.T) {
		requireEditorPolicyRefusal(t, editorValidateRun(t, stored(false), arm), http.StatusConflict, codeEditorPlanUnsupported, "arm")
	})
	t.Run("enabling a stored armed disabled plan is unsupported", func(t *testing.T) {
		before := stored(true)
		before.Plan.Enabled = false
		enable := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{Enabled: new(true)}}}
		requireEditorPolicyRefusal(t, editorValidateRun(t, before, enable), http.StatusConflict, codeEditorPlanUnsupported, "enable")
	})
	t.Run("a service change on an armed plan is unsupported", func(t *testing.T) {
		intent := editorValidatePatch("k", func(p *config.PlanScheduleServicePatch) { p.Coverage = new(60.0) })
		requireEditorPolicyRefusal(t, editorValidateRun(t, stored(true), intent), http.StatusConflict, codeEditorPlanUnsupported, "service")
	})
	t.Run("a step count above one on an armed plan is unsupported", func(t *testing.T) {
		b := stored(true)
		b.Plan.RampSchedule.TotalSteps = 1
		requireEditorPolicyRefusal(t, editorValidateRun(t, b, &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{StepCount: new(2)}}), http.StatusConflict, codeEditorPlanUnsupported, "count")
	})
	t.Run("an unrelated edit of a stored armed plan is not blocked", func(t *testing.T) {
		rename := &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Plan: config.PlanSchedulePlanPatch{Name: new("renamed")}}}
		require.NoError(t, editorValidateRun(t, stored(true), rename))
	})
	t.Run("Create of the same plan gives the same code", func(t *testing.T) {
		target := stored(true).Plan
		requireEditorPolicyRefusal(t, validatePlanEditorTarget(nil, target, &planEditorIntent{}), http.StatusConflict, codeEditorPlanUnsupported, "create")
	})
}
