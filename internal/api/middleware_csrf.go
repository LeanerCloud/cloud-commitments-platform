package api

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/aws/aws-lambda-go/events"
)

// requiresCSRFValidation returns true for state-changing requests that need CSRF protection.
//
// It is only consulted for non-public paths: validateSecurityContext returns
// before calling it whenever isPublicEndpoint(path) is true. AuthPublic routes
// that read a session therefore enforce CSRF inside their own handlers, and
// TestAuthPublicMoneyRoutes_SessionWithoutCSRFIsRefused checks that they do.
func (h *Handler) requiresCSRFValidation(method, path string) bool {
	// Every method except the safe ones (RFC 9110 section 9.2.1) can change
	// state, so it needs CSRF protection. Listing the safe methods rather than
	// the unsafe ones keeps PATCH, and any verb a route adds later, covered
	// (issue #1101).
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return false
	}

	// Auth endpoints that don't have a session yet are exempt.
	//
	// Note: /api/purchases/approve/ and /api/purchases/cancel/ are NOT listed
	// here. Those routes are AuthPublic and listed in isPublicEndpoint(), so
	// validateSecurityContext() short-circuits before requiresCSRFValidation() is
	// reached on the token-only (email-link) path. For the session-authed path
	// (approvePurchaseViaSession / cancelPurchaseViaSession), CSRF is enforced
	// directly inside those functions -- a blanket middleware exemption would
	// leave session-authenticated POSTs to these endpoints unprotected (CSRF).
	//
	// Similarly, /api/ri-exchange/approve/ and /api/ri-exchange/reject/ are
	// AuthPublic and therefore exempted by isPublicEndpoint(), not here.
	// approve/ has a session-authed sub-path (approveRIExchangeViaSession)
	// and, like the purchases functions above, enforces CSRF directly
	// inside that function -- NOT here, and not automatically just because
	// it is "similar" to purchases (issue #1757: this exemption used to
	// exist in prose only, with no actual validateCSRF call backing it).
	// reject/ has no session-authed path at all -- it is unconditionally
	// gated on a constant-time comparison against the record's stored
	// approval token, so CSRF does not apply to it.
	//
	// All exemptions use exact-match to prevent a path like
	// /api/register-malicious from bypassing CSRF via accidental prefix overlap.
	// This mirrors the exact-match approach in isPublicEndpoint().
	switch path {
	case "/api/auth/login",
		"/api/auth/setup-admin",
		"/api/auth/forgot-password",
		"/api/auth/reset-password",
		"/api/register": // POST /api/register (public registration, no session)
		return false
	}

	return true
}

// validateCSRF validates the CSRF token from the request header.
func (h *Handler) validateCSRF(ctx context.Context, req *events.LambdaFunctionURLRequest) error {
	if h.auth == nil {
		return fmt.Errorf("authentication service not configured")
	}

	// API-key-authenticated requests bypass CSRF (no cookie-based session).
	if h.apiKeyBypassCSRF(ctx, req) {
		return nil
	}

	sessionToken := h.extractBearerToken(req)
	if sessionToken == "" {
		return fmt.Errorf("no session token for CSRF validation")
	}

	csrfToken := extractCSRFToken(req)
	logMissingCSRFToken(req, csrfToken)
	return h.auth.ValidateCSRFToken(ctx, sessionToken, csrfToken)
}

// apiKeyBypassCSRF returns true when the request presents a valid admin or
// user API key. API-key auth is stateless and not cookie-based, so CSRF
// protection doesn't apply. An invalid API key returns false — the caller
// then falls through to session-based CSRF validation.
func (h *Handler) apiKeyBypassCSRF(ctx context.Context, req *events.LambdaFunctionURLRequest) bool {
	if principal, ok := principalFromContext(ctx); ok {
		return principal.Kind == PrincipalAdminAPIKey || principal.Kind == PrincipalUserAPIKey
	}
	apiKey := req.Headers["x-api-key"]
	if apiKey == "" {
		apiKey = req.Headers["X-API-Key"]
	}
	if apiKey == "" {
		return false
	}
	if h.apiKey != "" && subtle.ConstantTimeCompare([]byte(apiKey), []byte(h.apiKey)) == 1 {
		return true
	}
	if _, _, err := h.auth.ValidateUserAPIKeyAPI(ctx, apiKey); err == nil {
		return true
	}
	return false
}

// extractCSRFToken pulls the CSRF token from the case-insensitive header.
func extractCSRFToken(req *events.LambdaFunctionURLRequest) string {
	if t := req.Headers["x-csrf-token"]; t != "" {
		return t
	}
	return req.Headers["X-CSRF-Token"]
}

// logMissingCSRFToken emits a defensive warn-level log when a session-
// authenticated request reaches CSRF validation with no token header.
// Almost always indicates a frontend regression (sessionStorage cleared, or
// a new fetch site forgot to include the token). ValidateCSRFToken still
// rejects the request -- this just makes the cause obvious in logs.
func logMissingCSRFToken(req *events.LambdaFunctionURLRequest, csrfToken string) {
	if csrfToken != "" {
		return
	}
	logging.Warnf("CSRF validation: empty header on session-authenticated %s %s from %s -- frontend regression?",
		req.RequestContext.HTTP.Method, redactURL(req.RequestContext.HTTP.Path), req.RequestContext.HTTP.SourceIP)
}

// redactURL replaces any token= query parameter value with REDACTED so
// approval/revocation tokens are never written to structured access logs.
// Applied to every URL or path string before it is passed to a logging call.
//
// Matches:
//
//	?token=<value>   at the start of the query string
//	&token=<value>   as a subsequent parameter
//
// The replacement is done with a simple string scan so there is no regexp
// compilation overhead on the hot request path.
func redactURL(u string) string {
	return redactQueryParam(u, "token")
}

// redactQueryParam replaces the value of the named query parameter in u with
// REDACTED. The match is case-sensitive and handles both ? and & prefixes.
func redactQueryParam(u, param string) string {
	// Handle both ?param=val and &param=val forms.
	for _, prefix := range []string{"?" + param + "=", "&" + param + "="} {
		idx := strings.Index(u, prefix)
		if idx == -1 {
			continue
		}
		start := idx + len(prefix)
		// Find the end of the value (next & or end of string).
		end := strings.IndexByte(u[start:], '&')
		if end == -1 {
			u = u[:start] + "REDACTED"
		} else {
			u = u[:start] + "REDACTED" + u[start+end:]
		}
	}
	return u
}
