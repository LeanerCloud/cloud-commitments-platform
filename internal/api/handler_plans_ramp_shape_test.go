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
	t.Parallel()
	const planID = "12345678-1234-1234-1234-123456789abc"
	weekly := config.PresetRampSchedules[config.RampWeekly25Pct]
	custom30 := config.RampSchedule{Type: "custom", PercentPerStep: 30, StepIntervalDays: 7, TotalSteps: 3}

	tests := []struct {
		name       string
		body       string
		wantType   string
		stored     config.RampSchedule
		step       int
		wantStatus int
		wantStep   int
	}{
		{name: "in-progress ramp, different shape is refused", stored: weekly, step: 3, body: `{"name":"p","ramp_schedule":"monthly-10pct"}`, wantStatus: 409, wantStep: 0, wantType: ""},
		{name: "in-progress ramp, omitted ramp_schedule is refused", stored: weekly, step: 3, body: `{"name":"renamed"}`, wantStatus: 409, wantStep: 0, wantType: ""},
		{name: "first completed step, different shape is refused", stored: weekly, step: 1, body: `{"name":"p","ramp_schedule":"monthly-10pct"}`, wantStatus: 409, wantStep: 0, wantType: ""},
		// 30% and 33% both give 3 steps: only PercentPerStep differs.
		{name: "in-progress ramp, only the percent differs, is refused", stored: custom30, step: 2, body: `{"name":"p","ramp_schedule":"custom","custom_step_percent":33,"custom_interval_days":7}`, wantStatus: 409, wantStep: 0, wantType: ""},
		{name: "in-progress ramp, same shape keeps its step", stored: weekly, step: 3, body: `{"name":"renamed","ramp_schedule":"weekly-25pct"}`, wantStatus: 200, wantStep: 3, wantType: "weekly"},
		{name: "no completed step, different shape is allowed", stored: weekly, step: 0, body: `{"name":"p","ramp_schedule":"monthly-10pct"}`, wantStatus: 200, wantStep: 0, wantType: "monthly"},
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
	t.Parallel()
	const planID = "12345678-1234-1234-1234-123456789abc"
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	next := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	weekly20 := config.RampSchedule{Type: "weekly", PercentPerStep: 20, StepIntervalDays: 7, TotalSteps: 5}
	monthly5 := config.RampSchedule{Type: "monthly", PercentPerStep: 5, StepIntervalDays: 30, TotalSteps: 20}
	custom25 := config.RampSchedule{Type: "custom", PercentPerStep: 25, StepIntervalDays: 7, TotalSteps: 4}

	tests := []struct {
		name   string
		body   string
		stored config.RampSchedule
		step   int
	}{
		{name: "in-progress weekly 20%, renamed via custom", stored: weekly20, step: 3,
			body: `{"name":"renamed","ramp_schedule":"custom","custom_step_percent":20,"custom_interval_days":7}`},
		{name: "in-progress monthly 5%, renamed via custom", stored: monthly5, step: 1,
			body: `{"name":"renamed","ramp_schedule":"custom","custom_step_percent":5,"custom_interval_days":30}`},
		{name: "in-progress custom 25%/7d, renamed via the equal weekly preset", stored: custom25, step: 2,
			body: `{"name":"renamed","ramp_schedule":"weekly-25pct"}`},
		{name: "not started weekly 20%, renamed via custom", stored: weekly20, step: 0,
			body: `{"name":"renamed","ramp_schedule":"custom","custom_step_percent":20,"custom_interval_days":7}`},
		{name: "not started monthly 5%, renamed via custom", stored: monthly5, step: 0,
			body: `{"name":"renamed","ramp_schedule":"custom","custom_step_percent":5,"custom_interval_days":30}`},
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
	t.Parallel()
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
		body   string
		stored config.RampSchedule
	}{
		{name: "different percent and step count", stored: weekly20,
			body: `{"name":"p","ramp_schedule":"custom","custom_step_percent":25,"custom_interval_days":7}`},
		{name: "different interval", stored: weekly20,
			body: `{"name":"p","ramp_schedule":"custom","custom_step_percent":20,"custom_interval_days":14}`},
		{name: "different percent, same step count", stored: weekly30,
			body: `{"name":"p","ramp_schedule":"custom","custom_step_percent":33,"custom_interval_days":7}`},
		{name: "weekly to immediate", stored: weekly20, body: `{"name":"p","ramp_schedule":"immediate"}`},
		{name: "immediate to custom 100%", stored: immediate,
			body: `{"name":"p","ramp_schedule":"custom","custom_step_percent":100,"custom_interval_days":1}`},
		{name: "custom 100% to immediate", stored: custom100, body: `{"name":"p","ramp_schedule":"immediate"}`},
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
	t.Parallel()
	const planID = "12345678-1234-1234-1234-123456789abc"
	weekly := config.PresetRampSchedules[config.RampWeekly25Pct]
	immediate := config.PresetRampSchedules["immediate"]
	// 60% steps give one step (int(100/60)), so TotalSteps is 1 but it buys 60%,
	// not the whole base: the immediate fallback would raise it to 100%.
	partialOneStep := config.RampSchedule{Type: "custom", PercentPerStep: 60, StepIntervalDays: 7, TotalSteps: 1}

	tests := []struct {
		name       string
		body       string
		wantType   string
		stored     config.RampSchedule
		wantStatus int
	}{
		{name: "omitted over a stored weekly ramp is refused", stored: weekly, body: `{"name":"renamed"}`, wantStatus: 409, wantType: ""},
		{name: "omitted over a stored one-step 60% custom ramp is refused", stored: partialOneStep, body: `{"name":"renamed"}`, wantStatus: 409, wantType: ""},
		{name: "omitted over a stored immediate plan is fine", stored: immediate, body: `{"name":"renamed"}`, wantStatus: 200, wantType: "immediate"},
		{name: "omitted over a legacy plan with no stored ramp is fine", stored: config.RampSchedule{}, body: `{"name":"renamed"}`, wantStatus: 200, wantType: "immediate"},
		{name: "sending the stored ramp keeps it", stored: weekly, body: `{"name":"renamed","ramp_schedule":"weekly-25pct"}`, wantStatus: 200, wantType: "weekly"},
		{name: "sending immediate explicitly is allowed", stored: weekly, body: `{"name":"renamed","ramp_schedule":"immediate"}`, wantStatus: 200, wantType: "immediate"},
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
	t.Parallel()
	plan := (&PlanRequest{Name: "p"}).toPurchasePlan()

	assert.Equal(t, "immediate", plan.RampSchedule.Type)
	assert.Equal(t, 1, plan.RampSchedule.TotalSteps)
	assert.Equal(t, 100.0, plan.RampSchedule.PercentPerStep)
}

// #1935: custom_step_percent and custom_interval_days are read only with
// ramp_schedule "custom"; sent without it they would be ignored and the plan
// would buy everything at once, so the request is refused.
func TestDecodePlanRequest_CustomCadenceWithoutCustomSchedule(t *testing.T) {
	t.Parallel()
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
