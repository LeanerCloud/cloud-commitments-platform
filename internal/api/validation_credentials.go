package api

import (
	"regexp"
	"strings"
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
