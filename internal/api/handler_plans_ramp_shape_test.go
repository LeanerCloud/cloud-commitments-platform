package api

import (
	"context"
	"testing"

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
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", ctx, "test-token").Return(&Session{UserID: "admin-id", Email: "admin@example.com"}, nil)
	mockAuth.grantAdmin()
	mockAuth.On("ValidateCSRFToken", ctx, mock.Anything, mock.Anything).Return(nil)

	var saved *config.PurchasePlan
	mockStore.On("GetPurchasePlan", mock.Anything, existing.ID).Return(existing, nil)
	mockStore.On("UpdatePurchasePlan", mock.Anything, mock.AnythingOfType("*config.PurchasePlan")).
		Run(func(args mock.Arguments) { saved = args.Get(1).(*config.PurchasePlan) }).Return(nil)

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
