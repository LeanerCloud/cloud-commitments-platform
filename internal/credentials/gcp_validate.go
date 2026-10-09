package credentials

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

// GCPCredentialKind classifies why a stored GCP credential configuration was
// rejected. The values double as the coarse categories used by the audit tool.
type GCPCredentialKind string

const (
	GCPKindInvalidJSON        GCPCredentialKind = "invalid-json"
	GCPKindUnsupportedKey     GCPCredentialKind = "unsupported-key"
	GCPKindUnsupportedToken   GCPCredentialKind = "unsupported-token-endpoint"
	GCPKindUnsupportedSource  GCPCredentialKind = "unsupported-credential-source"
	GCPKindOther              GCPCredentialKind = "other"
	gcpExternalAccountType                      = "external_account"
	gcpServiceAccountType                       = "service_account"
	gcpTokenURL                                 = "https://sts.googleapis.com/v1/token"
	gcpTokenInfoURL                             = "https://sts.googleapis.com/v1/introspect"
	gcpServiceAccountTokenURI                   = "https://oauth2.googleapis.com/token"
	gcpUniverseDomain                           = "googleapis.com"
	gcpIMDSBase                                 = "http://169.254.169.254/latest/"
	gcpFileSourcePrefix                         = "/var/run/secrets/azure/tokens/"
	gcpAWSEnvironmentID                         = "aws1"
	gcpAWSRegionURL                             = gcpIMDSBase + "meta-data/placement/availability-zone"
	gcpAWSCredentialsURL                        = gcpIMDSBase + "meta-data/iam/security-credentials"
	gcpAWSSessionTokenURL                       = gcpIMDSBase + "api/token"
)

var (
	gcpImpersonationURLRe = regexp.MustCompile(`^https://iamcredentials\.googleapis\.com/v1/projects/-/serviceAccounts/[A-Za-z0-9._-]+@[A-Za-z0-9.-]+\.iam\.gserviceaccount\.com:generateAccessToken$`)
	gcpAWSVerificationRe  = regexp.MustCompile(`^https://sts\.(\{region\}|[a-z0-9-]+)\.amazonaws\.com(\.cn)?\?Action=GetCallerIdentity&Version=2011-06-15$`)

	gcpExternalAccountKeys = keySet("type", "audience", "subject_token_type", "token_url", "credential_source",
		"service_account_impersonation_url", "service_account_impersonation", "universe_domain",
		"token_info_url", "workforce_pool_user_project", "quota_project_id")
	gcpExternalAccountRequired = []string{"type", "audience", "subject_token_type", "token_url", "credential_source"}
	gcpServiceAccountKeys      = keySet("type", "project_id", "private_key", "client_email", "private_key_id",
		"client_id", "auth_uri", "token_uri", "auth_provider_x509_cert_url", "client_x509_cert_url", "universe_domain")
	gcpAWSSourceKeys    = keySet("environment_id", "region_url", "url", "regional_cred_verification_url", "imdsv2_session_token_url")
	gcpFileSourceKeys   = keySet("file", "format")
	gcpFileFormatKeys   = keySet("type", "subject_token_field_name")
	gcpImpersonationKey = keySet("token_lifetime_seconds")
)

// GCPCredentialError is returned by ValidateGCPCredentialJSON. Message names
// the offending field and what to change; it never contains field values.
type GCPCredentialError struct {
	Kind    GCPCredentialKind
	Message string
}

func (e *GCPCredentialError) Error() string { return e.Message }

