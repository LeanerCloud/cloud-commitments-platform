package credentials

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyGCPCredential(t *testing.T) {
	cases := []struct {
		name, credType, payload, want string
	}{
		{"aws shape", CredTypeGCPWIFConfig, iacAWSShape, "ok"},
		{"file shape", CredTypeGCPWIFConfig, iacFileShape, "ok"},
		{"service account", CredTypeGCPServiceAccount, validSAJSON, "ok"},
		{"token endpoint", CredTypeGCPWIFConfig, strings.Replace(iacFileShape, "https://sts.googleapis.com/v1/token", "https://example.invalid/token", 1), "unsupported-token-endpoint"},
		{"credential source", CredTypeGCPWIFConfig, strings.Replace(iacFileShape, "/var/run/secrets/azure/tokens/azure-identity-token", "/etc/unsupported-path", 1), "unsupported-credential-source"},
		{"unknown key", CredTypeGCPWIFConfig, strings.Replace(iacFileShape, `"type":"external_account"`, `"type":"external_account","extra_field":"x"`, 1), "unsupported-key"},
		{"invalid json", CredTypeGCPWIFConfig, "not json", "invalid-json"},
		{"other", CredTypeGCPWIFConfig, strings.Replace(iacFileShape, `"type":"external_account"`, `"type":"service_account"`, 1), "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ClassifyGCPCredential(tc.credType, []byte(tc.payload)))
		})
	}
}

func TestFormatGCPAuditLine_ContainsNoPayloadContent(t *testing.T) {
	payload := strings.Replace(iacFileShape, "https://sts.googleapis.com/v1/token", "https://example.invalid/token", 1)
	line := FormatGCPAuditLine("account", "id-1", CredTypeGCPWIFConfig, ClassifyGCPCredential(CredTypeGCPWIFConfig, []byte(payload)))
	for _, frag := range []string{"example.invalid", "https://", "/var/run", "audience", "sts.googleapis.com"} {
		assert.NotContains(t, line, frag)
	}
	assert.Equal(t, "account\tid-1\tgcp_workload_identity_config\tunsupported-token-endpoint", line)
}
