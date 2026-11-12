package api

import (
	"fmt"
	"strings"
)

var validAWSAuthModes = map[string]bool{
	"access_keys": true, "role_arn": true, "bastion": true, "workload_identity_federation": true,
}

var validAzureAuthModes = map[string]bool{
	"client_secret": true, "managed_identity": true, "workload_identity_federation": true,
}

var validGCPAuthModes = map[string]bool{
	"service_account": true, "application_default": true, "workload_identity_federation": true,
}

// validAccountProviders is the set of concrete providers for an account (excludes empty/"all").
var validAccountProviders = map[string]bool{
	"aws":   true,
	"azure": true,
	"gcp":   true,
}

// validateCloudAccountRequest checks required fields and allowed values.
func validateCloudAccountRequest(req CloudAccountRequest) error {
	if req.Name == "" {
		return NewClientError(400, "name is required")
	}

	if !validAccountProviders[req.Provider] {
		return NewClientError(400, "provider must be one of: aws, azure, gcp")
	}

	if req.ExternalID == "" {
		return NewClientError(400, "external_id is required")
	}

	if err := validateNoControlChars(
		namedField{"name", req.Name}, namedField{"external_id", req.ExternalID},
	); err != nil {
		return err
	}

	if err := validateEmailFormat(req.ContactEmail); err != nil {
		return NewClientError(400, "invalid contact_email format")
	}

	return validateAuthMode(req)
}

// validateAuthMode checks that the provider-specific auth mode is a known value.
func validateAuthMode(req CloudAccountRequest) error {
	switch req.Provider {
	case "aws":
		return validateAWSAuthMode(req)
	case "azure":
		if req.AzureAuthMode != "" && !validAzureAuthModes[req.AzureAuthMode] {
			return NewClientError(400, "invalid azure_auth_mode")
		}
	case "gcp":
		if req.GCPAuthMode != "" && !validGCPAuthModes[req.GCPAuthMode] {
			return NewClientError(400, "invalid gcp_auth_mode")
		}
		if err := validateGCPClientEmail(req.GCPClientEmail); err != nil {
			return err
		}
	}
	return nil
}

// validateAWSAuthMode checks the AWS-specific auth-mode invariants:
// the mode is one of the known values (when set) and, for any auth
// mode that calls sts:AssumeRole into a customer role, the External ID
// satisfies the AWS sts:ExternalId rules (issue #128, extended to
// bastion mode by #129). The split from validateAuthMode keeps the
// parent function's cyclomatic complexity inside the project's gocyclo
// budget.
//
// Self-account onboarding uses role_arn with an empty role ARN to mean
// "use ambient Lambda/container credentials" (see
// credentials.resolveRoleARNProvider).
// That path never calls sts:AssumeRole, so the ExternalId requirement
// doesn't apply — only enforce the validation when an actual cross-
// account role ARN is set. The same exemption is applied to bastion
// mode for parity (an empty role ARN in bastion mode is rejected
// elsewhere by the credential resolver, but the validator stays
// defensive).
//
// workload_identity_federation does NOT use ExternalID — OIDC verifies
// identity via the token subject claim (see resolveWebIdentityProvider
// in internal/credentials/resolver.go), and stscreds.WebIdentityRoleOptions
// has no ExternalID field. access_keys doesn't assume a role at all.
func validateAWSAuthMode(req CloudAccountRequest) error {
	if req.AWSAuthMode != "" && !validAWSAuthModes[req.AWSAuthMode] {
		return NewClientError(400, "invalid aws_auth_mode")
	}
	if err := validateAWSRoleARN(req.AWSRoleARN); err != nil {
		return err
	}
	if err := validateAWSWebIdentityTokenFile(req.AWSWebIdentityTokenFile); err != nil {
		return err
	}
	requiresExternalID := (req.AWSAuthMode == "role_arn" || req.AWSAuthMode == "bastion") &&
		strings.TrimSpace(req.AWSRoleARN) != ""
	if requiresExternalID {
		return validateAWSExternalID(req.AWSExternalID)
	}
	return nil
}

// AWS sts:ExternalId per
// https://docs.aws.amazon.com/IAM/UserGuides/id_roles_create_for-user_externalid.html:
// 2..1224 chars, charset [A-Za-z0-9_+=,.@:/-]. CUDly tightens the lower
// bound to 16 because (a) every value the frontend generates is at least
// 32 chars (UUID or 16-byte hex), and (b) anything shorter is too weak
// to be useful as a confused-deputy guard. Validation runs on both
// create and update via validateCloudAccountRequest.
const (
	awsExternalIDMinLen = 16
	awsExternalIDMaxLen = 1224
)

// validateAWSExternalID enforces the issue #128 backend invariants:
//   - non-empty (defense-in-depth: the frontend always populates this,
//     but a hostile or buggy client posting "" would make AssumeRole
//     bypass the sts:ExternalId condition entirely if the customer's
//     trust policy lacks the StringEquals constraint).
//   - 16..1224 characters (lower bound is CUDly-internal, upper bound
//     is the AWS hard limit).
//   - charset restricted to AWS-accepted characters.
func validateAWSExternalID(s string) error {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return NewClientError(400, "aws_external_id is required for role_arn and bastion auth modes")
	}
	if len(trimmed) < awsExternalIDMinLen || len(trimmed) > awsExternalIDMaxLen {
		return NewClientError(400, fmt.Sprintf(
			"aws_external_id must be %d-%d characters (AWS sts:ExternalId limit)",
			awsExternalIDMinLen, awsExternalIDMaxLen,
		))
	}
	if !isValidAWSExternalIDCharset(trimmed) {
		return NewClientError(400,
			"aws_external_id contains characters AWS sts:ExternalId does not "+
				"accept (allowed: letters, digits, +=,.@:/-_)")
	}
	return nil
}

// isValidAWSExternalIDCharset matches AWS's documented sts:ExternalId regex
// `[\w+=,.@:/-]` — letters, digits, underscore, and the seven punctuation
// marks listed. Implemented byte-wise (single-byte ASCII only) instead of
// regexp to keep this allocation-free on the hot path.
func isValidAWSExternalIDCharset(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9':
			continue
		}
		switch c {
		case '_', '+', '=', ',', '.', '@', ':', '/', '-':
			continue
		}
		return false
	}
	return true
}
