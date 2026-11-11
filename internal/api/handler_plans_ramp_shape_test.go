package api

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// putPlan drives PUT /api/plans/{id} through the router against an existing
// plan and returns the response plus the plan handed to UpdatePurchasePlan
// (nil when the update was refused).
func putPlan(t *testing.T, existing *config.PurchasePlan, body string) (*events.LambdaFunctionURLResponse, *config.PurchasePlan) {
	t.Helper()
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	// The admin X-API-Key authenticates the request, so the bearer session is
	// never validated.
	mockAuth.grantAdmin()

	var saved *config.PurchasePlan
	mockStore.On("GetPurchasePlan", mock.Anything, existing.ID).Return(existing, nil)
	// Maybe: a refused update never reaches the store. Callers prove which
	// way it went through the returned plan (nil when refused).
	mockStore.On("UpdatePurchasePlan", mock.Anything, mock.AnythingOfType("*config.PurchasePlan")).
		Run(func(args mock.Arguments) { saved = args.Get(1).(*config.PurchasePlan) }).Return(nil).Maybe()

	handler := &Handler{config: mockStore, auth: mockAuth, apiKey: "test-key"}
	resp, err := handler.HandleRequest(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"X-API-Key": "test-key", "Authorization": "Bearer test-token",
			"X-CSRF-Token": "test-csrf", "Content-Type": "application/json",
		},
		Body: body,
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: "PUT", Path: "/api/plans/" + existing.ID},
		},
	})
	require.NoError(t, err)
	return resp, saved
}

func TestUpdatePlan_RampShapeChange(t *testing.T) {
	const planID = "12345678-1234-1234-1234-123456789abc"
	weekly := config.PresetRampSchedules[config.RampWeekly25Pct]
	custom30 := config.RampSchedule{Type: "custom", PercentPerStep: 30, StepIntervalDays: 7, TotalSteps: 3}

	tests := []struct {
		name       string
		stored     config.RampSchedule
		step       int
		body       string
		wantStatus int
		wantStep   int
		wantType   string
	}{
		{"in-progress ramp, different shape is refused", weekly, 3, `{"name":"p","ramp_schedule":"monthly-10pct"}`, 409, 0, ""},
		{"in-progress ramp, omitted ramp_schedule is refused", weekly, 3, `{"name":"renamed"}`, 409, 0, ""},
		{"first completed step, different shape is refused", weekly, 1, `{"name":"p","ramp_schedule":"monthly-10pct"}`, 409, 0, ""},
		// 30% and 33% both give 3 steps: only PercentPerStep differs.
		{"in-progress ramp, only the percent differs, is refused", custom30, 2, `{"name":"p","ramp_schedule":"custom","custom_step_percent":33,"custom_interval_days":7}`, 409, 0, ""},
		{"in-progress ramp, same shape keeps its step", weekly, 3, `{"name":"renamed","ramp_schedule":"weekly-25pct"}`, 200, 3, "weekly"},
		{"no completed step, different shape is allowed", weekly, 0, `{"name":"p","ramp_schedule":"monthly-10pct"}`, 200, 0, "monthly"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rs := tc.stored
			rs.CurrentStep = tc.step
			existing := &config.PurchasePlan{ID: planID, Name: "p", Enabled: true, RampSchedule: rs}

			resp, saved := putPlan(t, existing, tc.body)

			assert.Equal(t, tc.wantStatus, resp.StatusCode)
			if tc.wantStatus == 409 {
				assert.Nil(t, saved, "a refused shape change must not reach the store")
				assert.Contains(t, resp.Body, "create a new plan")
				return
			}
			require.NotNil(t, saved)
			assert.Equal(t, tc.wantStep, saved.RampSchedule.CurrentStep)
			assert.Equal(t, tc.wantType, saved.RampSchedule.Type)
		})
	}
}

