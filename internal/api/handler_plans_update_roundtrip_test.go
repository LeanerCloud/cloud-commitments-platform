package api

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// PUT /api/plans/{id} replaces the plan: notification_days_before, enabled and
// auto_purchase take the request's value, so an omitted one resets to 0 or false,
// while the step cap and the named service's term, payment and coverage keep their
// stored value and other stored services survive (documented on the OpenAPI
// operation). The stored plan keeps the path id and its original timestamps.
func TestUpdatePlan_OmittedFieldsResetAndStoredSettingsAreKept(t *testing.T) {
	t.Parallel()
	const planID = "12345678-1234-1234-1234-123456789abc"
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	mockAuth.On("ValidateSession", ctx, "admin-token").Return(&Session{UserID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Email: "admin@example.com"}, nil)
	mockAuth.grantAdminPurchaser()

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	cap800 := 800.0
	existing := &config.PurchasePlan{
		ID: planID, Name: "Stored plan", Enabled: true, AutoPurchase: true, NotificationDaysBefore: 5,
		MaxStepCommitment: &cap800, CreatedAt: created, UpdatedAt: updated,
		Services: map[string]config.ServiceConfig{
			"aws/rds": {Provider: "aws", Service: "rds", Enabled: true, Term: 1, Payment: "all-upfront", Coverage: 65},
			"aws/ec2": {Provider: "aws", Service: "ec2", Enabled: true, Term: 3, Payment: "no-upfront", Coverage: 40},
		},
		RampSchedule: config.PresetRampSchedules["immediate"],
	}
	mockStore.On("GetPurchasePlan", ctx, planID).Return(existing, nil)
	var stored *config.PurchasePlan
	mockStore.On("UpdatePurchasePlan", ctx, mock.AnythingOfType("*config.PurchasePlan")).Run(func(args mock.Arguments) {
		p := *args.Get(1).(*config.PurchasePlan)
		stored = &p
	}).Return(nil)

	h := &Handler{config: mockStore, auth: mockAuth}
	_, err := h.updatePlan(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer admin-token"},
		Body:    `{"name":"Renamed plan","ramp_schedule":"immediate","provider":"aws","service":"rds"}`,
	}, planID)
	require.NoError(t, err)

	require.NotNil(t, stored, "the plan was not stored")
	assert.Equal(t, planID, stored.ID, "id comes from the path")
	assert.Equal(t, "Renamed plan", stored.Name, "name")
	assert.False(t, stored.Enabled, "an omitted enabled resets to false")
	assert.False(t, stored.AutoPurchase, "an omitted auto_purchase resets to false")
	assert.Equal(t, 0, stored.NotificationDaysBefore, "an omitted notification_days_before resets to 0")
	require.NotNil(t, stored.MaxStepCommitment, "an omitted max_step_commitment keeps the stored cap")
	assert.Equal(t, 800.0, *stored.MaxStepCommitment)
	assert.True(t, stored.CreatedAt.Equal(created), "created_at is kept")
	rds := stored.Services["aws/rds"]
	assert.Equal(t, 1, rds.Term, "an omitted term keeps the stored value")
	assert.Equal(t, "all-upfront", rds.Payment, "an omitted payment keeps the stored value")
	assert.Equal(t, 65.0, rds.Coverage, "an omitted target_coverage keeps the stored value")
	ec2, ok := stored.Services["aws/ec2"]
	require.True(t, ok, "the other stored service must survive a single-service edit: %v", stored.Services)
	assert.Equal(t, 3, ec2.Term)
	assert.Equal(t, 40.0, ec2.Coverage)
}
