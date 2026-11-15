package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/aws/aws-lambda-go/events"
)

// sessionCookieName uses the __Host- prefix: browsers only accept it with
// Secure, Path=/ and no Domain, so a sibling subdomain cannot plant or
// shadow the session.
const sessionCookieName = "__Host-cudly_session"

// cookieResponse lets a route handler attach Set-Cookie values to its body;
// buildResponse unwraps it into the response's Cookies field.
type cookieResponse struct {
	body    any
	cookies []string
}

// csrfTokenIssuer is implemented by the production auth adapter. It is
// optional so GET /api/auth/me stays usable with other AuthServiceInterface
// implementations (the field is omitted then).
type csrfTokenIssuer interface {
	CSRFTokenForSession(ctx context.Context, sessionToken string) (string, error)
}

func setSessionCookie(token string, expiresAt time.Time) (string, error) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if token == "" || maxAge <= 0 {
		return "", fmt.Errorf("session cookie needs a token and a future expiry")
	}
	return sessionCookie(token, maxAge), nil
}

func clearSessionCookie() string {
	return sessionCookie("", -1)
}

func sessionCookie(value string, maxAge int) string {
	c := &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
	return c.String()
}

// readSessionCookie returns the session cookie value from the Lambda
// `cookies` field or, on the HTTP server path, the `cookie` header. It uses
// net/http's tolerant reader, which skips malformed cookies, so one bad
// parent-domain cookie cannot discard ours.
func readSessionCookie(req *events.LambdaFunctionURLRequest) string {
	values := append([]string(nil), req.Cookies...)
	if h := req.Headers["cookie"]; h != "" {
		values = append(values, h)
	}
	if len(values) == 0 {
		return ""
	}
	c, err := (&http.Request{Header: http.Header{"Cookie": values}}).Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// withLoginCookie attaches the session cookie to a successful login body. A
// token whose expiry cannot be turned into a cookie is a server fault.
func withLoginCookie(resp *LoginResponse) (any, error) {
	expiresAt, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse session expiry: %w", err)
	}
	cookie, err := setSessionCookie(resp.Token, expiresAt)
	if err != nil {
		return nil, err
	}
	return &cookieResponse{body: resp, cookies: []string{cookie}}, nil
}

// warnIfCookieCannotWork logs when the configured dashboard origin is plain
// HTTP on a non-loopback host: browsers reject Secure cookies there, so
// login would succeed but no session would stick.
func warnIfCookieCannotWork(origin string) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return
	}
	logging.Errorf("SECURITY WARNING: CORS_ALLOWED_ORIGIN %q is plain HTTP; browsers reject the Secure %s cookie there. Serve the dashboard over HTTPS.", origin, sessionCookieName)
}

const (
	loginPath      = "/api/auth/login"
	setupAdminPath = "/api/auth/setup-admin"
	logoutPath     = "/api/auth/logout"
)

// attachLoginCookie adds the session Set-Cookie to a completed login or
// admin setup (*LoginResponse, not an MFA challenge error). It runs on the
// Handler path so Router.Route keeps returning the plain bodies.
func attachLoginCookie(path string, result any) (any, error) {
	if path != loginPath && path != setupAdminPath {
		return result, nil
	}
	if resp, ok := result.(*LoginResponse); ok {
		return withLoginCookie(resp)
	}
	return result, nil
}

// clearCookieOn401 expires a session cookie the server just refused: the
// frontend cannot clear an httpOnly cookie itself. It only fires when the
// cookie was the credential actually tried; a bad API key or bearer header
// sent beside a valid cookie must not log that session out.
func (h *Handler) clearCookieOn401(req *events.LambdaFunctionURLRequest, body any) any {
	cookie := readSessionCookie(req)
	if cookie == "" || h.extractBearerToken(req) != cookie {
		return body
	}
	return &cookieResponse{body: body, cookies: []string{clearSessionCookie()}}
}

// requireJSONBody refuses a non-JSON login or setup-admin body. A form or
// text/plain POST needs no CORS preflight, so accepting one would let a
// same-site page log a victim into the attacker's account.
func requireJSONBody(path string, req *events.LambdaFunctionURLRequest) error {
	if path != loginPath && path != setupAdminPath {
		return nil
	}
	ct := req.Headers["content-type"]
	if ct == "" {
		ct = req.Headers["Content-Type"]
	}
	if !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return NewClientError(415, "Content-Type must be application/json")
	}
	return nil
}
