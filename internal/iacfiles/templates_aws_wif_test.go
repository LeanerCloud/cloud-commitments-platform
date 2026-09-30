package iacfiles

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const awsWIFProvider = "arn:aws:iam::123456789012:oidc-provider/accounts.google.com"

func awsWIFStub(logPath string) string {
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
jq -cn --args '$ARGS.positional' -- "$@" >> %q
[[ "$1 $2" != "${FAIL_OPERATION:-}" ]] || exit 42
case "$1 $2" in
  'iam list-open-id-connect-providers')
    if [[ "$*" == *'--output text'* ]]; then
      jq -r --arg host "$TEST_HOST" '[.OpenIDConnectProviderList[].Arn | select(contains($host))][0] // "None"' <<< "$LIST_RESPONSE"
    else
      printf '%%s\n' "$LIST_RESPONSE"
    fi ;;
  'iam create-open-id-connect-provider') printf '%%s\n' "$CREATE_RESPONSE" ;;
  'iam get-open-id-connect-provider') printf '%%s\n' "$PROVIDER_RESPONSE" ;;
  'iam get-role') echo 'arn:aws:iam::123456789012:role/CUDly' ;;
  'sts get-caller-identity') echo 123456789012 ;;
  'iam create-role'|'iam put-role-policy') echo '{}' ;;
  *) exit 43 ;;
esac
`, logPath)
}

func runAWSWIF(t *testing.T, data testTemplateData, overrides map[string]string) (int, string, string, [][]string) {
	t.Helper()
	host := strings.TrimPrefix(data.OIDCIssuerURL, "https://")
	arn := "arn:aws:iam::123456789012:oidc-provider/" + host
	provider, err := json.Marshal(map[string]any{
		"Url": host, "ClientIDList": []string{data.OIDCAudience, "sts.amazonaws.com"},
		"ThumbprintList": []string{strings.Repeat("a", 40)},
	})
	require.NoError(t, err)
	env := map[string]string{
		"TEST_HOST": host, "LIST_RESPONSE": `{"OpenIDConnectProviderList":[]}`,
		"CREATE_RESPONSE":   fmt.Sprintf(`{"OpenIDConnectProviderArn":%q}`, arn),
		"PROVIDER_RESPONSE": string(provider),
	}
	for key, value := range overrides {
		env[key] = value
	}
	code, stdout, stderr, lines := runRenderedScript(t, "aws-wif-cli.sh",
		renderCLITemplate(t, "templates/aws-wif-cli.sh.tmpl", data),
		func(logPath string) map[string]string {
			return map[string]string{"aws": awsWIFStub(logPath), "curl": fmt.Sprintf("#!/usr/bin/env bash\njq -cn --args '[\"curl\"] + $ARGS.positional' -- \"$@\" >> %q\necho 409\n", logPath)}
		}, env)
	calls := make([][]string, 0, len(lines))
	for _, line := range lines {
		var args []string
		require.NoError(t, json.Unmarshal([]byte(line), &args))
		calls = append(calls, args)
	}
	return code, stdout, stderr, calls
}

func awsWIFData() testTemplateData {
	data := baseData()
	data.OIDCIssuerURL = "https://accounts.google.com"
	data.OIDCAudience = "sts.amazonaws.com"
	data.OIDCSubjectClaim = "123456789012345678901"
	return data
}

func awsWIFOperation(calls [][]string, operation string) []string {
	for _, call := range calls {
		if len(call) > 1 && call[1] == operation {
			return call
		}
	}
	return nil
}

func awsWIFArgument(t *testing.T, call []string, flag string) string {
	t.Helper()
	for i, arg := range call {
		if arg == flag && i+1 < len(call) {
			return call[i+1]
		}
	}
	t.Fatalf("missing %s in %v", flag, call)
	return ""
}

func TestAWSWIFCLIProviderGuards(t *testing.T) {
	listed := `{"OpenIDConnectProviderList":[{"Arn":"` + awsWIFProvider + `.evil.example"},{"Arn":"` + awsWIFProvider + `/other"},{"Arn":"` + awsWIFProvider + `"}]}`
	cases := []struct {
		name           string
		env            map[string]string
		create, reject bool
	}{
		{"absent", nil, true, false},
		{"exact with lookalikes", map[string]string{"LIST_RESPONSE": listed}, false, false},
		{"only lookalike", map[string]string{"LIST_RESPONSE": `{"OpenIDConnectProviderList":[{"Arn":"` + awsWIFProvider + `.evil.example"}]}`}, true, false},
		{"list denied", map[string]string{"FAIL_OPERATION": "iam list-open-id-connect-providers"}, false, true},
		{"create denied", map[string]string{"FAIL_OPERATION": "iam create-open-id-connect-provider"}, true, true},
		{"get denied", map[string]string{"FAIL_OPERATION": "iam get-open-id-connect-provider"}, true, true},
		{"missing audience", map[string]string{"LIST_RESPONSE": listed, "PROVIDER_RESPONSE": `{"Url":"accounts.google.com","ClientIDList":["other"],"ThumbprintList":[]}`}, false, true},
		{"wrong URL", map[string]string{"PROVIDER_RESPONSE": `{"Url":"accounts.google.com.evil","ClientIDList":["sts.amazonaws.com"],"ThumbprintList":[]}`}, true, true},
		{"zero thumbprint", map[string]string{"PROVIDER_RESPONSE": `{"Url":"accounts.google.com","ClientIDList":["sts.amazonaws.com"],"ThumbprintList":["0000000000000000000000000000000000000000"]}`}, true, true},
		{"malformed list", map[string]string{"LIST_RESPONSE": `not-json`}, false, true},
		{"malformed provider", map[string]string{"PROVIDER_RESPONSE": `{}`}, true, true},
		{"wrong created ARN", map[string]string{"CREATE_RESPONSE": `{"OpenIDConnectProviderArn":"` + awsWIFProvider + `.evil"}`}, true, true},
		{"duplicate exact", map[string]string{"LIST_RESPONSE": `{"OpenIDConnectProviderList":[{"Arn":"` + awsWIFProvider + `"},{"Arn":"` + awsWIFProvider + `"}]}`}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr, calls := runAWSWIF(t, awsWIFData(), tc.env)
			require.Equal(t, tc.create, awsWIFOperation(calls, "create-open-id-connect-provider") != nil, "%v", calls)
			if tc.reject {
				require.NotZero(t, code, "stdout=%s stderr=%s", stdout, stderr)
				require.Nil(t, awsWIFOperation(calls, "create-role"))
				require.Nil(t, awsWIFOperation(calls, "put-role-policy"))
				require.NotContains(t, stdout, "=== Done ===")
				for _, call := range calls {
					require.NotEqual(t, "curl", call[0], "registration after failed provider verification")
				}
				return
			}
			require.Zero(t, code, "%s", stderr)
			if tc.create {
				require.NotContains(t, awsWIFOperation(calls, "create-open-id-connect-provider"), "--thumbprint-list")
			}
			require.Equal(t, awsWIFProvider, awsWIFArgument(t, awsWIFOperation(calls, "get-open-id-connect-provider"), "--open-id-connect-provider-arn"))
			var policy map[string]any
			require.NoError(t, json.Unmarshal([]byte(awsWIFArgument(t, awsWIFOperation(calls, "create-role"), "--assume-role-policy-document")), &policy))
			want := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Federated":"` + awsWIFProvider + `"},"Action":"sts:AssumeRoleWithWebIdentity","Condition":{"StringEquals":{"accounts.google.com:aud":"sts.amazonaws.com","accounts.google.com:sub":"123456789012345678901"}}}]}`
			actual, err := json.Marshal(policy)
			require.NoError(t, err)
			require.JSONEq(t, want, string(actual))
		})
	}
}

