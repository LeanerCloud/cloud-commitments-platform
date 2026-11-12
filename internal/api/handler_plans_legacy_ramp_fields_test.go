package api

import (
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const legacyRampPlanID = "12345678-1234-1234-1234-123456789abc"

func equalCountPlan(step int) *config.PurchasePlan {
	return &config.PurchasePlan{ID: legacyRampPlanID, Name: "p", Enabled: true,
		UpdatedAt: time.Date(2026, 10, 1, 12, 0, 0, 123456000, time.UTC),
		RampSchedule: config.RampSchedule{Type: "custom", PercentPerStep: 25, StepIntervalDays: 7,
			TotalSteps: 4, CurrentStep: step, AllocationMode: config.RampAllocationEqualCount}}
}

func TestUpdatePlan_CustomTotalStepsEqualCount(t *testing.T) {
	existing := &config.PurchasePlan{ID: legacyRampPlanID, Name: "p", Enabled: true, RampSchedule: config.PresetRampSchedules[config.RampWeekly25Pct]}
	resp, saved := putPlan(t, existing, `{"name":"p","ramp_schedule":"custom","custom_total_steps":7,"ramp_allocation":"equal_count","custom_interval_days":14}`)
	require.Equal(t, 200, resp.StatusCode, resp.Body)
	require.NotNil(t, saved)
	rs := saved.RampSchedule
	assert.Equal(t, 7, rs.TotalSteps)
	assert.InDelta(t, 100.0/7, rs.PercentPerStep, 1e-9)
	assert.Equal(t, "custom", rs.Type)
	assert.Equal(t, 14, rs.StepIntervalDays)
	assert.Equal(t, config.RampAllocationEqualCount, rs.AllocationMode)
}

func TestUpdatePlan_RampFieldValidation(t *testing.T) {
	existing := equalCountPlan(0)
	for name, body := range map[string]string{
		"steps without allocation":   `{"name":"p","ramp_schedule":"custom","custom_total_steps":7}`,
		"allocation without steps":   `{"name":"p","ramp_schedule":"custom","ramp_allocation":"equal_count"}`,
		"unknown allocation":         `{"name":"p","ramp_schedule":"custom","custom_total_steps":7,"ramp_allocation":"front_loaded"}`,
		"steps above max":            `{"name":"p","ramp_schedule":"custom","custom_total_steps":101,"ramp_allocation":"equal_count"}`,
		"negative steps":             `{"name":"p","ramp_schedule":"custom","custom_total_steps":-1,"ramp_allocation":"equal_count"}`,
		"not a custom schedule":      `{"name":"p","ramp_schedule":"weekly-25pct","custom_total_steps":7,"ramp_allocation":"equal_count"}`,
		"malformed expected version": `{"name":"p","ramp_schedule":"custom","custom_total_steps":4,"ramp_allocation":"equal_count","expected_updated_at":"yesterday"}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp, saved := putPlan(t, existing, body)
			assert.Equal(t, 400, resp.StatusCode, resp.Body)
			assert.Nil(t, saved)
		})
	}
}

func TestUpdatePlan_AllocationModeChangeIsAShapeChange(t *testing.T) {
	// Same type, percent, interval and step count: only the allocation mode differs.
	legacyBody := `{"name":"p","ramp_schedule":"custom","custom_step_percent":25,"custom_interval_days":7}`
	t.Run("step 0 is accepted", func(t *testing.T) {
		existing := equalCountPlan(0)
		resp, saved := putPlan(t, existing, legacyBody)
		require.Equal(t, 200, resp.StatusCode, resp.Body)
		assert.Equal(t, config.RampAllocationMode(""), saved.RampSchedule.AllocationMode)
	})
	t.Run("step 2 is refused", func(t *testing.T) {
		existing := equalCountPlan(2)
		resp, saved := putPlan(t, existing, legacyBody)
		assert.Equal(t, 409, resp.StatusCode, resp.Body)
		assert.Nil(t, saved)
	})
	t.Run("resending equal_count at step 2 keeps progress", func(t *testing.T) {
		existing := equalCountPlan(2)
		resp, saved := putPlan(t, existing, `{"name":"p","ramp_schedule":"custom","custom_total_steps":4,"ramp_allocation":"equal_count","custom_interval_days":7}`)
		require.Equal(t, 200, resp.StatusCode, resp.Body)
		assert.Equal(t, 2, saved.RampSchedule.CurrentStep)
	})
}

func TestUpdatePlan_ExpectedUpdatedAt(t *testing.T) {
	body := func(v string) string {
		return `{"name":"p","ramp_schedule":"custom","custom_total_steps":4,"ramp_allocation":"equal_count","custom_interval_days":7,"expected_updated_at":"` + v + `"}`
	}
	t.Run("stale version is refused", func(t *testing.T) {
		resp, saved := putPlan(t, equalCountPlan(0), body("2026-10-01T11:59:59.123456Z"))
		assert.Equal(t, 409, resp.StatusCode, resp.Body)
		assert.Contains(t, resp.Body, "plan changed; refresh")
		assert.Nil(t, saved)
	})
	t.Run("equal microsecond version is accepted", func(t *testing.T) {
		resp, saved := putPlan(t, equalCountPlan(0), body("2026-10-01T12:00:00.123456Z"))
		require.Equal(t, 200, resp.StatusCode, resp.Body)
		assert.True(t, saved.UpdatedAt.Equal(time.Date(2026, 10, 1, 12, 0, 0, 123456000, time.UTC)))
	})
	t.Run("same instant in another zone is accepted", func(t *testing.T) {
		resp, _ := putPlan(t, equalCountPlan(0), body("2026-10-01T14:00:00.123456+02:00"))
		assert.Equal(t, 200, resp.StatusCode, resp.Body)
	})
}

func legacySlugPlan(minCount int, ramp string) *config.PurchasePlan {
	return &config.PurchasePlan{ID: legacyRampPlanID, Name: "p", Enabled: true, RampSchedule: immediateRamp(),
		Services: map[string]config.ServiceConfig{
			"azure/vm": {Provider: "azure", Service: "vm", Enabled: true, Term: 1, Payment: "monthly", Coverage: 70,
				IncludeRegions: []string{"westeurope"}, MinCount: minCount, RampSchedule: ramp},
		}}
}

func TestMergeUpdatedPlan_LegacySlugEntryKeepsSettingsUnderItsKey(t *testing.T) {
	req := &PlanRequest{Name: "p", Provider: "azure", Service: "compute", Term: 3, Payment: "upfront",
		TargetCoverage: 90, RampSchedule: "immediate", Enabled: true}
	plan, err := mergeUpdatedPlan(legacySlugPlan(2, "weekly-25pct"), req, legacyRampPlanID, false)
	require.NoError(t, err)
	require.Len(t, plan.Services, 1)
	svc, ok := plan.Services["azure/vm"]
	require.True(t, ok, "the stored key must not change: %v", plan.Services)
	assert.Equal(t, []string{"westeurope"}, svc.IncludeRegions)
	assert.Equal(t, 2, svc.MinCount)
	assert.Equal(t, "weekly-25pct", svc.RampSchedule)
	assert.Equal(t, "compute", svc.Service)
	assert.Equal(t, 3, svc.Term)
	assert.Equal(t, 90.0, svc.Coverage)
}

func TestMergeUpdatedPlan_ValueMismatchOnFilteredEntryIsRefused(t *testing.T) {
	for _, svc := range []string{"relational-db", "cache"} {
		req := &PlanRequest{Name: "p", Provider: "azure", Service: svc, RampSchedule: "immediate", Enabled: true}
		_, err := mergeUpdatedPlan(legacySlugPlan(0, ""), req, legacyRampPlanID, false)
		ce, ok := IsClientError(err)
		require.True(t, ok, "want ClientError, got %v", err)
		assert.Equal(t, 409, ce.code)
	}
	req := &PlanRequest{Name: "p", Provider: "gcp", Service: "compute", RampSchedule: "immediate", Enabled: true}
	_, err := mergeUpdatedPlan(legacySlugPlan(0, ""), req, legacyRampPlanID, false)
	ce, _ := IsClientError(err)
	require.NotNil(t, ce)
	assert.Equal(t, 409, ce.code)
}

func TestMergeUpdatedPlan_ValueMismatchOnMinCountOrRampEntryIsRefused(t *testing.T) {
	for name, existing := range map[string]*config.PurchasePlan{
		"min count": legacySlugPlan(3, ""),
		"ramp":      legacySlugPlan(0, "weekly-25pct"),
	} {
		t.Run(name, func(t *testing.T) {
			existing.Services["azure/vm"] = withoutFilters(existing.Services["azure/vm"])
			req := &PlanRequest{Name: "p", Provider: "azure", Service: "cache", Payment: "monthly", RampSchedule: "immediate", Enabled: true}
			_, err := mergeUpdatedPlan(existing, req, legacyRampPlanID, false)
			ce, ok := IsClientError(err)
			require.True(t, ok, "want ClientError, got %v", err)
			assert.Equal(t, 409, ce.code)
		})
	}
}

func withoutFilters(s config.ServiceConfig) config.ServiceConfig {
	s.IncludeRegions = nil
	return s
}

func TestMergeUpdatedPlan_ValueMismatchOnPlainEntryStillSwitches(t *testing.T) {
	existing := legacySlugPlan(0, "")
	existing.Services["azure/vm"] = config.ServiceConfig{Provider: "azure", Service: "vm", Enabled: true, Term: 1, Payment: "monthly", Coverage: 70}
	req := &PlanRequest{Name: "p", Provider: "azure", Service: "cache", Payment: "monthly", RampSchedule: "immediate", Enabled: true}
	plan, err := mergeUpdatedPlan(existing, req, legacyRampPlanID, false)
	require.NoError(t, err)
	assert.Contains(t, plan.Services, "azure/cache")
}

func TestUpdatePlan_ManagedRampOmittingAllocationKeepsMarkers(t *testing.T) {
	existing := equalCountPlan(2)
	existing.RampSchedule.DatesManaged = true
	resp, saved := putPlan(t, existing, `{"name":"renamed","ramp_schedule":"custom","custom_step_percent":25,"custom_interval_days":7}`)
	require.Equal(t, 200, resp.StatusCode, resp.Body)
	assert.True(t, saved.RampSchedule.DatesManaged)
	assert.Equal(t, config.RampAllocationEqualCount, saved.RampSchedule.AllocationMode)
	assert.Equal(t, 2, saved.RampSchedule.CurrentStep)
}
