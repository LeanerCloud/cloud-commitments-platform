// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"encoding/base64"
	"fmt"
	"math"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/aws/aws-lambda-go/events"
)

// Security constants.
const (
	// MaxRequestBodySize is the maximum allowed request body size (1MB).
	MaxRequestBodySize = 1 * 1024 * 1024
)

// Input validation helpers

// uuidRegex validates UUID format (used for path parameters).
var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validProviders are the allowed provider values.
// The concrete providers are mirrored in frontend/src/history.ts VALID_PROVIDERS;
// keep the two in sync.
var validProviders = map[string]bool{
	"":      true, // empty is allowed (means all)
	"all":   true,
	"aws":   true,
	"azure": true,
	"gcp":   true,
}

// serviceNameRegex validates service override names: lowercase alphanumeric + hyphens, 1-64 chars.
// Uppercase is rejected to prevent stored-XSS via mixed-case surprises and to keep names consistent.
var serviceNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// regionNameRegex validates AWS/Azure/GCP region names - requires at least one character.
var regionNameRegex = regexp.MustCompile(`^[a-z0-9-]+$`)

// validateProvider checks if a provider value is valid.
func validateProvider(provider string) error {
	if !validProviders[provider] {
		return NewClientError(400, "invalid provider: must be aws, azure, gcp, or all")
	}
	return nil
}

// providerFilterParam validates a read endpoint's provider query parameter
// and returns the value to filter on: "" (every provider) for an absent
// value or "all", else the provider. Anything validateProvider refuses is a
// 400, as on the recommendations and history reads (issue #312).
func providerFilterParam(provider string) (string, error) {
	if err := validateProvider(provider); err != nil {
		return "", err
	}
	if provider == "all" {
		return "", nil
	}
	return provider, nil
}

// validateEmailFormat returns a 400 error when email is non-empty but does not
// parse as an RFC 5322 address or lacks a TLD (e.g. name@hostonly). Empty
// strings are accepted (contact email is optional on cloud accounts).
// The frontend pre-checks the same "has @ and a TLD" shape in
// frontend/src/utils.ts isValidEmail and frontend/src/auth.ts EMAIL_SHAPE_RE;
// this is the authoritative check and the frontend regexes are a looser
// pre-check; keep the two in sync.
func validateEmailFormat(email string) error {
	if email == "" {
		return nil
	}
	addr, err := mail.ParseAddress(email)
	if err != nil {
		return NewClientError(400, "invalid email format")
	}
	// mail.ParseAddress is RFC 5322-compliant and accepts single-label domains
	// like "name@host" that have no TLD. Reject them here so the profile-update
	// path applies the same constraint as sign-up. The address portion always
	// contains exactly one "@" after a successful parse.
	at := strings.LastIndex(addr.Address, "@")
	if at < 0 || !strings.Contains(addr.Address[at+1:], ".") {
		return NewClientError(400, "invalid email format")
	}
	return nil
}

// namedField pairs a request field's JSON name with its value, for
// validateNoControlChars.
type namedField struct{ name, value string }

// validateNoControlChars returns a 400 naming the first field whose value
// holds a control character (C0 such as CR, LF and tab, DEL, or C1). Account
// names and external IDs are single-line identifiers that the federation IaC
// templates print on shell and HCL comment lines, where a line break would
// end the comment and run the rest as code (#970). They are rejected rather
// than stripped, so a stored identifier is never silently changed.
func validateNoControlChars(fields ...namedField) error {
	for _, f := range fields {
		if strings.IndexFunc(f.value, unicode.IsControl) >= 0 {
			return NewClientError(400, f.name+" must not contain control characters such as line breaks or tabs")
		}
	}
	return nil
}

// validateServiceName checks if a service name is valid.
func validateServiceName(service string) error {
	// Empty is allowed for queries (means all services)
	if service == "" {
		return nil
	}

	// Check length limits
	if len(service) > 64 {
		return NewClientError(400, "invalid service name: must be 1-64 characters")
	}

	// Check pattern (now requires at least one character due to + quantifier)
	if !serviceNameRegex.MatchString(service) {
		return NewClientError(400, "invalid service name: must contain only alphanumeric characters and hyphens")
	}
	return nil
}

// validateRegion checks if a region name is valid.
func validateRegion(region string) error {
	// Empty is allowed for queries (means all regions)
	if region == "" {
		return nil
	}

	// Check length limits
	if len(region) > 64 {
		return NewClientError(400, "invalid region: must be 1-64 characters")
	}

	// Check pattern (now requires at least one character due to + quantifier)
	if !regionNameRegex.MatchString(region) {
		return NewClientError(400, "invalid region: must contain only lowercase alphanumeric characters and hyphens")
	}
	return nil
}

// validateServicePath checks for path traversal attacks in service paths.
func validateServicePath(service string) error {
	// Reject path traversal attempts
	if strings.Contains(service, "..") {
		return NewClientError(400, "invalid service path: path traversal not allowed")
	}
	if strings.Contains(service, "//") {
		return NewClientError(400, "invalid service path: double slashes not allowed")
	}

	// Reject any non-alphanumeric except single slash and hyphen
	for _, r := range service {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '/' && r != '-' && r != '_' {
			return NewClientError(400, "invalid service path: contains invalid characters")
		}
	}

	// Ensure format is "provider/service" only
	parts := strings.Split(service, "/")
	if len(parts) != 2 {
		return NewClientError(400, "invalid service path: must be in format provider/service")
	}

	return nil
}

