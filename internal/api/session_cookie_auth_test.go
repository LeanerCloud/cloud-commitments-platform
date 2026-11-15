package api

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestCookieAuthenticatedRequestStillRequiresCSRF(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		csrf    string
		want    int
		cookies bool
	}{
		{"missing header", "", 403, true},
		{"wrong header", "wrong", 403, true},
		{"valid header", "good", 200, true},
		{"no cookie, valid header", "good", 401, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := new(MockAuthService)
			m.On("ValidateSession", mock.Anything, "tok-A").Return(&Session{UserID: "uA", Email: "a@example.com"}, nil).Maybe()
			m.On("ValidateCSRFToken", mock.Anything, "tok-A", "good").Return(nil).Maybe()
			m.On("ValidateCSRFToken", mock.Anything, "tok-A", mock.Anything).Return(errors.New("invalid CSRF token")).Maybe()
			m.On("Logout", mock.Anything, "tok-A").Return(nil).Maybe()
			h := &Handler{auth: m}
			headers := map[string]string{}
			if tc.csrf != "" {
				headers["x-csrf-token"] = tc.csrf
			}
			var cookies []string
			if tc.cookies {
				cookies = []string{cookieName + "=tok-A"}
			}

			resp, err := h.HandleRequest(context.Background(), cookieReq("POST", "/api/auth/logout", "", headers, cookies...))
			require.NoError(t, err)
			assert.Equal(t, tc.want, resp.StatusCode, resp.Body)
			if tc.want == 200 {
				require.Len(t, resp.Cookies, 1)
				c := parseSetCookie(t, resp.Cookies[0])
				assert.Equal(t, cookieName, c.Name)
				assert.Empty(t, c.Value)
				assert.Equal(t, -1, c.MaxAge, "Max-Age=0 expires the cookie")
				assert.True(t, c.Secure && c.HttpOnly)
				assert.Equal(t, "/", c.Path)
			} else {
				assert.Empty(t, resp.Cookies)
				m.AssertNotCalled(t, "Logout", mock.Anything, mock.Anything)
			}
		})
	}
}

func TestCookieFromHTTPServerCookieHeader(t *testing.T) {
	t.Parallel()
	m := new(MockAuthService)
	m.On("ValidateSession", mock.Anything, "tok-A").Return(&Session{UserID: "uA", Email: "a@example.com"}, nil)
	m.On("ValidateCSRFToken", mock.Anything, "tok-A", "good").Return(nil)
	m.On("Logout", mock.Anything, "tok-A").Return(nil)
	h := &Handler{auth: m}

	resp, err := h.HandleRequest(context.Background(), cookieReq("POST", "/api/auth/logout", "",
		map[string]string{"cookie": "theme=dark; " + cookieName + "=tok-A", "x-csrf-token": "good"}))
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, resp.Body)
}

func TestSessionCookieCrossUserCSRFRefused(t *testing.T) {
	t.Parallel()
	m := new(MockAuthService)
	m.On("ValidateSession", mock.Anything, "tok-A").Return(&Session{UserID: "uA"}, nil)
	m.On("ValidateCSRFToken", mock.Anything, "tok-A", "csrf-of-tok-B").Return(errors.New("invalid CSRF token"))
	h := &Handler{auth: m}

	resp, err := h.HandleRequest(context.Background(), cookieReq("POST", "/api/auth/logout", "",
		map[string]string{"x-csrf-token": "csrf-of-tok-B"}, cookieName+"=tok-A"))
	require.NoError(t, err)
	assert.Equal(t, 403, resp.StatusCode)
	m.AssertNotCalled(t, "Logout", mock.Anything, mock.Anything)
}

