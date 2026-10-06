package api

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Issue #609 (tier A). Plan steps carry no recommendations, so a step can
// never buy anything. These tests use the reported plan: Azure compute, 1 year,
// monthly payment, Auto-Purchase on.

const (
	bareStepAPIPlanID = "11111111-1111-1111-1111-111111111609"
	bareStepAPIExecID = "22222222-3333-4444-5555-666666666609"
)

func bareStepAPIPlan() *config.PurchasePlan {
	return &config.PurchasePlan{
		ID:           bareStepAPIPlanID,
		Name:         "Azure compute 80% weekly",
		Enabled:      true,
		AutoPurchase: true,
		Services: map[string]config.ServiceConfig{
			"azure:compute": {Provider: "azure", Service: "compute", Term: 1, Payment: "monthly"},
		},
		RampSchedule: config.RampSchedule{CurrentStep: 0, TotalSteps: 4, StepIntervalDays: 7},
	}
}

func TestAddPurchasesToPlanIsRefusedWhileStepsCannotCarryRecommendations(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })

	mockAuth.On("ValidateSession", ctx, "admin-token").Return(&Session{
		UserID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		Email:  "admin@example.com",
	}, nil)
	mockAuth.grantAdmin()
	mockStore.On("GetPurchasePlan", ctx, bareStepAPIPlanID).Return(bareStepAPIPlan(), nil)
	// Nothing may be written: reaching the transaction at all is the defect.
	mockStore.On("WithTx", ctx, mock.AnythingOfType("func(pgx.Tx) error")).
		Return(errors.New("transaction opened: the guard did not fire")).Maybe()

	handler := &Handler{config: mockStore, auth: mockAuth}
	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer admin-token"},
		Body:    `{"count": 4, "start_date": "2026-10-07"}`,
	}

	result, err := handler.createPlannedPurchases(ctx, req, bareStepAPIPlanID)

	require.Error(t, err, "Add Purchases must not create steps that cannot buy anything")
	assert.Nil(t, result)
	ce, ok := IsClientError(err)
	require.True(t, ok, "expected a client error, got %v", err)
	assert.Equal(t, 409, ce.code)
	assert.Contains(t, ce.Error(), "#609")
	mockStore.AssertNotCalled(t, "SavePurchaseExecutionTx")
}

func TestRetryOfBarePlanStepIsRefusedAndCreatesNothing(t *testing.T) {
	creator := retryCallerID
	failed := &config.PurchaseExecution{
		ExecutionID: bareStepAPIExecID,
		PlanID:      bareStepAPIPlanID,
		StepNumber:  1,
		Status:      "failed",
		// A transient failure that is not one of the persistent-failure
		// hints, so the refusal under test is the only gate that can fire.
		Error:           "failed to send approval email: SES throttle exceeded",
		CreatedByUserID: &creator,
		Source:          common.PurchaseSourceWeb,
	}
	// purchasesFiredByRetry asserts that a refused retry persisted no successor.
	tokens, err := purchasesFiredByRetry(t, failed, sessionRetryReq())

	assert.Empty(t, tokens)
	require.Error(t, err, "retrying a plan step with no recommendations must be refused")
	ce, ok := IsClientError(err)
	require.True(t, ok, "expected a client error, got %v", err)
	assert.Equal(t, 409, ce.code)
	assert.Contains(t, ce.Error(), "no recommendations")
}
