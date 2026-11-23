package api

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
)

// gcpClientEmailRegex matches a GCP service-account email.
// Pattern: <name>@<project>.iam.gserviceaccount.com
// The service-account name component may contain alphanumerics, dots, hyphens,
// and underscores. The project ID component may contain alphanumerics, dots, and
// hyphens. Rejecting any value that does not match prevents URL path injection in
// BuildGCPFederatedCredential (issue #405).
var gcpClientEmailRegex = regexp.MustCompile(`^[a-zA-Z0-9._-]+@[a-zA-Z0-9.-]+\.iam\.gserviceaccount\.com$`)

// awsRoleARNRegex matches a valid IAM role ARN across commercial, China, and GovCloud
// partitions. The role-name portion is restricted to the IAM-permitted character
// set ([A-Za-z0-9+=,.@_-]) for both the optional path segments and the final
// name. Limiting the trailing portion prevents whitespace, newlines, or other
// control characters from sneaking past validation and reaching
// stscreds.NewAssumeRoleProvider (issue #413).
var awsRoleARNRegex = regexp.MustCompile(`^arn:(aws|aws-cn|aws-us-gov):iam::[0-9]{12}:role/(?:[A-Za-z0-9+=,.@_-]+/)*[A-Za-z0-9+=,.@_-]+$`)

// awsWebIdentityTokenFilePrefixes lists the only path prefixes accepted for
// aws_web_identity_token_file. Restricting to known EKS/Kubernetes mount points
// prevents arbitrary host-file reads when the credential resolver reads the file
// and sends its content to AWS STS (issue #403).
var awsWebIdentityTokenFilePrefixes = []string{
	"/var/run/secrets/eks.amazonaws.com/serviceaccount/",
	"/var/run/secrets/kubernetes.io/serviceaccount/",
}

// validateGCPClientEmail returns a 400 error when gcp_client_email is non-empty
// but does not match the GCP service-account email format. An empty value is
// allowed (field is optional when auth mode does not require impersonation).
func validateGCPClientEmail(email string) error {
	if email == "" {
		return nil
	}
	if !gcpClientEmailRegex.MatchString(email) {
		return NewClientError(400, "gcp_client_email must be a valid GCP service-account email "+
			"(format: name@project.iam.gserviceaccount.com)")
	}
	return nil
}

// validateAWSRoleARN returns a 400 error when aws_role_arn is non-empty but
// does not match the IAM role ARN format for any AWS partition. An empty value
// is allowed (self-account onboarding uses an empty role ARN to mean "ambient
// credentials").
func validateAWSRoleARN(arn string) error {
	if arn == "" {
		return nil
	}
	if !awsRoleARNRegex.MatchString(arn) {
		return NewClientError(400, "aws_role_arn must be a valid IAM role ARN "+
			"(format: arn:aws:iam::<12-digit-account-id>:role/<name>)")
	}
	return nil
}

// validateAWSWebIdentityTokenFile returns a 400 error when the path is non-empty
// but does not start with one of the allowed prefixes, or contains path traversal
// sequences. An empty value is allowed (the resolver falls back to the
// AWS_WEB_IDENTITY_TOKEN_FILE environment variable).
func validateAWSWebIdentityTokenFile(path string) error {
	if path == "" {
		return nil
	}
	// Block path traversal regardless of prefix match.
	if strings.Contains(path, "..") {
		return NewClientError(400, "aws_web_identity_token_file must not contain path traversal sequences")
	}
	for _, prefix := range awsWebIdentityTokenFilePrefixes {
		if strings.HasPrefix(path, prefix) {
			return nil
		}
	}
	return NewClientError(400, "aws_web_identity_token_file must start with an allowed prefix "+
		"(/var/run/secrets/eks.amazonaws.com/serviceaccount/ or "+
		"/var/run/secrets/kubernetes.io/serviceaccount/)")
}