// TestUpdatePlan_EquivalentCustomRampKeepsStoredSchedule pins issue #1184.
// The UI can send only the presets or "custom", so Edit resubmits a stored
// non-preset weekly/monthly ramp as an equal custom one. The type label is
// cosmetic for step-based ramps (only "immediate" changes behavior), so an
// equal percent, interval and step count is the same schedule: an in-progress
// plan keeps its progress, and the stored type, start date and next execution
// date are kept rather than rebuilt, whether or not a step has completed.
func TestUpdatePlan_EquivalentCustomRampKeepsStoredSchedule(t *testing.T) {
	const planID = "12345678-1234-1234-1234-123456789abc"
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	next := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	weekly20 := config.RampSchedule{Type: "weekly", PercentPerStep: 20, StepIntervalDays: 7, TotalSteps: 5}
	monthly5 := config.RampSchedule{Type: "monthly", PercentPerStep: 5, StepIntervalDays: 30, TotalSteps: 20}
	custom25 := config.RampSchedule{Type: "custom", PercentPerStep: 25, StepIntervalDays: 7, TotalSteps: 4}

	tests := []struct {
		name   string
		stored config.RampSchedule
		step   int
		body   string
	}{
		{"in-progress weekly 20%, renamed via custom", weekly20, 3,
			`{"name":"renamed","ramp_schedule":"custom","custom_step_percent":20,"custom_interval_days":7}`},
		{"in-progress monthly 5%, renamed via custom", monthly5, 1,
			`{"name":"renamed","ramp_schedule":"custom","custom_step_percent":5,"custom_interval_days":30}`},
		{"in-progress custom 25%/7d, renamed via the equal weekly preset", custom25, 2,
			`{"name":"renamed","ramp_schedule":"weekly-25pct"}`},
		{"not started weekly 20%, renamed via custom", weekly20, 0,
			`{"name":"renamed","ramp_schedule":"custom","custom_step_percent":20,"custom_interval_days":7}`},
		{"not started monthly 5%, renamed via custom", monthly5, 0,
			`{"name":"renamed","ramp_schedule":"custom","custom_step_percent":5,"custom_interval_days":30}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rs := tc.stored
			rs.CurrentStep = tc.step
			rs.StartDate = start
			nextExec := next
			existing := &config.PurchasePlan{ID: planID, Name: "p", Enabled: true, RampSchedule: rs, NextExecutionDate: &nextExec}

			resp, saved := putPlan(t, existing, tc.body)

			require.Equal(t, 200, resp.StatusCode, resp.Body)
			require.NotNil(t, saved)
			assert.Equal(t, "renamed", saved.Name)
			assert.Equal(t, rs, saved.RampSchedule, "the stored schedule (type, progress, start date) must be kept")
			require.NotNil(t, saved.NextExecutionDate)
			assert.True(t, next.Equal(*saved.NextExecutionDate), "next execution date must be kept, got %v", saved.NextExecutionDate)
		})
	}
}

// TestUpdatePlan_RealRampChangeOnNonPresetStillRefused is the other half of
// #1184: label-agnostic matching must not let a real change through on an
// in-progress plan.
func TestUpdatePlan_RealRampChangeOnNonPresetStillRefused(t *testing.T) {
	const planID = "12345678-1234-1234-1234-123456789abc"
	weekly20 := config.RampSchedule{Type: "weekly", PercentPerStep: 20, StepIntervalDays: 7, TotalSteps: 5, CurrentStep: 2}
	// 30% and 33% both give 3 steps: only the percent differs.
	weekly30 := config.RampSchedule{Type: "weekly", PercentPerStep: 30, StepIntervalDays: 7, TotalSteps: 3, CurrentStep: 1}
	immediate := config.PresetRampSchedules[config.RampImmediate]
	immediate.CurrentStep = 1
	// Same percent, interval and step count as immediate: only the type
	// differs, and "immediate" is never interchangeable with a step-based type.
	custom100 := config.RampSchedule{Type: "custom", PercentPerStep: 100, StepIntervalDays: 0, TotalSteps: 1, CurrentStep: 1}

	tests := []struct {
		name   string
		stored config.RampSchedule
		body   string
	}{
		{"different percent and step count", weekly20,
			`{"name":"p","ramp_schedule":"custom","custom_step_percent":25,"custom_interval_days":7}`},
		{"different interval", weekly20,
			`{"name":"p","ramp_schedule":"custom","custom_step_percent":20,"custom_interval_days":14}`},
		{"different percent, same step count", weekly30,
			`{"name":"p","ramp_schedule":"custom","custom_step_percent":33,"custom_interval_days":7}`},
		{"weekly to immediate", weekly20, `{"name":"p","ramp_schedule":"immediate"}`},
		{"immediate to custom 100%", immediate,
			`{"name":"p","ramp_schedule":"custom","custom_step_percent":100,"custom_interval_days":1}`},
		{"custom 100% to immediate", custom100, `{"name":"p","ramp_schedule":"immediate"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			existing := &config.PurchasePlan{ID: planID, Name: "p", Enabled: true, RampSchedule: tc.stored}

			resp, saved := putPlan(t, existing, tc.body)

			assert.Equal(t, 409, resp.StatusCode, resp.Body)
			assert.Nil(t, saved, "a refused shape change must not reach the store")
			assert.Contains(t, resp.Body, "create a new plan")
		})
	}
}