func TestAWSWIFCLIInputGuards(t *testing.T) {
	for _, key := range []string{"OIDC_AUDIENCE", "OIDC_SUBJECT_CLAIM"} {
		for _, invalid := range []string{"a b", "a\tb", "a\nb", "a\rb", "a\vb", "a\fb", "a$b", "a*b"} {
			t.Run(key+"/"+fmt.Sprintf("%q", invalid), func(t *testing.T) {
				code, _, stderr, calls := runAWSWIF(t, awsWIFData(), map[string]string{key: invalid})
				require.NotZero(t, code)
				require.Empty(t, calls)
				require.Contains(t, stderr, key)
			})
		}
	}
	for _, issuer := range []string{"http://accounts.google.com", "https://accounts.google.com/", "https://"} {
		t.Run(issuer, func(t *testing.T) {
			code, _, stderr, calls := runAWSWIF(t, awsWIFData(), map[string]string{"OIDC_ISSUER_URL": issuer})
			require.NotZero(t, code)
			require.Empty(t, calls)
			require.Contains(t, stderr, "OIDC_ISSUER_URL")
		})
	}
	t.Run("missing jq", func(t *testing.T) {
		code, _, stderr, calls := runAWSWIF(t, awsWIFData(), map[string]string{"PATH": t.TempDir()})
		require.NotZero(t, code)
		require.Empty(t, calls)
		require.Contains(t, stderr, "jq is required")
	})
}

func TestAWSWIFCLIAudienceAndLiteralClaims(t *testing.T) {
	for _, tc := range []struct {
		name, rendered, want string
		override             map[string]string
	}{
		{"Azure default", "api://AzureADTokenExchange", "api://AzureADTokenExchange", nil},
		{"rendered empty", "", "sts.amazonaws.com", nil},
		{"explicit empty", "api://AzureADTokenExchange", "sts.amazonaws.com", map[string]string{"OIDC_AUDIENCE": ""}},
		{"literal override", "default", `a"b\c`, map[string]string{"OIDC_AUDIENCE": `a"b\c`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := awsWIFData()
			data.OIDCAudience = tc.rendered
			if tc.name == "Azure default" {
				data.Source = "azure"
				data.OIDCIssuerURL = "https://login.microsoftonline.com/11111111-1111-1111-1111-111111111111/v2.0"
			}
			host := strings.TrimPrefix(data.OIDCIssuerURL, "https://")
			env := map[string]string{"OIDC_SUBJECT_CLAIM": `a"b\c`}
			for k, v := range tc.override {
				env[k] = v
			}
			body, err := json.Marshal(map[string]any{"Url": host, "ClientIDList": []string{tc.want}, "ThumbprintList": []string{strings.Repeat("a", 40)}})
			require.NoError(t, err)
			env["PROVIDER_RESPONSE"] = string(body)
			code, _, stderr, calls := runAWSWIF(t, data, env)
			require.Zero(t, code, "%s", stderr)
			require.Equal(t, tc.want, awsWIFArgument(t, awsWIFOperation(calls, "create-open-id-connect-provider"), "--client-id-list"))
			var policy struct {
				Statement []struct {
					Condition struct{ StringEquals map[string]string }
				}
			}
			require.NoError(t, json.Unmarshal([]byte(awsWIFArgument(t, awsWIFOperation(calls, "create-role"), "--assume-role-policy-document")), &policy))
			require.Len(t, policy.Statement, 1)
			require.Equal(t, map[string]string{host + ":aud": tc.want, host + ":sub": `a"b\c`}, policy.Statement[0].Condition.StringEquals)
		})
	}
}
