package credentials

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Configurations as emitted by iac/federation/gcp-target/terraform/registration.tf
// (jsonencode output, keys sorted).
const (
	iacAWSShape  = `{"audience":"//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/q","credential_source":{"environment_id":"aws1","region_url":"http://169.254.169.254/latest/meta-data/placement/availability-zone","regional_cred_verification_url":"https://sts.{region}.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15","url":"http://169.254.169.254/latest/meta-data/iam/security-credentials"},"service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/sa@proj.iam.gserviceaccount.com:generateAccessToken","subject_token_type":"urn:ietf:params:aws:token-type:aws4_request","token_url":"https://sts.googleapis.com/v1/token","type":"external_account"}`
	iacFileShape = `{"audience":"//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/q","credential_source":{"file":"/var/run/secrets/azure/tokens/azure-identity-token"},"service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/sa@proj.iam.gserviceaccount.com:generateAccessToken","subject_token_type":"urn:ietf:params:oauth:token-type:id_token","token_url":"https://sts.googleapis.com/v1/token","type":"external_account"}`
	validSAJSON  = `{"type":"service_account","project_id":"p","private_key":"k","client_email":"sa@p.iam.gserviceaccount.com","token_uri":"https://oauth2.googleapis.com/token"}`
)

func TestValidateGCPCredentialJSON_Accepted(t *testing.T) {
	assert.NoError(t, ValidateGCPCredentialJSON(CredTypeGCPWIFConfig, []byte(iacAWSShape)))
	assert.NoError(t, ValidateGCPCredentialJSON(CredTypeGCPWIFConfig, []byte(iacFileShape)))
	assert.NoError(t, ValidateGCPCredentialJSON(CredTypeGCPServiceAccount, []byte(validSAJSON)))
	withOptional := strings.Replace(iacFileShape, `"type":"external_account"`,
		`"type":"external_account","universe_domain":"googleapis.com","token_info_url":"https://sts.googleapis.com/v1/introspect","service_account_impersonation":{"token_lifetime_seconds":3600}`, 1)
	assert.NoError(t, ValidateGCPCredentialJSON(CredTypeGCPWIFConfig, []byte(withOptional)))
	withFormat := strings.Replace(iacFileShape, `"file":"/var/run/secrets/azure/tokens/azure-identity-token"`,
		`"file":"/var/run/secrets/azure/tokens/azure-identity-token","format":{"type":"json","subject_token_field_name":"t"}`, 1)
	assert.NoError(t, ValidateGCPCredentialJSON(CredTypeGCPWIFConfig, []byte(withFormat)))
	withIMDSv2 := strings.Replace(iacAWSShape, `"url":"http://169.254.169.254/latest/meta-data/iam/security-credentials"`,
		`"url":"http://169.254.169.254/latest/meta-data/iam/security-credentials","imdsv2_session_token_url":"http://169.254.169.254/latest/api/token"`, 1)
	assert.NoError(t, ValidateGCPCredentialJSON(CredTypeGCPWIFConfig, []byte(withIMDSv2)))
	withRegion := strings.Replace(iacAWSShape, "sts.{region}.amazonaws.com", "sts.us-east-1.amazonaws.com", 1)
	assert.NoError(t, ValidateGCPCredentialJSON(CredTypeGCPWIFConfig, []byte(withRegion)))
}

