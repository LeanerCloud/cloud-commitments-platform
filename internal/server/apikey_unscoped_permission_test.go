package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestHandleRequest_UnscopedAPIKey_DeniedOnAdminOwner is the regression test
// for issue #61: an API key created with zero explicit permissions used to
// inherit its owner's full permission set at request time
// (computeEffectivePermissionsFromAuthCtx), so an admin's unscoped key was
// silently admin-capable on every endpoint. It must now be denied.
//
// This drives the real, wired-together stack -- a real auth.Service over a
// mock store, behind a real api.Handler -- through HandleRequest so the
// assertion covers the actual request path (API-key auth middleware ->
// requirePermission -> ComputeEffectivePermissions), not just the service
// method in isolation. See apikey_usage_booking_test.go for why package
// server is required for this: api's mock auth service stubs the permission
// check itself, which would make the regression structurally invisible.
func TestHandleRequest_UnscopedAPIKey_DeniedOnAdminOwner(t *testing.T) {
	rawKey := "unscoped-key-owned-by-admin" // #nosec G101 -- test fixture, not a real credential
	hash := sha256.Sum256([]byte(rawKey))
	keyHash := base64.RawURLEncoding.EncodeToString(hash[:])

	key := &auth.UserAPIKey{
		ID:       "key-unscoped-1",
		UserID:   "admin-1",
		Name:     "unscoped key",
		KeyHash:  keyHash,
		IsActive: true,
		// No Permissions set: this is the exact "leave scope blank" shape
		// the issue describes. CreateAPIKey now rejects minting one, but a
		// key minted before that fix (or inserted directly, e.g. by a
		// migration) can still exist and authenticate.
	}
	user := &auth.User{
		ID:       "admin-1",
		Email:    "admin@example.com",
		Active:   true,
		GroupIDs: []string{auth.DefaultAdminGroupID},
	}
	adminGroup := &auth.Group{
		ID:          auth.DefaultAdminGroupID,
		Name:        "Administrators",
		Permissions: []auth.Permission{{Action: auth.ActionAdmin, Resource: auth.ResourceAll}},
	}

	mockStore := new(auth.MockStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })

	mockStore.On("GetAPIKeyByHash", mock.Anything, keyHash).Return(key, nil)
	mockStore.On("GetUserByID", mock.Anything, "admin-1").Return(user, nil)
	mockStore.On("GetGroup", mock.Anything, auth.DefaultAdminGroupID).Return(adminGroup, nil)
	mockStore.On("RecordAPIKeyUsage", mock.Anything, "key-unscoped-1", mock.Anything).Return(nil).Maybe()

	service := auth.NewService(auth.ServiceConfig{
		Store:           mockStore,
		SessionDuration: time.Hour,
		CSRFKey:         auth.TestCSRFKey(),
	})
	handler := api.NewHandler(api.HandlerConfig{
		AuthService:       newAuthServiceAdapter(service),
		CORSAllowedOrigin: "https://dashboard.example.com",
	})

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"x-api-key": rawKey},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Method: "GET",
				Path:   "/api/api-keys",
			},
		},
	}

	resp, err := handler.HandleRequest(context.Background(), req)

	require.NoError(t, err)
	// Pre-fix this returned 200: the key's owner is an Administrators-group
	// member, and an unscoped key inherited {admin, *} wholesale even though
	// the key itself was never granted view:api-keys.
	require.Equal(t, 403, resp.StatusCode,
		"an unscoped API key must not inherit its admin owner's permissions; body: %s", resp.Body)
}
