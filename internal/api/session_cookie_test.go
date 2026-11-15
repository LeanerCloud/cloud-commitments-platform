package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
)

const cookieName = "__Host-cudly_session"

// csrfAuth adds the optional CSRFTokenForSession the production adapter has.
func cookieReq(method, path, body string, headers map[string]string, cookies ...string) *events.LambdaFunctionURLRequest {
	if body != "" {
		merged := map[string]string{"content-type": "application/json"}
		for k, v := range headers {
			merged[k] = v
		}
		headers = merged
	}
	return &events.LambdaFunctionURLRequest{
		Headers: headers,
		Cookies: cookies,
		Body:    body,
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: method, Path: path, SourceIP: "203.0.113.9"},
		},
	}
}

func parseSetCookie(t *testing.T, raw string) *http.Cookie {
	t.Helper()
	c, err := http.ParseSetCookie(raw)
	require.NoError(t, err)
	return c
}

func loginResp(token string, ttl time.Duration) *LoginResponse {
	return &LoginResponse{
		Token: token, ExpiresAt: time.Now().Add(ttl).UTC().Format(time.RFC3339),
		User: &UserInfo{ID: "u1", Email: "u@example.com"}, CSRFToken: "csrf",
	}
}

func loginBody(t *testing.T, m map[string]string) string {
	t.Helper()
	if p, ok := m["password"]; ok {
		m["password"] = base64.StdEncoding.EncodeToString([]byte(p))
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

func assertSessionCookie(t *testing.T, resp *events.LambdaFunctionURLResponse, token string, ttl time.Duration) {
	t.Helper()
	require.Len(t, resp.Cookies, 1, resp.Body)
	c := parseSetCookie(t, resp.Cookies[0])
	assert.Equal(t, cookieName, c.Name)
	assert.Equal(t, token, c.Value)
	assert.True(t, c.HttpOnly)
	assert.True(t, c.Secure)
	assert.Equal(t, http.SameSiteStrictMode, c.SameSite)
	assert.Equal(t, "/", c.Path)
	assert.Empty(t, c.Domain, "a Domain attribute is forbidden by the __Host- prefix")
	assert.InDelta(t, ttl.Seconds(), float64(c.MaxAge), 5)
}
func TestLoginSetsSessionCookie(t *testing.T) {
	t.Parallel()
	m := new(MockAuthService)
	m.On("Login", mock.Anything, LoginRequest{Email: "u@example.com", Password: "pw"}).Return(loginResp("tok-1", time.Hour), nil)
	h := &Handler{auth: m, rateLimiter: NewInMemoryRateLimiter()}

	resp, err := h.HandleRequest(context.Background(),
		cookieReq("POST", "/api/auth/login", loginBody(t, map[string]string{"email": "u@example.com", "password": "pw"}), nil))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, resp.Body)
	assertSessionCookie(t, resp, "tok-1", time.Hour)
	assert.Contains(t, resp.Body, `"token":"tok-1"`, "CLI/MCP clients still read the body token")
}

func TestMFACompletionLegSetsSessionCookie(t *testing.T) {
	t.Parallel()
	m := new(MockAuthService)
	m.On("Login", mock.Anything, LoginRequest{Email: "u@example.com", MFAToken: "mt", MFACode: "123456"}).Return(loginResp("tok-2", 2*time.Hour), nil)
	h := &Handler{auth: m, rateLimiter: NewInMemoryRateLimiter()}

	resp, err := h.HandleRequest(context.Background(),
		cookieReq("POST", "/api/auth/login", loginBody(t, map[string]string{"email": "u@example.com", "mfa_token": "mt", "mfa_code": "123456"}), nil))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, resp.Body)
	assertSessionCookie(t, resp, "tok-2", 2*time.Hour)
}

func TestLoginWithoutSessionSetsNoCookie(t *testing.T) {
	t.Parallel()
	m := new(MockAuthService)
	m.On("Login", mock.Anything, mock.Anything).
		Return(nil, &auth.MFARequiredError{Token: "mt", ExpiresAt: time.Now().Add(time.Minute)}).Once()
	m.On("Login", mock.Anything, mock.Anything).Return(loginResp("tok", -time.Minute), nil).Once()
	m.On("Login", mock.Anything, mock.Anything).Return(&LoginResponse{Token: "tok", ExpiresAt: "garbage"}, nil).Once()
	h := &Handler{auth: m, rateLimiter: NewInMemoryRateLimiter()}
	body := loginBody(t, map[string]string{"email": "u@example.com", "password": "pw"})

	for _, want := range []int{401, 500, 500} {
		resp, err := h.HandleRequest(context.Background(), cookieReq("POST", "/api/auth/login", body, nil))
		require.NoError(t, err)
		assert.Equal(t, want, resp.StatusCode, resp.Body)
		assert.Empty(t, resp.Cookies)
	}
}
func TestWarnIfCookieCannotWork(t *testing.T) {
	t.Parallel()
	assert.NotPanics(t, func() {
		for _, o := range []string{"", "%zz", "https://d.example.com", "http://localhost:3000", "http://d.example.com"} {
			warnIfCookieCannotWork(o)
		}
	})
}