// #1935: PUT replaces the ramp, so an omitted ramp_schedule means immediate. On a
// plan with no completed step that used to turn a weekly or monthly ramp into
// buying the whole base in one step without a word; it is now refused, like the
// in-progress case, unless the stored schedule already is the immediate one.
func TestUpdatePlan_OmittedRampSchedule(t *testing.T) {
	const planID = "12345678-1234-1234-1234-123456789abc"
	weekly := config.PresetRampSchedules[config.RampWeekly25Pct]
	immediate := config.PresetRampSchedules["immediate"]
	// 60% steps give one step (int(100/60)), so TotalSteps is 1 but it buys 60%,
	// not the whole base: the immediate fallback would raise it to 100%.
	partialOneStep := config.RampSchedule{Type: "custom", PercentPerStep: 60, StepIntervalDays: 7, TotalSteps: 1}

	tests := []struct {
		name       string
		stored     config.RampSchedule
		body       string
		wantStatus int
		wantType   string
	}{
		{"omitted over a stored weekly ramp is refused", weekly, `{"name":"renamed"}`, 409, ""},
		{"omitted over a stored one-step 60% custom ramp is refused", partialOneStep, `{"name":"renamed"}`, 409, ""},
		{"omitted over a stored immediate plan is fine", immediate, `{"name":"renamed"}`, 200, "immediate"},
		{"omitted over a legacy plan with no stored ramp is fine", config.RampSchedule{}, `{"name":"renamed"}`, 200, "immediate"},
		{"sending the stored ramp keeps it", weekly, `{"name":"renamed","ramp_schedule":"weekly-25pct"}`, 200, "weekly"},
		{"sending immediate explicitly is allowed", weekly, `{"name":"renamed","ramp_schedule":"immediate"}`, 200, "immediate"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			existing := &config.PurchasePlan{ID: planID, Name: "p", Enabled: true, RampSchedule: tc.stored}

			resp, saved := putPlan(t, existing, tc.body)

			assert.Equal(t, tc.wantStatus, resp.StatusCode, resp.Body)
			if tc.wantStatus == 409 {
				assert.Nil(t, saved, "a refused update must not reach the store")
				assert.Contains(t, resp.Body, "ramp_schedule was omitted")
				assert.Contains(t, resp.Body, "resend the current ramp_schedule to keep it")
				return
			}
			require.NotNil(t, saved)
			assert.Equal(t, tc.wantType, saved.RampSchedule.Type)
		})
	}
}

// #1935: which preset a plan gets when ramp_schedule is omitted on create is
// pinned here, since the API docs did not say: the immediate one.
func TestPlanRequest_OmittedRampScheduleOnCreateIsImmediate(t *testing.T) {
	plan := (&PlanRequest{Name: "p"}).toPurchasePlan()

	assert.Equal(t, "immediate", plan.RampSchedule.Type)
	assert.Equal(t, 1, plan.RampSchedule.TotalSteps)
	assert.Equal(t, 100.0, plan.RampSchedule.PercentPerStep)
}

// #1935: custom_step_percent and custom_interval_days are read only with
// ramp_schedule "custom"; sent without it they would be ignored and the plan
// would buy everything at once, so the request is refused.
func TestDecodePlanRequest_CustomCadenceWithoutCustomSchedule(t *testing.T) {
	refused := []string{
		`{"name":"p","custom_step_percent":25}`,
		`{"name":"p","custom_interval_days":7}`,
		`{"name":"p","ramp_schedule":"weekly-25pct","custom_step_percent":25,"custom_interval_days":7}`,
		`{"name":"p","ramp_schedule":"immediate","custom_interval_days":7}`,
	}
	for _, body := range refused {
		_, err := decodePlanRequest(body)
		ce, ok := IsClientError(err)
		require.True(t, ok, body)
		assert.Equal(t, 400, ce.code, body)
		assert.Contains(t, ce.message, "apply only with ramp_schedule \"custom\"", body)
	}

	for _, body := range []string{
		`{"name":"p","ramp_schedule":"custom","custom_step_percent":25,"custom_interval_days":7}`,
		`{"name":"p","ramp_schedule":"weekly-25pct"}`,
		`{"name":"p"}`,
	} {
		_, err := decodePlanRequest(body)
		require.NoError(t, err, body)
	}
}
