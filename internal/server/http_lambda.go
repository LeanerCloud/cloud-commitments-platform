package server

import (
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"

	"github.com/aws/aws-lambda-go/events"
)

// httpToLambdaRequest converts a standard HTTP request to Lambda Function URL
// request format. It fails when the body cannot be read whole; answer that
// with writeBodyReadError.
func httpToLambdaRequest(w http.ResponseWriter, r *http.Request, trustedHops int) (*events.LambdaFunctionURLRequest, error) {
	body, err := readRequestBody(w, r)
	if err != nil {
		return nil, err
	}

	// Convert headers - lowercase all keys to match Lambda Function URL behavior.
	// Go's http.Request.Header canonicalizes keys (e.g. "X-CSRF-Token" → "X-Csrf-Token"),
	// but our middleware expects lowercase keys (e.g. "x-csrf-token").
	headers := make(map[string]string)
	for key, values := range r.Header {
		if len(values) > 0 {
			headers[strings.ToLower(key)] = values[0]
		}
	}

	// Convert query parameters
	queryParams := make(map[string]string)
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			queryParams[key] = values[0]
		}
	}

	sourceIP := requestSourceIP(r, trustedHops)

	// Hand the router the raw (still percent-encoded) path, as Lambda does, so
	// path ids are decoded exactly once, by the router.
	return &events.LambdaFunctionURLRequest{
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Method:    r.Method,
				Path:      r.URL.EscapedPath(),
				Protocol:  r.Proto,
				SourceIP:  sourceIP,
				UserAgent: r.Header.Get("User-Agent"),
			},
			TimeEpoch: time.Now().Unix(),
		},
		RawPath:               r.URL.EscapedPath(),
		RawQueryString:        r.URL.RawQuery,
		Headers:               headers,
		QueryStringParameters: queryParams,
		Body:                  body,
		IsBase64Encoded:       false,
	}, nil
}

// safeHeaderNames is a whitelist of headers that are safe to pass through from Lambda responses.
// This prevents header injection attacks where malicious headers could be set.
var safeHeaderNames = map[string]bool{
	// Content headers
	"content-type":     true,
	"content-length":   true,
	"content-encoding": true,
	// Caching headers
	"cache-control": true,
	"etag":          true,
	"last-modified": true,
	"retry-after":   true,
	// Request tracking headers
	"x-request-id":     true,
	"x-correlation-id": true,
	// CORS headers
	"access-control-allow-origin":      true,
	"access-control-allow-methods":     true,
	"access-control-allow-headers":     true,
	"access-control-allow-credentials": true,
	"access-control-max-age":           true,
	// Security headers
	"strict-transport-security": true,
	"x-content-type-options":    true,
	"x-frame-options":           true,
	"x-xss-protection":          true,
	"content-security-policy":   true,
	"referrer-policy":           true,
	"permissions-policy":        true,
}

// isSafeHeaderValue checks that a header value doesn't contain CRLF injection characters.
func isSafeHeaderValue(value string) bool {
	return !strings.ContainsAny(value, "\r\n")
}

// lambdaResponseToHTTP converts a Lambda Function URL response to standard HTTP response.
func lambdaResponseToHTTP(w http.ResponseWriter, lambdaResp *events.LambdaFunctionURLResponse) {
	// Decode body before writing headers/status to avoid double WriteHeader on error
	var body []byte
	if lambdaResp.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(lambdaResp.Body)
		if err != nil {
			logging.Errorf("Error decoding base64 response body: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		body = decoded
	} else {
		body = []byte(lambdaResp.Body)
	}

	// Set headers with validation to prevent header injection
	for key, value := range lambdaResp.Headers {
		lowerKey := strings.ToLower(key)
		if !safeHeaderNames[lowerKey] {
			logging.Warnf("Blocked unsafe header from Lambda response: %s", key)
			continue
		}
		if !isSafeHeaderValue(value) {
			logging.Warnf("Blocked header with unsafe value (CRLF injection attempt): %s", key)
			continue
		}
		w.Header().Set(key, value)
	}

	// Set cookies with CRLF validation
	for _, cookie := range lambdaResp.Cookies {
		if !isSafeHeaderValue(cookie) {
			logging.Warnf("Blocked cookie with unsafe value (CRLF injection attempt)")
			continue
		}
		w.Header().Add("Set-Cookie", cookie)
	}

	// Set status code and write body
	w.WriteHeader(lambdaResp.StatusCode)
	if _, err := w.Write(body); err != nil { // #nosec G705 -- body is the proxied Lambda API response; Content-Type is set from the validated Lambda response headers above
		logging.Errorf("http: failed to write response body: %v", err)
	}
}