// validateUUID checks if a string is a valid UUID.
func validateUUID(id string) error {
	if !uuidRegex.MatchString(id) {
		return NewClientError(400, "invalid ID format: must be a valid UUID")
	}
	return nil
}

// validUUIDPtrOrNil returns p when *p is a valid UUID, or nil otherwise.
// Used to convert reviewer/creator strings (which may be "admin-api-key"
// for API-key sessions — not a real UUID) to a nullable FK-safe actor pointer
// before stamping transitioned_by on state-transition rows.
func validUUIDPtrOrNil(p *string) *string {
	if p == nil || validateUUID(*p) != nil {
		return nil
	}
	return p
}

// validateContentType checks if the Content-Type header is acceptable for the request.
func validateContentType(req *events.LambdaFunctionURLRequest) error {
	method := req.RequestContext.HTTP.Method
	// Only POST/PUT/PATCH with bodies need content-type validation
	if method != "POST" && method != "PUT" && method != "PATCH" {
		return nil
	}

	// If there's no body, no content-type required
	if req.Body == "" {
		return nil
	}

	contentType := req.Headers["content-type"]
	if contentType == "" {
		contentType = req.Headers["Content-Type"]
	}

	// Accept application/json or form data
	if contentType == "" {
		return NewClientError(400, "Content-Type header is required for requests with a body")
	}

	// Check for valid content types (allowing charset suffixes)
	validTypes := []string{"application/json", "application/x-www-form-urlencoded"}
	for _, vt := range validTypes {
		if strings.HasPrefix(contentType, vt) {
			return nil
		}
	}

	return NewClientError(400, "unsupported Content-Type: must be application/json")
}

// validateRequestBodySize checks if the request body is within allowed limits.
func validateRequestBodySize(body string) error {
	if len(body) > MaxRequestBodySize {
		return NewClientError(400, fmt.Sprintf("request body too large: maximum size is %d bytes", MaxRequestBodySize))
	}
	return nil
}

// MaxAccountIDsPerRequest caps the number of account IDs accepted in a
// single comma-separated `account_ids` query parameter. Each accepted ID
// fans out into per-account DB queries / cloud API calls downstream, so
// an unbounded list is an amplification vector — a single request with
// thousands of IDs can exhaust connection pools or hit cloud-API rate
// limits. 200 is generous for legitimate usage (typical operators have
// far fewer onboarded accounts) while keeping the worst-case work bounded.
const MaxAccountIDsPerRequest = 200

// parseAccountIDs splits a comma-separated account_ids query parameter into a slice.
// Ids are trimmed and lowercased so every caller compares canonical UUIDs.
// Empty entries are removed. Returns nil when the input is empty.
// Returns an error if any value is not a valid UUID, or if the list
// exceeds MaxAccountIDsPerRequest entries.
func parseAccountIDs(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	// Cap the parse step itself: count non-empty entries before allocating
	// the result slice. We reject early so a megabyte-long string of empty
	// commas can't pin an Lambda CPU on Split allocations.
	nonEmpty := 0
	for _, id := range parts {
		if strings.TrimSpace(id) != "" {
			nonEmpty++
		}
	}
	if nonEmpty > MaxAccountIDsPerRequest {
		return nil, NewClientError(400, fmt.Sprintf("too many account IDs: max %d per request", MaxAccountIDsPerRequest))
	}
	ids := make([]string, 0, nonEmpty)
	for _, id := range parts {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if err := validateUUID(trimmed); err != nil {
			return nil, fmt.Errorf("invalid account_ids: %q is not a valid UUID", trimmed)
		}
		ids = append(ids, strings.ToLower(trimmed))
	}
	return ids, nil
}

// decodeBase64Password decodes a base64-encoded password.
// Returns the decoded password or an error if decoding fails.
// If the input is empty, returns empty string with no error.
func decodeBase64Password(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", NewClientError(400, "invalid password encoding")
	}
	return string(decoded), nil
}

// parseMinSavingsParam parses a numeric savings-floor query parameter.
// Returns (0, nil) when the parameter is absent or empty (no floor).
// Returns 400 when the value is present but not a finite non-negative
// integer or float (NaN and +/-Inf are rejected: a NaN floor silently
// disables or inverts the filter downstream). Fractional values are
// allowed (e.g. "12.5") since savings floors can be sub-dollar amounts.
//
// paramName is included in the error message so callers can distinguish
// min_savings_usd vs min_savings_pct errors in client logs.
func parseMinSavingsParam(raw, paramName string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, NewClientError(400, fmt.Sprintf("%s must be a finite non-negative number", paramName))
	}
	if v < 0 {
		return 0, NewClientError(400, fmt.Sprintf("%s must be non-negative", paramName))
	}
	return v, nil
}