// credentialPayloadSchema defines the per-credential-type required keys and
// optional keys. Any payload key outside the union of required+optional is
// rejected. Required keys must be non-empty strings. The schemas mirror what
// the credentials resolver actually consumes — see internal/credentials/resolver.go
// (awsAccessKeyPayload, the azure_client_secret + azure WIF + GCP loaders).
type credentialPayloadSchema struct {
	required []string
	optional []string
}

var credentialPayloadSchemas = map[string]credentialPayloadSchema{
	"aws_access_keys":     {required: []string{"access_key_id", "secret_access_key"}},
	"azure_client_secret": {required: []string{"client_secret"}},
}

// gcpServiceAccountKeys are the fields a Google service-account JSON file is
// expected to carry. Subset that we strictly require, plus the rest as optional.
var gcpServiceAccountRequired = []string{"type", "project_id", "private_key", "client_email"}

var gcpServiceAccountOptional = []string{
	"private_key_id", "client_id", "auth_uri", "token_uri",
	"auth_provider_x509_cert_url", "client_x509_cert_url", "universe_domain",
}

// gcpWIFConfigRequired/Optional follow the GCP "external_account" credential
// JSON format documented at https://google.aip.dev/auth/4117.
var gcpWIFConfigRequired = []string{"type", "audience", "subject_token_type", "token_url", "credential_source"}

var gcpWIFConfigOptional = []string{
	"service_account_impersonation_url", "service_account_impersonation",
	"workforce_pool_user_project", "quota_project_id", "universe_domain",
}

// maxCredentialPayloadDepth caps the JSON nesting allowed inside a
// CredentialsRequest.Payload. Two is enough for every supported credential type
// (top-level keys → simple values, or one nested object for credential_source).
const maxCredentialPayloadDepth = 2

// payloadTypeMatches returns true when payload["type"] is a string equal to
// want. Returns false when the field is absent, wrong type, or wrong value.
// Extracted to avoid a `||` operator in validateCredentialPayload that would
// push the function over the cyclomatic complexity gate.
func payloadTypeMatches(payload map[string]interface{}, want string) bool {
	t, ok := payload["type"].(string)
	return ok && t == want
}

// validateCredentialPayload enforces shape per declared credential_type. It
// rejects payloads with missing required keys, unknown extra keys, non-string
// required values, or excessive nesting depth. Caller has already verified the
// credentialType is in validCredentialTypes.
func validateCredentialPayload(credentialType string, payload map[string]interface{}) error {
	if len(payload) == 0 {
		return NewClientError(400, "credentials payload must not be empty")
	}
	if depth := payloadDepth(payload, 1); depth > maxCredentialPayloadDepth {
		return NewClientError(400, fmt.Sprintf("credentials payload nests too deeply (max %d levels)", maxCredentialPayloadDepth))
	}

	switch credentialType {
	case "aws_access_keys", "azure_client_secret":
		schema := credentialPayloadSchemas[credentialType]
		return validateFlatPayload(credentialType, payload, schema.required, schema.optional)
	case "gcp_service_account":
		if err := validateFlatPayload(credentialType, payload, gcpServiceAccountRequired, gcpServiceAccountOptional); err != nil {
			return err
		}
		if !payloadTypeMatches(payload, "service_account") {
			return NewClientError(400, "gcp_service_account payload must have type=\"service_account\"")
		}
		return nil
	case "gcp_workload_identity_config":
		if err := validateGCPWIFPayload(payload); err != nil {
			return err
		}
		if !payloadTypeMatches(payload, "external_account") {
			return NewClientError(400, "gcp_workload_identity_config payload must have type=\"external_account\"")
		}
		return nil
	}
	// validCredentialTypes is the gate; if a new type slips past it without a
	// schema entry here, that is a programming error worth surfacing.
	return NewClientError(400, fmt.Sprintf("no payload schema defined for credential_type %q", credentialType))
}