func TestExtractBearerTokenSources(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    string
		cookies []string
	}{
		{"cookie only", nil, "ck", []string{cookieName + "=ck"}},
		{"bearer beats cookie", map[string]string{"authorization": "Bearer hd"}, "hd", []string{cookieName + "=ck"}},
		{"x-authorization beats cookie", map[string]string{"x-authorization": "Bearer hd"}, "hd", []string{cookieName + "=ck"}},
		{"sigv4 authorization falls back to cookie", map[string]string{"authorization": "AWS4-HMAC-SHA256 Credential=x"}, "ck", []string{cookieName + "=ck"}},
		{"api key ignores cookie", map[string]string{"x-api-key": "k"}, "", []string{cookieName + "=ck"}},
		{"empty value", nil, "", []string{cookieName + "="}},
		{"garbled cookie", nil, "", []string{";;==", "\x00\xff"}},
		{"other cookie name", nil, "", []string{"cudly_session=ck", "x" + cookieName + "=ck"}},
		{"no cookies", nil, "", nil},
		{"malformed sibling cookie before ours", nil, "ck", []string{`a="q"x; ` + cookieName + "=ck"}},
		{"malformed sibling cookie after ours", nil, "ck", []string{cookieName + `=ck; b="q"x`}},
		{"malformed sibling in a separate cookie", nil, "ck", []string{`a="q"x`, cookieName + "=ck"}},
		{"picks named cookie among many", nil, "ck", []string{"a=1; " + cookieName + "=ck; b=2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, h.extractBearerToken(&events.LambdaFunctionURLRequest{Headers: tc.headers, Cookies: tc.cookies}))
		})
	}
}

func TestAPIKeyRequestNeverUsesCookie(t *testing.T) {
	t.Parallel()
	m := new(MockAuthService) // no ValidateSession expectation: a session lookup would panic
	h := &Handler{auth: m, apiKey: "admin-key"}

	p, err := h.authenticatePrincipal(context.Background(),
		cookieReq("POST", "/api/x", "", map[string]string{"x-api-key": "admin-key"}, cookieName+"=tok-A"))
	require.NoError(t, err)
	assert.Equal(t, PrincipalAdminAPIKey, p.Kind)
	assert.Nil(t, p.Session)
	assert.True(t, h.apiKeyBypassCSRF(context.Background(), cookieReq("POST", "/api/x", "", map[string]string{"x-api-key": "admin-key"}, cookieName+"=tok-A")))
}

// GET and HEAD skip CSRF, so a same-site image tag must not be able to log the
// victim out by hitting the logout path.
func TestLogoutPathNonPostNeverClearsCookie(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"GET", "HEAD"} {
		m := new(MockAuthService)
		m.On("ValidateSession", mock.Anything, "tok-A").Return(&Session{UserID: "uA"}, nil).Maybe()
		h := &Handler{auth: m}

		resp, err := h.HandleRequest(context.Background(), cookieReq(method, "/api/auth/logout", "", nil, cookieName+"=tok-A"))
		require.NoError(t, err)
		assert.Empty(t, resp.Cookies, method)
		m.AssertNotCalled(t, "Logout", mock.Anything, mock.Anything)
	}
}

// A rejected credential that is not the cookie must not wipe a valid cookie.
func TestRejectedOtherCredentialKeepsCookie(t *testing.T) {
	t.Parallel()
	for _, hdrs := range []map[string]string{{"x-api-key": "bad"}, {"authorization": "Bearer nope"}} {
		m := new(MockAuthService)
		m.On("ValidateUserAPIKeyAPI", mock.Anything, "bad").Return(nil, nil, errors.New("invalid key")).Maybe()
		m.On("ValidateSession", mock.Anything, "nope").Return(nil, errors.New("invalid")).Maybe()
		h := &Handler{auth: m}

		resp, err := h.HandleRequest(context.Background(), cookieReq("GET", "/api/auth/me", "", hdrs, cookieName+"=tok-A"))
		require.NoError(t, err)
		assert.Equal(t, 401, resp.StatusCode, "%v", hdrs)
		assert.Empty(t, resp.Cookies, "%v", hdrs)
	}
}
