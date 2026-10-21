package api

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/archera"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Deployment-wide actions require an UNCONSTRAINED grant on top of an
// unrestricted account scope. These tests build the principal with
// grantPermissionsScoped, whose constraint decision is the real matcher; a
// stub that always allows (allowConstraintChecks) would prove nothing here.

func capOnly() *auth.PermissionConstraints {
	return &auth.PermissionConstraints{MaxPurchaseAmount: 100}
}

func providerOnly() *auth.PermissionConstraints {
	return &auth.PermissionConstraints{Providers: []string{"aws"}}
}

func isGlobalSet(sets []auth.PermissionConstraints) bool {
	return len(sets) == 1 && sets[0].Global && !sets[0].StrictScope
}

func TestRefreshRecommendations_ConstrainedViewerRefused(t *testing.T) {
	t.Setenv("SCHEDULER_LAMBDA_ARN", "")
	for name, held := range map[string]*auth.PermissionConstraints{
		"account": {AccountIDs: []string{scopedInAccount}},
		"cap":     capOnly(),
		"service": {Services: []string{"ec2"}},
		"region":  {Regions: []string{"us-east-1"}},
	} {
		for scopeName, accounts := range map[string][]string{"group scope all": nil, "group scope star": {"*"}} {
			t.Run(name+"/"+scopeName, func(t *testing.T) {
				f := newRefreshFixture(t, []auth.Permission{{Action: auth.ActionView, Resource: auth.ResourceRecommendations, Constraints: held}}, accounts)

				res, err := f.refresh(scopedRequest(""))

				require.Error(t, err, "got response %v", res)
				ce, ok := IsClientError(err)
				require.True(t, ok, "got %v", err)
				assert.Equal(t, 403, ce.code)
				f.assertNothingStarted(t)
			})
		}
	}
}

func TestRefreshRecommendations_UserAPIKeyUsesGlobalSetAndKeyID(t *testing.T) {
	t.Setenv("SCHEDULER_LAMBDA_ARN", "")
	for _, allowed := range []bool{false, true} {
		mockAuth := new(MockAuthService)
		mockAuth.On("HasAPIKeyPermissionAPI", mock.Anything, "user-key", "view", "recommendations").
			Return("u2", "key-1", true, nil)
		mockAuth.On("GetAllowedAccountsAPI", mock.Anything, "u2").Return([]string(nil), nil)
		mockAuth.On("HasAPIKeyPermissionForConstraintsAPI", mock.Anything, "key-1", "u2", "view", "recommendations",
			mock.MatchedBy(isGlobalSet)).Return(allowed, nil).Once()
		f := newRefreshFixtureWithAuth(t, mockAuth)
		ctx := contextWithPrincipal(context.Background(), &Principal{Kind: PrincipalUserAPIKey, UserID: "u2", APIKeyID: "key-1"})
		req := &events.LambdaFunctionURLRequest{Headers: map[string]string{"x-api-key": "user-key"}}

		res, err := f.handler.postRefreshRecommendations(ctx, req)

		mockAuth.AssertExpectations(t)
		if allowed {
			require.NoError(t, err)
			assert.IsType(t, &RefreshResponse{}, res)
			f.store.AssertNumberOfCalls(t, "MarkCollectionStarted", 1)
			continue
		}
		require.Error(t, err, "got response %v", res)
		ce, ok := IsClientError(err)
		require.True(t, ok)
		assert.Equal(t, 403, ce.code)
		f.assertNothingStarted(t)
	}
}

func TestRefreshRecommendations_AdminKeyStillRefreshesWithConstrainedGrants(t *testing.T) {
	t.Setenv("SCHEDULER_LAMBDA_ARN", "")
	f := newRefreshFixture(t, []auth.Permission{{Action: auth.ActionView, Resource: auth.ResourceRecommendations, Constraints: capOnly()}}, nil)
	f.handler.apiKey = "test-key"

	res, err := f.refresh(&events.LambdaFunctionURLRequest{Headers: map[string]string{"X-API-Key": "test-key"}})

	require.NoError(t, err)
	assert.IsType(t, &RefreshResponse{}, res)
}

const riExchangeConfigBody = `{"mode":"manual","utilization_threshold":50,"lookback_days":30}`

func capOnlyConfigWriter(t *testing.T) (*Handler, *MockConfigStore) {
	t.Helper()
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", mock.Anything, scopedToken).Return(&Session{UserID: scopedUserID}, nil).Maybe()
	mockAuth.grantPermissionsScoped([]auth.Permission{{Action: auth.ActionUpdate, Resource: auth.ResourceConfig, Constraints: capOnly()}}, nil)
	store := new(MockConfigStore)
	return &Handler{config: store, auth: mockAuth}, store
}