func TestValidateGCPCredentialJSON_Rejected(t *testing.T) {
	rep := func(base, old, repl string) string {
		require.Contains(t, base, old)
		return strings.Replace(base, old, repl, 1)
	}
	const tokenURL = `"https://sts.googleapis.com/v1/token"`
	const impURL = `"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/sa@proj.iam.gserviceaccount.com:generateAccessToken"`
	const fileVal = `"/var/run/secrets/azure/tokens/azure-identity-token"`
	cases := []struct {
		name     string
		credType string
		payload  string
		kind     GCPCredentialKind
	}{
		{"token_url other host", CredTypeGCPWIFConfig, rep(iacFileShape, tokenURL, `"https://example.invalid/v1/token"`), GCPKindUnsupportedToken},
		{"token_url http", CredTypeGCPWIFConfig, rep(iacFileShape, tokenURL, `"http://sts.googleapis.com/v1/token"`), GCPKindUnsupportedToken},
		{"token_url suffix host", CredTypeGCPWIFConfig, rep(iacFileShape, tokenURL, `"https://sts.googleapis.com.example.invalid/v1/token"`), GCPKindUnsupportedToken},
		{"token_url host in path", CredTypeGCPWIFConfig, rep(iacFileShape, tokenURL, `"https://example.invalid/sts.googleapis.com/v1/token"`), GCPKindUnsupportedToken},
		{"token_url userinfo", CredTypeGCPWIFConfig, rep(iacFileShape, tokenURL, `"https://sts.googleapis.com@example.invalid/v1/token"`), GCPKindUnsupportedToken},
		{"token_url port", CredTypeGCPWIFConfig, rep(iacFileShape, tokenURL, `"https://sts.googleapis.com:8443/v1/token"`), GCPKindUnsupportedToken},
		{"token_url uppercase", CredTypeGCPWIFConfig, rep(iacFileShape, tokenURL, `"https://STS.googleapis.com/v1/token"`), GCPKindUnsupportedToken},
		{"token_url unicode host", CredTypeGCPWIFConfig, rep(iacFileShape, tokenURL, `"https://sts.googleapis.comé/v1/token"`), GCPKindUnsupportedToken},
		{"token_url other path", CredTypeGCPWIFConfig, rep(iacFileShape, tokenURL, `"https://sts.googleapis.com/other"`), GCPKindUnsupportedToken},
		{"token_url query", CredTypeGCPWIFConfig, rep(iacFileShape, tokenURL, `"https://sts.googleapis.com/v1/token?x=1"`), GCPKindUnsupportedToken},
		{"token_url null", CredTypeGCPWIFConfig, rep(iacFileShape, tokenURL, `null`), GCPKindUnsupportedToken},
		{"token_url number", CredTypeGCPWIFConfig, rep(iacFileShape, tokenURL, `5`), GCPKindUnsupportedToken},
		{"impersonation other host", CredTypeGCPWIFConfig, rep(iacFileShape, impURL, `"https://example.invalid/v1/projects/-/serviceAccounts/sa@proj.iam.gserviceaccount.com:generateAccessToken"`), GCPKindUnsupportedToken},
		{"impersonation other path", CredTypeGCPWIFConfig, rep(iacFileShape, impURL, `"https://iamcredentials.googleapis.com/v1/other"`), GCPKindUnsupportedToken},
		{"token_info_url other host", CredTypeGCPWIFConfig, rep(iacFileShape, `"type":"external_account"`, `"type":"external_account","token_info_url":"https://example.invalid/x"`), GCPKindUnsupportedToken},
		{"file outside prefix", CredTypeGCPWIFConfig, rep(iacFileShape, fileVal, `"/etc/unsupported-path"`), GCPKindUnsupportedSource},
		{"file traversal", CredTypeGCPWIFConfig, rep(iacFileShape, fileVal, `"/var/run/secrets/azure/tokens/../../../../etc/unsupported-path"`), GCPKindUnsupportedSource},
		{"file relative", CredTypeGCPWIFConfig, rep(iacFileShape, fileVal, `"var/run/secrets/azure/tokens/x"`), GCPKindUnsupportedSource},
		{"file sibling dir", CredTypeGCPWIFConfig, rep(iacFileShape, fileVal, `"/var/run/secrets/kubernetes/x"`), GCPKindUnsupportedSource},
		{"file prefix only", CredTypeGCPWIFConfig, rep(iacFileShape, fileVal, `"/var/run/secrets/azure/tokens/"`), GCPKindUnsupportedSource},
		{"file null", CredTypeGCPWIFConfig, rep(iacFileShape, fileVal, `null`), GCPKindUnsupportedSource},
		{"source url generic", CredTypeGCPWIFConfig, rep(iacFileShape, `{"file":`+fileVal+`}`, `{"url":"https://example.invalid/token"}`), GCPKindUnsupportedSource},
		{"source executable", CredTypeGCPWIFConfig, rep(iacFileShape, `{"file":`+fileVal+`}`, `{"executable":{"command":"unsupported"}}`), GCPKindUnsupportedSource},
		{"source file plus url", CredTypeGCPWIFConfig, rep(iacFileShape, `{"file":`+fileVal+`}`, `{"file":`+fileVal+`,"url":"https://example.invalid/token"}`), GCPKindUnsupportedSource},
		{"source empty", CredTypeGCPWIFConfig, rep(iacFileShape, `{"file":`+fileVal+`}`, `{}`), GCPKindUnsupportedSource},
		{"source null", CredTypeGCPWIFConfig, rep(iacFileShape, `{"file":`+fileVal+`}`, `null`), GCPKindUnsupportedSource},
		{"source string", CredTypeGCPWIFConfig, rep(iacFileShape, `{"file":`+fileVal+`}`, `"x"`), GCPKindUnsupportedSource},
		{"source format bad key", CredTypeGCPWIFConfig, rep(iacFileShape, `{"file":`+fileVal+`}`, `{"file":`+fileVal+`,"format":{"type":"text","x":"y"}}`), GCPKindUnsupportedSource},
		{"aws region_url other host", CredTypeGCPWIFConfig, rep(iacAWSShape, "http://169.254.169.254/latest/meta-data/placement", "http://example.invalid/latest/meta-data/placement"), GCPKindUnsupportedSource},
		{"aws url other host", CredTypeGCPWIFConfig, rep(iacAWSShape, "http://169.254.169.254/latest/meta-data/iam", "http://example.invalid/latest/meta-data/iam"), GCPKindUnsupportedSource},
		{"aws verification other host", CredTypeGCPWIFConfig, rep(iacAWSShape, "https://sts.{region}.amazonaws.com", "https://example.invalid"), GCPKindUnsupportedSource},
		{"aws verification suffix host", CredTypeGCPWIFConfig, rep(iacAWSShape, "sts.{region}.amazonaws.com?", "sts.{region}.amazonaws.com.example.invalid?"), GCPKindUnsupportedSource},
		{"aws verification placeholder in path", CredTypeGCPWIFConfig, rep(iacAWSShape, "https://sts.{region}.amazonaws.com?", "https://example.invalid/{region}?"), GCPKindUnsupportedSource},
		{"aws verification other query", CredTypeGCPWIFConfig, rep(iacAWSShape, "Action=GetCallerIdentity", "Action=Other"), GCPKindUnsupportedSource},
		{"aws imdsv2 other host", CredTypeGCPWIFConfig, rep(iacAWSShape, `"url":"http://169.254.169.254/latest/meta-data/iam/security-credentials"`, `"url":"http://169.254.169.254/latest/meta-data/iam/security-credentials","imdsv2_session_token_url":"http://example.invalid/latest/api/token"`), GCPKindUnsupportedSource},
		{"aws wrong environment", CredTypeGCPWIFConfig, rep(iacAWSShape, `"aws1"`, `"aws2"`), GCPKindUnsupportedSource},
		{"unknown top-level key", CredTypeGCPWIFConfig, rep(iacFileShape, `"type":"external_account"`, `"type":"external_account","extra_field":"x"`), GCPKindUnsupportedKey},
		{"case variant token_url", CredTypeGCPWIFConfig, rep(iacFileShape, `"token_url"`, `"Token_URL"`), GCPKindUnsupportedKey},
		{"case variant credential_source", CredTypeGCPWIFConfig, rep(iacFileShape, `"credential_source"`, `"CREDENTIAL_SOURCE"`), GCPKindUnsupportedKey},
		{"case variant nested file", CredTypeGCPWIFConfig, rep(iacFileShape, `"file"`, `"File"`), GCPKindUnsupportedSource},
		{"duplicate token_url", CredTypeGCPWIFConfig, rep(iacFileShape, `"type":"external_account"`, `"type":"external_account","token_url":"https://example.invalid/v1/token"`), GCPKindInvalidJSON},
		{"duplicate nested file", CredTypeGCPWIFConfig, rep(iacFileShape, `{"file":`+fileVal+`}`, `{"file":`+fileVal+`,"file":"/etc/unsupported-path"}`), GCPKindInvalidJSON},
		{"trailing content", CredTypeGCPWIFConfig, iacFileShape + `{}`, GCPKindInvalidJSON},
		{"trailing garbage", CredTypeGCPWIFConfig, iacFileShape + ` x`, GCPKindInvalidJSON},
		{"null document", CredTypeGCPWIFConfig, `null`, GCPKindInvalidJSON},
		{"array document", CredTypeGCPWIFConfig, `[]`, GCPKindInvalidJSON},
		{"not json", CredTypeGCPWIFConfig, `not json`, GCPKindInvalidJSON},
		{"empty", CredTypeGCPWIFConfig, ``, GCPKindInvalidJSON},
		{"type mismatch authorized_user", CredTypeGCPWIFConfig, `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`, GCPKindUnsupportedKey},
		{"type service_account under wif", CredTypeGCPWIFConfig, rep(iacFileShape, `"type":"external_account"`, `"type":"service_account"`), GCPKindOther},
		{"type external_account under sa", CredTypeGCPServiceAccount, rep(validSAJSON, `"type":"service_account"`, `"type":"external_account"`), GCPKindOther},
		{"type null", CredTypeGCPWIFConfig, rep(iacFileShape, `"type":"external_account"`, `"type":null`), GCPKindOther},
		{"missing audience", CredTypeGCPWIFConfig, rep(iacFileShape, `"audience":"//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/q",`, ``), GCPKindOther},
		{"audience null", CredTypeGCPWIFConfig, rep(iacFileShape, `"//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/q"`, `null`), GCPKindOther},
		{"universe_domain other", CredTypeGCPWIFConfig, rep(iacFileShape, `"type":"external_account"`, `"type":"external_account","universe_domain":"example.invalid"`), GCPKindOther},
		{"impersonation options not object", CredTypeGCPWIFConfig, rep(iacFileShape, `"type":"external_account"`, `"type":"external_account","service_account_impersonation":5`), GCPKindOther},
		{"impersonation lifetime not integer", CredTypeGCPWIFConfig, rep(iacFileShape, `"type":"external_account"`, `"type":"external_account","service_account_impersonation":{"token_lifetime_seconds":1.5}`), GCPKindOther},
		{"impersonation options extra key", CredTypeGCPWIFConfig, rep(iacFileShape, `"type":"external_account"`, `"type":"external_account","service_account_impersonation":{"x":1}`), GCPKindOther},
		{"sa token_uri other host", CredTypeGCPServiceAccount, rep(validSAJSON, "https://oauth2.googleapis.com/token", "https://example.invalid/token"), GCPKindUnsupportedToken},
		{"sa token_uri null", CredTypeGCPServiceAccount, rep(validSAJSON, `"https://oauth2.googleapis.com/token"`, `null`), GCPKindUnsupportedToken},
		{"sa case variant token_uri", CredTypeGCPServiceAccount, rep(validSAJSON, `"token_uri"`, `"Token_Uri"`), GCPKindUnsupportedKey},
		{"sa unknown key", CredTypeGCPServiceAccount, rep(validSAJSON, `"project_id"`, `"extra_field":"x","project_id"`), GCPKindUnsupportedKey},
		{"unknown credential type", "aws_access_keys", validSAJSON, GCPKindOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGCPCredentialJSON(tc.credType, []byte(tc.payload))
			var ge *GCPCredentialError
			require.ErrorAs(t, err, &ge)
			assert.Equal(t, tc.kind, ge.Kind)
			assert.NotContains(t, ge.Message, "example.invalid", "messages must not echo field values")
		})
	}
}
