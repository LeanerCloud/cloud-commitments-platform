package api

import (
	"context"
	"encoding/json"

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

// errorResponseHeaders returns base plus any headers a client error carries
// (for example Retry-After). base is never mutated.
func errorResponseHeaders(err error, base map[string]string) map[string]string {
	ce, ok := IsClientError(err)
	if !ok || len(ce.headers) == 0 {
		return base
	}
	merged := make(map[string]string, len(base)+len(ce.headers))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range ce.headers {
		merged[k] = v
	}
	return merged
}

// handleRequestError converts an error to status code and response.
func (h *Handler) handleRequestError(err error) (int, any) { //nolint:gocritic // unnamedResult: stable internal API, adding names would require body renaming in impl
	if IsNotFoundError(err) {
		return 404, map[string]string{"error": "Not found"}
	}
	if ce, ok := IsClientError(err); ok {
		// If the error carries structured details (e.g. retry soft-block
		// surfaces ops_hint / retry_attempt_n / threshold), flatten them
		// into the JSON body next to "error" so the frontend can branch
		// on machine-readable hints without substring-matching the
		// human message.
		if details := ce.Details(); len(details) > 0 {
			body := make(map[string]any, len(details)+1)
			body["error"] = ce.message
			for k, v := range details {
				if k == "error" {
					continue // never let a detail key shadow the message
				}
				body[k] = v
			}
			return ce.code, body
		}
		return ce.code, map[string]string{"error": ce.message}
	}

	logging.Errorf("API error: %v", err)
	return 500, map[string]string{"error": "Internal server error"}
}

// rawResponse allows handlers to return pre-formatted, non-JSON content
// (e.g. HTML, YAML). buildResponse will use the body and contentType directly
// instead of JSON-marshaling.
//
// csp, when non-empty, overrides the default restrictive Content-Security-
// Policy header set by setSecurityHeaders. Used by serveDocsUI to relax the
// CSP for the Swagger UI page (the default `default-src 'none'` blocks the
// CDN-hosted swagger-ui assets and the inline bootstrap script, leaving the
// page blank — closes issue #329).
type rawResponse struct {
	contentType string
	body        string
	csp         string
}

// buildResponse creates a Lambda Function URL response. It never returns an
// error: all failure modes (marshal failure, non-nil err arg) are converted
// into a 500 response body so callers can use the single-return form.
func (h *Handler) buildResponse(statusCode int, headers map[string]string, body any, err error) *events.LambdaFunctionURLResponse {
	if err != nil {
		return &events.LambdaFunctionURLResponse{
			StatusCode: 500,
			Headers:    headers,
			Body:       `{"error": "internal server error"}`,
		}
	}

	if cr, ok := body.(*cookieResponse); ok {
		resp := h.buildResponse(statusCode, headers, cr.body, nil)
		resp.Cookies = cr.cookies
		return resp
	}

	// Handle raw (non-JSON) responses
	if raw, ok := body.(*rawResponse); ok {
		headers["Content-Type"] = raw.contentType
		if raw.csp != "" {
			headers["Content-Security-Policy"] = raw.csp
		}
		return &events.LambdaFunctionURLResponse{
			StatusCode: statusCode,
			Headers:    headers,
			Body:       raw.body,
		}
	}

	var bodyBytes []byte
	if body != nil {
		var marshalErr error
		bodyBytes, marshalErr = json.Marshal(body)
		if marshalErr != nil {
			logging.Errorf("Failed to marshal response: %v", marshalErr)
			return &events.LambdaFunctionURLResponse{
				StatusCode: 500,
				Headers:    headers,
				Body:       `{"error": "internal server error"}`,
			}
		}
	} else {
		// Nil-body success paths (e.g. DELETE /accounts/:id returning
		// (nil, nil)) previously emitted an empty string, which made the
		// frontend's `response.json()` throw SyntaxError on an otherwise-
		// successful request. Emit an explicit empty JSON object instead
		// so every 2xx response parses cleanly.
		bodyBytes = []byte("{}")
	}

	return &events.LambdaFunctionURLResponse{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       string(bodyBytes),
	}
}