func TestConfigWrites_CapOnlyHolderRefused(t *testing.T) {
	writes := map[string]func(*Handler) (any, error){
		"global config": func(h *Handler) (any, error) {
			return h.updateConfig(context.Background(), scopedRequest(`{}`))
		},
		"service config": func(h *Handler) (any, error) {
			return h.updateServiceConfig(context.Background(), scopedRequest(serviceConfigBody), "aws/ec2")
		},
		"ri-exchange config": func(h *Handler) (any, error) {
			return h.updateRIExchangeConfig(context.Background(), scopedRequest(riExchangeConfigBody))
		},
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			h, store := capOnlyConfigWriter(t)

			res, err := write(h)

			require.Error(t, err, "got response %v", res)
			ce, ok := IsClientError(err)
			require.True(t, ok, "got %v", err)
			assert.Equal(t, 403, ce.code)
			assert.Empty(t, store.Calls, "a constrained holder must not reach the store")
		})
	}
}

// The scope check stays after body validation, so a malformed body is still 400.
func TestUpdateRIExchangeConfig_ConstrainedMalformedBodyStill400(t *testing.T) {
	h, _ := capOnlyConfigWriter(t)

	_, err := h.updateRIExchangeConfig(context.Background(), scopedRequest(`{"mode":"bogus"}`))

	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 400, ce.code)
}

func TestUpdateConfig_UserAPIKeyUsesGlobalSetAndKeyID(t *testing.T) {
	mockAuth := new(MockAuthService)
	mockAuth.On("HasAPIKeyPermissionAPI", mock.Anything, "user-key", "update", "config").Return("u2", "key-1", true, nil)
	mockAuth.On("GetAllowedAccountsAPI", mock.Anything, "u2").Return([]string(nil), nil)
	mockAuth.On("HasAPIKeyPermissionForConstraintsAPI", mock.Anything, "key-1", "u2", "update", "config",
		mock.MatchedBy(isGlobalSet)).Return(false, nil).Once()
	store := new(MockConfigStore)
	h := &Handler{config: store, auth: mockAuth}

	ctx := contextWithPrincipal(context.Background(), &Principal{Kind: PrincipalUserAPIKey, UserID: "u2", APIKeyID: "key-1"})

	_, err := h.updateConfig(ctx, &events.LambdaFunctionURLRequest{Headers: map[string]string{"x-api-key": "user-key"}, Body: `{}`})

	mockAuth.AssertExpectations(t)
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 403, ce.code)
	assert.Empty(t, store.Calls)
}

// Reads of the global configuration stay open to a constrained viewer.
func TestGetConfig_ConstrainedViewerStillReads(t *testing.T) {
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", mock.Anything, scopedToken).Return(&Session{UserID: scopedUserID}, nil).Maybe()
	mockAuth.grantPermissionsScoped([]auth.Permission{{Action: auth.ActionView, Resource: auth.ResourceConfig, Constraints: providerOnly()}}, nil)
	store := new(MockConfigStore)
	store.On("GetGlobalConfig", mock.Anything).Return(&config.GlobalConfig{}, nil)
	store.On("ListServiceConfigs", mock.Anything).Return([]config.ServiceConfig{}, nil)
	h := &Handler{config: store, auth: mockAuth}

	res, err := NewRouter(h).Route(context.Background(), "GET", "/api/config", scopedRequest(""))

	require.NoError(t, err)
	assert.IsType(t, &ConfigResponse{}, res)
}

func TestInsuranceStatus_ConstrainedViewerGets404(t *testing.T) {
	m := new(MockAuthService)
	m.On("HasPermissionAPI", mock.Anything, "u1", "view", "recommendations").Return(true, nil)
	m.On("GetAllowedAccountsAPI", mock.Anything, "u1").Return([]string(nil), nil)
	m.On("HasPermissionForConstraintsAPI", mock.Anything, "u1", "view", "recommendations", mock.MatchedBy(isGlobalSet)).
		Return(false, nil)
	h := &Handler{auth: m, insurance: insuranceProvider(t, archera.Settings{})}

	_, err := h.getInsuranceStatus(sessionPrincipalCtx(), &events.LambdaFunctionURLRequest{})

	assert.ErrorIs(t, err, errNotFound)
}
