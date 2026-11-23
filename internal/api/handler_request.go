package api

import (
	"context"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/aws/aws-lambda-go/events"
)

// setSecurityHeaders adds comprehensive security headers to the response.
// HSTSHeaderValue is the Strict-Transport-Security value for every response
// path: API responses here, and the HTTP-mode static, Lambda static and
// health responses in internal/server. One constant keeps the modes from
// drifting apart again (issue #14: Lambda mode had dropped "preload").
// "preload" only makes the domain eligible for the browser preload list;
// submitting it to hstspreload.org is a separate, manual step.
const HSTSHeaderValue = "max-age=31536000; includeSubDomains; preload"

func setSecurityHeaders(headers map[string]string) map[string]string {
	// Content Security Policy - restrictive for API responses
	// Only allow connections to same origin, block all other resources
	headers["Content-Security-Policy"] = "default-src 'none'; frame-ancestors 'none'"

	// Strict Transport Security - enforce HTTPS for 1 year including subdomains
	headers["Strict-Transport-Security"] = HSTSHeaderValue

	// Permissions Policy - disable all browser features
	headers["Permissions-Policy"] = "geolocation=(), microphone=(), camera=()"

	// X-Content-Type-Options - prevent MIME sniffing
	headers["X-Content-Type-Options"] = "nosniff"

	// X-Frame-Options - prevent clickjacking
	headers["X-Frame-Options"] = "DENY"

	// Referrer-Policy - control referrer information
	headers["Referrer-Policy"] = "strict-origin-when-cross-origin"

	// Cache-Control - prevent caching of sensitive data
	headers["Cache-Control"] = "no-store, no-cache, must-revalidate"

	return headers
}

// HandleRequest processes a Lambda Function URL request.
func (h *Handler) HandleRequest(ctx context.Context, req *events.LambdaFunctionURLRequest) (*events.LambdaFunctionURLResponse, error) {
	if req == nil {
		return h.buildResponse(400, h.buildResponseHeaders(), map[string]string{"error": "nil request"}, nil), nil
	}
	corsHeaders := h.buildResponseHeaders()

	// Handle preflight
	method := req.RequestContext.HTTP.Method
	if method == "OPTIONS" {
		return h.buildResponse(200, corsHeaders, nil, nil), nil
	}

	path := req.RequestContext.HTTP.Path
	logging.Debugf("API Request: %s %s", method, redactURL(path))

	// Validate request
	requestCtx, response := h.validateRequestContext(ctx, req, method, path, corsHeaders)
	if response != nil {
		return response, nil
	}

	// Route and execute request
	return h.executeRequest(requestCtx, method, path, req, corsHeaders)
}

// buildResponseHeaders creates response headers with security and CORS settings.
func (h *Handler) buildResponseHeaders() map[string]string {
	corsHeaders := map[string]string{
		"Content-Type": "application/json",
	}

	corsHeaders = setSecurityHeaders(corsHeaders)

	if h.corsAllowedOrigin != "" {
		corsHeaders["Access-Control-Allow-Origin"] = h.corsAllowedOrigin
		corsHeaders["Access-Control-Allow-Methods"] = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
		corsHeaders["Access-Control-Allow-Headers"] = "Content-Type, X-API-Key, Authorization, X-Authorization, X-CSRF-Token"
		corsHeaders["Access-Control-Allow-Credentials"] = "true"
	}

	return corsHeaders
}

func (h *Handler) validateRequestContext(ctx context.Context, req *events.LambdaFunctionURLRequest, method, path string, corsHeaders map[string]string) (context.Context, *events.LambdaFunctionURLResponse) {
	// Validate request body size
	if err := validateRequestBodySize(req.Body); err != nil {
		logging.Warnf("Request body size exceeded: %d bytes", len(req.Body))
		return ctx, h.buildResponse(413, corsHeaders, map[string]string{"error": "Request body too large"}, nil)
	}

	// Validate Content-Type
	if err := validateContentType(req); err != nil {
		return ctx, h.buildResponse(415, corsHeaders, map[string]string{"error": err.Error()}, nil)
	}

	// Validate authentication and CSRF
	requestCtx, response := h.validateSecurityContext(ctx, req, method, path, corsHeaders)
	if response != nil {
		return ctx, response
	}

	return requestCtx, nil
}

func (h *Handler) validateSecurityContext(ctx context.Context, req *events.LambdaFunctionURLRequest, method, path string, corsHeaders map[string]string) (context.Context, *events.LambdaFunctionURLResponse) {
	if h.isPublicEndpoint(path) {
		return ctx, nil
	}

	principal, err := h.authenticatePrincipal(ctx, req)
	if err != nil {
		return ctx, h.buildResponse(401, corsHeaders, h.clearCookieOn401(req, map[string]string{"error": "Unauthorized"}), nil)
	}
	ctx = contextWithPrincipal(ctx, principal)

	// Book the API key's usage here and nowhere else. This is the one code
	// path that runs exactly once per inbound request, so it is the only
	// place where a per-request counter can be incremented correctly. The
	// credential itself is validated several times per request (here, then
	// again by every permission check, and once per verb on the multi-verb
	// gates), so booking inside auth.Service.ValidateUserAPIKey would count
	// validations and inflate every reported number by a factor that varies
	// per endpoint.
	if principal.Kind == PrincipalUserAPIKey {
		h.auth.RecordAPIKeyUsageAsync(principal.APIKeyID)
	}

	if h.requiresCSRFValidation(method, path) {
		if err := h.validateCSRF(ctx, req); err != nil {
			logging.Warnf("CSRF validation failed: %v", err)
			return ctx, h.buildResponse(403, corsHeaders, map[string]string{"error": "CSRF validation failed"}, nil)
		}
	}

	return ctx, nil
}

// executeRequest routes and executes the API request.
func (h *Handler) executeRequest(ctx context.Context, method, path string, req *events.LambdaFunctionURLRequest, corsHeaders map[string]string) (*events.LambdaFunctionURLResponse, error) {
	err := requireJSONBody(path, req)
	var response any
	if err == nil {
		response, err = h.routeRequest(ctx, method, path, req)
	}
	if err == nil {
		response, err = attachLoginCookie(path, response)
	}

	statusCode := 200
	if err != nil {
		statusCode, response = h.handleRequestError(err)
		corsHeaders = errorResponseHeaders(err, corsHeaders)
	}
	// POST is CSRF-checked before this point, so a logout ends the cookie
	// even when the server-side session was already gone. GET and HEAD skip
	// the CSRF check and must never clear it.
	if method == "POST" && path == logoutPath {
		response = &cookieResponse{body: response, cookies: []string{clearSessionCookie()}}
	}

	return h.buildResponse(statusCode, corsHeaders, response, nil), nil
}
