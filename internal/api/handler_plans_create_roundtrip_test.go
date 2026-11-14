package api

import (
	"context"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// POST /api/plans must store the plan its body describes: the older create test
// asserted only the name, enabled and the account arguments, so a field dropped
// between PlanRequest and the stored plan went unnoticed.
func TestCreatePlan_StoresEveryRequestedField(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	mockAuth.On("ValidateSession", ctx, "admin-token").Return(&Session{UserID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Email: "admin@example.com"}, nil)
	mockAuth.grantAdminPurchaser()

	const accountID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	var created *config.PurchasePlan
	mockStore.On("CreatePurchasePlanWithAccounts", ctx, mock.AnythingOfType("*config.PurchasePlan"), []string{accountID}, mock.Anything).Run(func(args mock.Arguments) {
		p := *args.Get(1).(*config.PurchasePlan)
		created = &p
	}).Return(nil)
	mockStore.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		return &config.CloudAccount{ID: id, Name: "test-aws", Provider: "aws"}, nil
	}

	h := &Handler{config: mockStore, auth: mockAuth}
	_, err := h.createPlan(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer admin-token"},
		Body: `{"name":"Quarterly RDS","enabled":true,"auto_purchase":true,"max_step_commitment":500,"notification_days_before":9,
			"ramp_schedule":"weekly-25pct","provider":"aws","service":"rds",
			"term":1,"payment":"all-upfront","target_coverage":65,
			"target_accounts":["` + accountID + `"]}`,
	})
	require.NoError(t, err)

	require.NotNil(t, created, "the plan was not created")
	assert.Equal(t, "Quarterly RDS", created.Name, "name")
	assert.True(t, created.Enabled, "enabled")
	assert.True(t, created.AutoPurchase, "auto_purchase")
	require.NotNil(t, created.MaxStepCommitment, "max_step_commitment")
	assert.Equal(t, 500.0, *created.MaxStepCommitment, "max_step_commitment")
	assert.Equal(t, 9, created.NotificationDaysBefore, "notification_days_before")
	assert.Equal(t, "weekly", created.RampSchedule.Type, "ramp_schedule type")
	assert.Equal(t, 25.0, created.RampSchedule.PercentPerStep, "ramp_schedule step percent")
	assert.Equal(t, 7, created.RampSchedule.StepIntervalDays, "ramp_schedule interval")
	assert.Equal(t, 4, created.RampSchedule.TotalSteps, "ramp_schedule steps")
	svc, ok := created.Services["aws/rds"]
	require.True(t, ok, "services: %v", created.Services)
	assert.Equal(t, "aws", svc.Provider, "service provider")
	assert.Equal(t, "rds", svc.Service, "service name")
	assert.Equal(t, 1, svc.Term, "term")
	assert.Equal(t, "all-upfront", svc.Payment, "payment")
	assert.Equal(t, 65.0, svc.Coverage, "target_coverage")
}