// A failed server-side logout must still expire the httpOnly cookie: the
// frontend cannot clear it, so leaving it would strand a dead session.
func TestFailedLogoutStillClearsCookie(t *testing.T) {
	t.Parallel()
	m := new(MockAuthService)
	m.On("ValidateSession", mock.Anything, "tok-A").Return(&Session{UserID: "uA"}, nil)
	m.On("ValidateCSRFToken", mock.Anything, "tok-A", "good").Return(nil)
	m.On("Logout", mock.Anything, "tok-A").Return(errors.New("store down"))
	h := &Handler{auth: m}

	resp, err := h.HandleRequest(context.Background(),
		cookieReq("POST", "/api/auth/logout", "", map[string]string{"x-csrf-token": "good"}, cookieName+"=tok-A"))
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
	require.Len(t, resp.Cookies, 1)
	assert.Equal(t, -1, parseSetCookie(t, resp.Cookies[0]).MaxAge)
}

// A session the middleware rejects (revoked or expired) gets its stale cookie
// expired; a request that sent no cookie gets no Set-Cookie.
func TestRejectedSessionCookieIsCleared(t *testing.T) {
	t.Parallel()
	m := new(MockAuthService)
	m.On("ValidateSession", mock.Anything, "stale").Return(nil, errors.New("expired"))
	h := &Handler{auth: m}

	resp, err := h.HandleRequest(context.Background(), cookieReq("GET", "/api/auth/me", "", nil, cookieName+"=stale"))
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
	require.Len(t, resp.Cookies, 1)
	assert.Equal(t, -1, parseSetCookie(t, resp.Cookies[0]).MaxAge)

	resp, err = h.HandleRequest(context.Background(), cookieReq("GET", "/api/auth/me", "", nil))
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
	assert.Empty(t, resp.Cookies)
}

func TestSetupAdminSetsSessionCookie(t *testing.T) {
	t.Parallel()
	m := new(MockAuthService)
	m.On("SetupAdmin", mock.Anything, SetupAdminRequest{Email: "a@example.com", Password: "pw"}).Return(loginResp("tok-3", time.Hour), nil)
	h := &Handler{auth: m, rateLimiter: NewInMemoryRateLimiter()}

	resp, err := h.HandleRequest(context.Background(),
		cookieReq("POST", "/api/auth/setup-admin", loginBody(t, map[string]string{"email": "a@example.com", "password": "pw"}), nil))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, resp.Body)
	assertSessionCookie(t, resp, "tok-3", time.Hour)
}

// A form or text/plain POST needs no CORS preflight; accepting one would allow
// login CSRF from a same-site page.
func TestSessionIssuingRoutesRequireJSON(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/auth/login", "/api/auth/setup-admin"} {
		for _, ct := range []string{"application/x-www-form-urlencoded", "text/plain", "text/plain;charset=UTF-8"} {
			m := new(MockAuthService) // no Login/SetupAdmin expectation: reaching the service would panic
			h := &Handler{auth: m, rateLimiter: NewInMemoryRateLimiter()}
			body := loginBody(t, map[string]string{"email": "u@example.com", "password": "pw"})

			resp, err := h.HandleRequest(context.Background(), cookieReq("POST", path, body, map[string]string{"content-type": ct}))
			require.NoError(t, err)
			assert.Contains(t, []int{400, 415}, resp.StatusCode, "%s %s", path, ct)
			assert.Empty(t, resp.Cookies)
		}
	}
}

func TestLoginAcceptsJSONWithCharset(t *testing.T) {
	t.Parallel()
	m := new(MockAuthService)
	m.On("Login", mock.Anything, mock.Anything).Return(loginResp("tok-4", time.Hour), nil)
	h := &Handler{auth: m, rateLimiter: NewInMemoryRateLimiter()}

	resp, err := h.HandleRequest(context.Background(), cookieReq("POST", "/api/auth/login",
		loginBody(t, map[string]string{"email": "u@example.com", "password": "pw"}), map[string]string{"content-type": "application/json; charset=utf-8"}))
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, resp.Body)
}
