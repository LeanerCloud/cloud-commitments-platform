package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Fixtures are hand-built plans, not real configuration (issue #1746).

const boundResponsePlanID = "12345678-1234-1234-1234-123456789abc"

func boundResponseHandler(t *testing.T) (*Handler, *MockConfigStore) {
	t.Helper()
	store := new(MockConfigStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	auth := new(MockAuthService)
	t.Cleanup(func() { auth.AssertExpectations(t) })
	auth.On("ValidateSession", mock.Anything, "admin-token").Return(&Session{
		UserID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Email: "admin@example.com",
	}, nil)
	auth.grantAdmin()
	return &Handler{config: store, auth: auth}, store
}

func boundResponseRequest(body string) *events.LambdaFunctionURLRequest {
	return &events.LambdaFunctionURLRequest{Headers: map[string]string{"Authorization": "Bearer admin-token"}, Body: body}
}

func boundStoredPlan() *config.PurchasePlan {
	p := boundResponsePlan(3)
	p.ID = boundResponsePlanID
	p.Enabled = true
	p.RampSchedule = config.RampSchedule{Type: "immediate", PercentPerStep: 100, TotalSteps: 1, AllocationMode: config.RampAllocationBoundGroups}
	return p
}

// assertCountedResponse checks the JSON a client receives and that the plan the
// store handled still has its identities.
func assertCountedResponse(t *testing.T, result any, handled *config.PurchasePlan) {
	t.Helper()
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "bound_identities")
	assert.Contains(t, string(raw), `"bound_count":3`)
	require.NotNil(t, handled)
	g := handled.Services["aws/ec2#g1"]
	assert.Len(t, g.BoundIdentities, 3)
	assert.Zero(t, g.BoundCount)
}

func TestGetPlan_ReturnsCountedCopy(t *testing.T) {
	h, store := boundResponseHandler(t)
	stored := boundStoredPlan()
	store.On("GetPurchasePlan", mock.Anything, boundResponsePlanID).Return(stored, nil)
	result, err := h.getPlan(context.Background(), boundResponseRequest(""), boundResponsePlanID)
	require.NoError(t, err)
	assertCountedResponse(t, result, stored)
}

func TestUpdatePlan_ReturnsCountedCopy(t *testing.T) {
	h, store := boundResponseHandler(t)
	var saved *config.PurchasePlan
	store.On("GetPurchasePlan", mock.Anything, boundResponsePlanID).Return(boundStoredPlan(), nil)
	store.On("UpdatePurchasePlan", mock.Anything, mock.AnythingOfType("*config.PurchasePlan")).
		Run(func(args mock.Arguments) { saved = args.Get(1).(*config.PurchasePlan) }).Return(nil)
	result, err := h.updatePlan(context.Background(), boundResponseRequest(`{"name":"renamed","ramp_schedule":"immediate"}`), boundResponsePlanID)
	require.NoError(t, err)
	assertCountedResponse(t, result, saved)
}

func TestPatchPlan_ReturnsCountedCopy(t *testing.T) {
	h, store := boundResponseHandler(t)
	var saved *config.PurchasePlan
	store.On("GetPurchasePlan", mock.Anything, boundResponsePlanID).Return(boundStoredPlan(), nil)
	store.On("UpdatePurchasePlan", mock.Anything, mock.AnythingOfType("*config.PurchasePlan")).
		Run(func(args mock.Arguments) { saved = args.Get(1).(*config.PurchasePlan) }).Return(nil)
	result, err := h.patchPlan(context.Background(), boundResponseRequest(`{"name":"renamed"}`), boundResponsePlanID)
	require.NoError(t, err)
	assertCountedResponse(t, result, saved)
}

// The create body cannot carry bound groups, so the store stands in for a
// plan that comes back from it with them.
func TestCreatePlan_ReturnsCountedCopy(t *testing.T) {
	h, store := boundResponseHandler(t)
	account := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	var created *config.PurchasePlan
	store.On("CreatePurchasePlanWithAccounts", mock.Anything, mock.AnythingOfType("*config.PurchasePlan"), []string{account}, mock.Anything).
		Run(func(args mock.Arguments) {
			created = args.Get(1).(*config.PurchasePlan)
			created.Services = boundStoredPlan().Services
		}).Return(nil)
	store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		return &config.CloudAccount{ID: id, Name: "test-aws", Provider: "aws"}, nil
	}
	body := `{"name":"New","enabled":true,"provider":"aws","service":"ec2","target_accounts":["` + account + `"]}`
	result, err := h.createPlan(context.Background(), boundResponseRequest(body))
	require.NoError(t, err)
	assertCountedResponse(t, result, created)
}
