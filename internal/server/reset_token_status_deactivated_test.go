package server

// Issue #421: the reset-token status endpoint must agree with the confirm
// endpoint for an admin-deactivated user. Both requests go through
// Handler.HandleRequest into the real auth.Service (only the store is mocked).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/jackc/pgx/v5"
)

const deactivatedResetRawToken = "deactivated-user-reset-token"

func newResetTokenHarness(t *testing.T) (*api.Handler, *auth.MockStore) {
	t.Helper()
	mockStore := new(auth.MockStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	service := auth.NewService(auth.ServiceConfig{
		Store:           mockStore,
		SessionDuration: time.Hour,
		CSRFKey:         auth.TestCSRFKey(),
	})
	return api.NewHandler(api.HandlerConfig{AuthService: newAuthServiceAdapter(service)}), mockStore
}

func deactivatedResetUser() *auth.User {
	expiry := time.Now().Add(time.Hour)
	deactivatedAt := time.Now().Add(-time.Hour)
	return &auth.User{
		ID:                  "deactivated-user",
		Email:               "deactivated@example.com",
		Active:              false,
		DeactivatedAt:       &deactivatedAt,
		PasswordResetToken:  hashSessionTokenForTest(deactivatedResetRawToken),
		PasswordResetExpiry: &expiry,
	}
}

func resetStatusRequest(token string) *events.LambdaFunctionURLRequest {
	return &events.LambdaFunctionURLRequest{
		QueryStringParameters: map[string]string{"token": token},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Method: "GET",
				Path:   "/api/auth/reset-password/status",
			},
		},
	}
}

func TestHandleRequest_ResetPasswordStatus_DeactivatedUserLooksLikeUnknownToken(t *testing.T) {
	ctx := context.Background()
	handler, mockStore := newResetTokenHarness(t)

	mockStore.On("GetUserByResetToken", mock.Anything, hashSessionTokenForTest(deactivatedResetRawToken)).
		Return(deactivatedResetUser(), nil).Once()
	mockStore.On("GetUserByResetToken", mock.Anything, hashSessionTokenForTest("unknown-token")).
		Return((*auth.User)(nil), pgx.ErrNoRows).Once()

	deactivated, err := handler.HandleRequest(ctx, resetStatusRequest(deactivatedResetRawToken))
	require.NoError(t, err)
	unknown, err := handler.HandleRequest(ctx, resetStatusRequest("unknown-token"))
	require.NoError(t, err)

	require.Equal(t, 200, unknown.StatusCode, "body: %s", unknown.Body)
	require.Equal(t, 200, deactivated.StatusCode, "body: %s", deactivated.Body)
	var body map[string]string
	require.NoError(t, json.Unmarshal([]byte(deactivated.Body), &body))
	assert.Equal(t, "used", body["state"])
	assert.Equal(t, "reset", body["flow"])
	assert.Equal(t, unknown.StatusCode, deactivated.StatusCode)
	assert.Equal(t, unknown.Headers, deactivated.Headers,
		"deactivated token must be indistinguishable from an unknown token")
	assert.JSONEq(t, unknown.Body, deactivated.Body,
		"deactivated token must be indistinguishable from an unknown token")
}

func TestHandleRequest_ConfirmPasswordReset_DeactivatedUserRefused(t *testing.T) {
	ctx := context.Background()
	handler, mockStore := newResetTokenHarness(t)

	user := deactivatedResetUser()
	mockStore.On("GetUserByResetToken", mock.Anything, hashSessionTokenForTest(deactivatedResetRawToken)).
		Return(user, nil).Once()
	mockStore.On("ConsumePasswordResetToken", mock.Anything, user.ID, user.PasswordResetToken).
		Return(nil).Once()

	body, err := json.Marshal(map[string]string{
		"token":        deactivatedResetRawToken,
		"new_password": base64.StdEncoding.EncodeToString([]byte("Str0ng-Passw0rd-For-Test!")),
	})
	require.NoError(t, err)
	resp, err := handler.HandleRequest(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    string(body),
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Method: "POST",
				Path:   "/api/auth/reset-password",
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 403, resp.StatusCode, "body: %s", resp.Body)
}
