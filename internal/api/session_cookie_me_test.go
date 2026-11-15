package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type csrfAuth struct{ *MockAuthService }

func (a csrfAuth) CSRFTokenForSession(_ context.Context, tok string) (string, error) {
	return "csrf-of-" + tok, nil
}

type failingCSRFAuth struct{ *MockAuthService }

func (failingCSRFAuth) CSRFTokenForSession(context.Context, string) (string, error) {
	return "", errors.New("csrf derivation failed")
}

func TestAuthMeFailsWhenCSRFTokenCannotBeIssued(t *testing.T) {
	t.Parallel()
	m := new(MockAuthService)
	m.On("ValidateSession", mock.Anything, "tok-A").Return(&Session{UserID: "uA", Email: "a@example.com"}, nil)
	m.On("GetUser", mock.Anything, "uA").Return(&User{ID: "uA", Email: "a@example.com"}, nil)
	h := &Handler{auth: failingCSRFAuth{m}}

	resp, err := h.HandleRequest(context.Background(), cookieReq("GET", "/api/auth/me", "", nil, cookieName+"=tok-A"))
	require.NoError(t, err)
	assert.Equal(t, 500, resp.StatusCode)
	assert.NotContains(t, resp.Body, "csrf_token")
}

func TestAuthMeReturnsCSRFTokenForCookieSession(t *testing.T) {
	t.Parallel()
	m := new(MockAuthService)
	m.On("ValidateSession", mock.Anything, "tok-A").Return(&Session{UserID: "uA", Email: "a@example.com"}, nil)
	m.On("ValidateSession", mock.Anything, "garbled").Return(nil, errors.New("invalid session"))
	m.On("GetUser", mock.Anything, "uA").Return(&User{ID: "uA", Email: "a@example.com"}, nil)
	h := &Handler{auth: csrfAuth{m}}

	resp, err := h.HandleRequest(context.Background(), cookieReq("GET", "/api/auth/me", "", nil, cookieName+"=tok-A"))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, resp.Body)
	var got CurrentUserResponse
	require.NoError(t, json.Unmarshal([]byte(resp.Body), &got))
	assert.Equal(t, "uA", got.ID)
	assert.Equal(t, "csrf-of-tok-A", got.CSRFToken)

	resp, err = h.HandleRequest(context.Background(), cookieReq("GET", "/api/auth/me", "", nil, cookieName+"=garbled"))
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}
