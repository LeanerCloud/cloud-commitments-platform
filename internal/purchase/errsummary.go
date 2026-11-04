package purchase

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"google.golang.org/api/googleapi"
)

// Closed set of purchase failure categories shown to users in place of the
// provider's own error text, which carries ARNs, account IDs and request IDs.
const (
	ErrCategoryAccessDenied = "access_denied"
	ErrCategoryUnavailable  = "not_available"
	ErrCategoryLimit        = "limit_reached"
	ErrCategoryUnknown      = "unknown"
)

var errCategoryMessages = map[string]string{
	ErrCategoryAccessDenied: "the cloud provider refused the purchase: CUDly's role is not allowed to make it in this account",
	ErrCategoryUnavailable:  "the requested commitment is not available from the provider",
	ErrCategoryLimit:        "a provider limit was reached",
	ErrCategoryUnknown:      "the provider returned an error",
}

const partialRunPrefix = "some purchases failed (partial success): "

// ErrCategoryMessage returns the fixed user message for a category.
func ErrCategoryMessage(category string) string {
	if m, ok := errCategoryMessages[category]; ok {
		return m
	}
	return errCategoryMessages[ErrCategoryUnknown]
}

// IsDefiniteRefusal reports whether the provider clearly refused the
// purchase, so nothing was bought.
func IsDefiniteRefusal(category string) bool {
	return category == ErrCategoryAccessDenied || category == ErrCategoryUnavailable || category == ErrCategoryLimit
}

// SummarizeProviderError classifies a live error by type and returns its
// category and fixed message.
func SummarizeProviderError(err error) (category, message string) {
	category = classifyTypedError(err)
	if category == ErrCategoryUnknown && err != nil {
		category = classifyText(err.Error())
	}
	return category, ErrCategoryMessage(category)
}

func classifyTypedError(err error) string {
	var apiErr interface{ ErrorCode() string }
	var azErr *azcore.ResponseError
	var gErr *googleapi.Error
	switch {
	case errors.As(err, &apiErr):
		if c := classifyText(apiErr.ErrorCode()); c != ErrCategoryUnknown {
			return c
		}
	case errors.As(err, &azErr):
		if c := classifyText(azErr.ErrorCode); c != ErrCategoryUnknown {
			return c
		}
		return categoryForStatus(azErr.StatusCode)
	case errors.As(err, &gErr):
		return categoryForStatus(gErr.Code)
	}
	var httpErr interface{ HTTPStatusCode() int }
	if errors.As(err, &httpErr) {
		return categoryForStatus(httpErr.HTTPStatusCode())
	}
	return ErrCategoryUnknown
}

func categoryForStatus(code int) string {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrCategoryAccessDenied
	case http.StatusNotFound:
		return ErrCategoryUnavailable
	case http.StatusTooManyRequests:
		return ErrCategoryLimit
	}
	return ErrCategoryUnknown
}

var (
	deniedNeedles = []string{"accessdenied", "unauthorized", "authorizationfailed", "forbidden",
		"encoded authorization", "statuscode: 401", "statuscode: 403", "response 401", "response 403", "error 403"}
	limitNeedles   = []string{"limitexceeded", "quota", "throttl", "toomanyrequests", "statuscode: 429", "response 429", "error 429"}
	unavailNeedles = []string{"capacity", "offeringnotfound", "notfound", "not available",
		"notavailable", "statuscode: 404", "response 404", "error 404"}
	// Substrings that only appear in provider-authored error text.
	providerMarkers = []string{"operation error", "api error", "statuscode:", "requestid", "arn:aws",
		"encoded authorization", "accessdenied", "unauthorizedoperation", "authorizationfailed",
		"googleapi", "error code:", "response 401", "response 403", "response 404", "response 429", "response 5"}
)

func containsAny(lower string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

func classifyText(s string) string {
	lower := strings.ToLower(s)
	switch {
	case containsAny(lower, deniedNeedles):
		return ErrCategoryAccessDenied
	case containsAny(lower, limitNeedles):
		return ErrCategoryLimit
	case containsAny(lower, unavailNeedles):
		return ErrCategoryUnavailable
	}
	return ErrCategoryUnknown
}

func hasProviderMarker(s string) bool {
	return containsAny(strings.ToLower(s), providerMarkers)
}

// IsDefiniteStoredRefusal reports whether every "; "-joined segment of a stored
// error is a definite provider refusal. One unknown or server-authored segment
// means that rec's outcome is not proven, so the answer is false.
func IsDefiniteStoredRefusal(s string) bool {
	s = strings.TrimPrefix(s, partialRunPrefix)
	for _, seg := range strings.Split(s, "; ") {
		if !hasProviderMarker(seg) || !IsDefiniteRefusal(classifyText(seg)) {
			return false
		}
	}
	return s != ""
}

// SanitizeStoredError rewrites provider error text in an already-stored
// error string into its category message. Strings and "; "-joined segments
// without provider markers pass through unchanged.
func SanitizeStoredError(s string) string {
	if !hasProviderMarker(s) {
		return s
	}
	prefix := ""
	if strings.HasPrefix(s, partialRunPrefix) {
		prefix = partialRunPrefix
		s = strings.TrimPrefix(s, partialRunPrefix)
	}
	segs := strings.Split(s, "; ")
	for i, seg := range segs {
		if hasProviderMarker(seg) {
			segs[i] = ErrCategoryMessage(classifyText(seg))
		}
	}
	return prefix + strings.Join(segs, "; ")
}