func gcpErr(kind GCPCredentialKind, format string, args ...any) error {
	return &GCPCredentialError{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// jsonString returns v when it is a string and "" otherwise.
func jsonString(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func keySet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// ValidateGCPCredentialJSON checks that raw, the exact bytes that will be
// handed to the Google auth library, is a supported GCP credential
// configuration for credType. Keys are matched case-sensitively at every
// level and URLs must match the supported endpoints exactly.
func ValidateGCPCredentialJSON(credType string, raw []byte) error {
	m, err := parseStrictJSONObject(raw)
	if err != nil {
		return err
	}
	switch credType {
	case CredTypeGCPWIFConfig:
		return validateExternalAccount(m)
	case CredTypeGCPServiceAccount:
		return validateServiceAccount(m)
	}
	return gcpErr(GCPKindOther, "unsupported GCP credential type; use gcp_service_account or gcp_workload_identity_config")
}

func parseStrictJSONObject(raw []byte) (map[string]any, error) {
	bad := func() error {
		return gcpErr(GCPKindInvalidJSON, "credential configuration must be a single well-formed JSON object without duplicate keys or trailing content")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := scanDuplicateKeys(dec); err != nil {
		return nil, bad()
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, bad()
	}
	var m map[string]any
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, bad()
	}
	return m, nil
}

var errDuplicateKey = errors.New("duplicate key")

// scanDuplicateKeys consumes one JSON value from dec and fails on a repeated
// object key at any depth.
func scanDuplicateKeys(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for dec.More() {
		if d == '{' {
			kt, keyErr := dec.Token()
			if keyErr != nil {
				return keyErr
			}
			k := jsonString(kt)
			if seen[k] {
				return errDuplicateKey
			}
			seen[k] = true
		}
		if nestedErr := scanDuplicateKeys(dec); nestedErr != nil {
			return nestedErr
		}
	}
	_, err = dec.Token()
	return err
}

func validateServiceAccount(m map[string]any) error {
	if err := rejectUnknownKeys(m, gcpServiceAccountKeys, "", GCPKindUnsupportedKey); err != nil {
		return err
	}
	if jsonString(m["type"]) != gcpServiceAccountType {
		return gcpErr(GCPKindOther, `field "type" must be "service_account" for a gcp_service_account credential`)
	}
	if v, ok := m["token_uri"]; ok {
		if s, isStr := v.(string); !isStr || s != gcpServiceAccountTokenURI {
			return gcpErr(GCPKindUnsupportedToken, `field "token_uri" must be the Google OAuth2 token endpoint; remove it or use the value from the downloaded service account key`)
		}
	}
	return nil
}

func validateExternalAccount(m map[string]any) error {
	if err := rejectUnknownKeys(m, gcpExternalAccountKeys, "", GCPKindUnsupportedKey); err != nil {
		return err
	}
	if err := validateExternalAccountFields(m); err != nil {
		return err
	}
	if err := validateEndpoints(m); err != nil {
		return err
	}
	if v, ok := m["universe_domain"]; ok {
		if s, isStr := v.(string); !isStr || s != gcpUniverseDomain {
			return gcpErr(GCPKindOther, `field "universe_domain" must be "googleapis.com"; remove it`)
		}
	}
	if v, ok := m["service_account_impersonation"]; ok {
		if err := validateImpersonationOptions(v); err != nil {
			return err
		}
	}
	return validateCredentialSource(m["credential_source"])
}

func validateExternalAccountFields(m map[string]any) error {
	for _, k := range gcpExternalAccountRequired {
		if _, ok := m[k]; !ok {
			return gcpErr(GCPKindOther, "missing required field %q", k)
		}
	}
	if jsonString(m["type"]) != gcpExternalAccountType {
		return gcpErr(GCPKindOther, `field "type" must be "external_account" for a gcp_workload_identity_config credential`)
	}
	for _, k := range []string{"audience", "subject_token_type", "workforce_pool_user_project", "quota_project_id"} {
		if v, ok := m[k]; ok {
			if s, isStr := v.(string); !isStr || strings.TrimSpace(s) == "" {
				return gcpErr(GCPKindOther, "field %q must be a non-empty string", k)
			}
		}
	}
	return nil
}

func validateEndpoints(m map[string]any) error {
	fixed := []struct{ key, want string }{{"token_url", gcpTokenURL}, {"token_info_url", gcpTokenInfoURL}}
	for _, f := range fixed {
		if v, ok := m[f.key]; ok {
			if s, isStr := v.(string); !isStr || s != f.want {
				return gcpErr(GCPKindUnsupportedToken, "field %q must be exactly %s; change it to that value", f.key, f.want)
			}
		}
	}
	if v, ok := m["service_account_impersonation_url"]; ok {
		if s, isStr := v.(string); !isStr || !gcpImpersonationURLRe.MatchString(s) {
			return gcpErr(GCPKindUnsupportedToken, `field "service_account_impersonation_url" must be https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/<service-account-email>:generateAccessToken`)
		}
	}
	return nil
}

func validateImpersonationOptions(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return gcpErr(GCPKindOther, `field "service_account_impersonation" must be an object`)
	}
	if err := rejectUnknownKeys(obj, gcpImpersonationKey, "service_account_impersonation.", GCPKindOther); err != nil {
		return err
	}
	if n, has := obj["token_lifetime_seconds"]; has {
		num, isNum := n.(json.Number)
		if !isNum {
			return gcpErr(GCPKindOther, `field "service_account_impersonation.token_lifetime_seconds" must be an integer`)
		}
		if _, err := num.Int64(); err != nil {
			return gcpErr(GCPKindOther, `field "service_account_impersonation.token_lifetime_seconds" must be an integer`)
		}
	}
	return nil
}

func validateCredentialSource(v any) error {
	src, ok := v.(map[string]any)
	if !ok || len(src) == 0 {
		return gcpErr(GCPKindUnsupportedSource, `field "credential_source" must be an object describing an AWS or workload identity token file source`)
	}
	_, hasFile := src["file"]
	_, hasEnv := src["environment_id"]
	switch {
	case hasFile && !hasEnv:
		return validateFileSource(src)
	case hasEnv && !hasFile:
		return validateAWSSource(src)
	}
	return gcpErr(GCPKindUnsupportedSource, `field "credential_source" must be either an AWS source (environment_id "aws1") or a token file source under `+gcpFileSourcePrefix)
}

func validateFileSource(src map[string]any) error {
	if err := rejectUnknownKeys(src, gcpFileSourceKeys, "credential_source.", GCPKindUnsupportedSource); err != nil {
		return err
	}
	file := jsonString(src["file"])
	if file == "" || file != filepath.Clean(file) || !strings.HasPrefix(file, gcpFileSourcePrefix) || len(file) == len(gcpFileSourcePrefix) {
		return gcpErr(GCPKindUnsupportedSource, `field "credential_source.file" must be a clean absolute path under `+gcpFileSourcePrefix)
	}
	if f, ok := src["format"]; ok {
		return validateFileFormat(f)
	}
	return nil
}

func validateFileFormat(f any) error {
	obj, isObj := f.(map[string]any)
	if !isObj {
		return gcpErr(GCPKindUnsupportedSource, `field "credential_source.format" must be an object`)
	}
	if err := rejectUnknownKeys(obj, gcpFileFormatKeys, "credential_source.format.", GCPKindUnsupportedSource); err != nil {
		return err
	}
	if t := jsonString(obj["type"]); t != "text" && t != "json" {
		return gcpErr(GCPKindUnsupportedSource, `field "credential_source.format.type" must be "text" or "json"`)
	}
	if n, has := obj["subject_token_field_name"]; has {
		if s, isStr := n.(string); !isStr || s == "" {
			return gcpErr(GCPKindUnsupportedSource, `field "credential_source.format.subject_token_field_name" must be a non-empty string`)
		}
	}
	return nil
}

func validateAWSSource(src map[string]any) error {
	if err := rejectUnknownKeys(src, gcpAWSSourceKeys, "credential_source.", GCPKindUnsupportedSource); err != nil {
		return err
	}
	if jsonString(src["environment_id"]) != gcpAWSEnvironmentID {
		return gcpErr(GCPKindUnsupportedSource, `field "credential_source.environment_id" must be "aws1"`)
	}
	fixed := []struct {
		key, want string
		required  bool
	}{
		{"region_url", gcpAWSRegionURL, true},
		{"url", gcpAWSCredentialsURL, true},
		{"imdsv2_session_token_url", gcpAWSSessionTokenURL, false},
	}
	for _, f := range fixed {
		v, ok := src[f.key]
		if !ok && !f.required {
			continue
		}
		if s, isStr := v.(string); !ok || !isStr || s != f.want {
			return gcpErr(GCPKindUnsupportedSource, "field \"credential_source.%s\" must be exactly %s", f.key, f.want)
		}
	}
	if !gcpAWSVerificationRe.MatchString(jsonString(src["regional_cred_verification_url"])) {
		return gcpErr(GCPKindUnsupportedSource, `field "credential_source.regional_cred_verification_url" must be https://sts.{region}.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15`)
	}
	return nil
}

func rejectUnknownKeys(m map[string]any, allowed map[string]bool, prefix string, kind GCPCredentialKind) error {
	for k := range m {
		if !allowed[k] {
			return gcpErr(kind, "field %q is not supported; remove it", prefix+k)
		}
	}
	return nil
}