// validateFlatPayload checks that every required key is present as a non-empty
// string and that no key falls outside required+optional.
func validateFlatPayload(credentialType string, payload map[string]interface{}, required, optional []string) error {
	allowed := buildAllowedSet(required, optional)
	if err := rejectUnknownKeys(credentialType, payload, allowed); err != nil {
		return err
	}
	for _, key := range required {
		v, ok := payload[key]
		if !ok {
			return NewClientError(400, fmt.Sprintf("credentials payload for %s missing required key %q", credentialType, key))
		}
		s, isStr := v.(string)
		if !isStr || strings.TrimSpace(s) == "" {
			return NewClientError(400, fmt.Sprintf("credentials payload key %q must be a non-empty string", key))
		}
	}
	return nil
}

// validateGCPWIFPayload checks that the WIF config payload has every required
// key, allows optional GCP fields, and does not contain unknown keys. The
// `credential_source` value may be a nested object; everything else must be a
// non-empty string.
func validateGCPWIFPayload(payload map[string]interface{}) error {
	allowed := buildAllowedSet(gcpWIFConfigRequired, gcpWIFConfigOptional)
	if err := rejectUnknownKeys("gcp_workload_identity_config", payload, allowed); err != nil {
		return err
	}
	for _, key := range gcpWIFConfigRequired {
		if err := validateGCPWIFRequiredKey(payload, key); err != nil {
			return err
		}
	}
	return validateGCPWIFEndpointKeys(payload)
}

// validateGCPWIFEndpointKeys checks the config's token exchange and
// service-account impersonation URLs with credentials.ValidateGCPWIFEndpoints,
// the same check the resolver runs before it uses a stored config. Runs after
// the required-key checks, so token_url is a non-empty string here.
func validateGCPWIFEndpointKeys(payload map[string]interface{}) error {
	tokenURL, ok := payload["token_url"].(string)
	if !ok {
		return NewClientError(400, "credentials payload key \"token_url\" must be a non-empty string")
	}
	impersonationURL := ""
	if v, ok := payload["service_account_impersonation_url"]; ok {
		s, isStr := v.(string)
		if !isStr || strings.TrimSpace(s) == "" {
			return NewClientError(400, "credentials payload key \"service_account_impersonation_url\" must be a non-empty string")
		}
		impersonationURL = s
	}
	if err := credentials.ValidateGCPWIFEndpoints(tokenURL, impersonationURL); err != nil {
		return NewClientError(400, "credentials payload for gcp_workload_identity_config: "+err.Error())
	}
	return nil
}

// validateGCPWIFRequiredKey checks one required GCP WIF key, treating
// "credential_source" specially as a nested object.
func validateGCPWIFRequiredKey(payload map[string]interface{}, key string) error {
	v, ok := payload[key]
	if !ok {
		return NewClientError(400, fmt.Sprintf("credentials payload for gcp_workload_identity_config missing required key %q", key))
	}
	if key == "credential_source" {
		if _, isMap := v.(map[string]interface{}); !isMap {
			return NewClientError(400, "credentials payload key \"credential_source\" must be an object")
		}
		return nil
	}
	s, isStr := v.(string)
	if !isStr || strings.TrimSpace(s) == "" {
		return NewClientError(400, fmt.Sprintf("credentials payload key %q must be a non-empty string", key))
	}
	return nil
}

// buildAllowedSet collapses required + optional key lists into a lookup set.
func buildAllowedSet(required, optional []string) map[string]bool {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, k := range required {
		allowed[k] = true
	}
	for _, k := range optional {
		allowed[k] = true
	}
	return allowed
}

// rejectUnknownKeys returns 400 when any payload key is outside the allowed set.
func rejectUnknownKeys(credentialType string, payload map[string]interface{}, allowed map[string]bool) error {
	for key := range payload {
		if !allowed[key] {
			return NewClientError(400, fmt.Sprintf("credentials payload for %s contains unknown key %q", credentialType, key))
		}
	}
	return nil
}

// payloadDepth returns the maximum nesting depth of m, counting the top-level
// map as depth 1. A nested map adds 1; non-map values do not.
func payloadDepth(m map[string]interface{}, current int) int {
	maxDepth := current
	for _, v := range m {
		if nested, ok := v.(map[string]interface{}); ok {
			if d := payloadDepth(nested, current+1); d > maxDepth {
				maxDepth = d
			}
		}
	}
	return maxDepth
}
