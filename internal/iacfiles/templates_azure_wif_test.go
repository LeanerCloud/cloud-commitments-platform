package iacfiles

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/iac"
	"github.com/stretchr/testify/require"
)

func TestAzureWIFTerraformPlansOnly(t *testing.T) {
	data, err := iac.Modules.ReadFile("federation/azure-target/terraform/tests/identity_guards.tftest.hcl")
	require.NoError(t, err)
	runs := regexp.MustCompile(`(?m)^run "[^"]+" \{`).FindAll(data, -1)
	plans := regexp.MustCompile(`(?m)^run "[^"]+" \{\s*command\s*=\s*plan\s`).FindAll(data, -1)
	require.NotEmpty(t, runs)
	require.Len(t, plans, len(runs), "every mock run must explicitly start with command = plan")
	commands := regexp.MustCompile(`(?m)^\s*command\s*=\s*(\w+)`).FindAllSubmatch(data, -1)
	for _, command := range commands {
		require.Equal(t, "plan", string(command[1]), "apply is forbidden in this mock suite")
	}
}

func runAzureWIF(t *testing.T, template string, data testTemplateData, env map[string]string) (int, string, string, []string) {
	t.Helper()
	return runRenderedScript(t, "azure-wif.sh", renderCLITemplate(t, template, data), func(logPath string) map[string]string {
		return map[string]string{
			"az": fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
printf 'az %%s\n' "$*" >> %q
case "$1 $2" in
  'account set') ;;
  'account show') echo 11111111-1111-1111-1111-111111111111 ;;
  'ad app')
    if [[ "$3" == 'federated-credential' ]]; then
      [[ "${FAIL_CREDENTIAL:-}" == '' ]] || { echo "$FAIL_CREDENTIAL" >&2; exit 42; }
      while [[ $# -gt 0 ]]; do
        if [[ "$1" == '--parameters' ]]; then
          jq -c . <<< "$2" >> %q
          exit
        fi
        shift
      done
      exit 43
    fi
    echo 22222222-2222-2222-2222-222222222222 ;;
  'ad sp') echo 33333333-3333-3333-3333-333333333333 ;;
  'deployment sub') ;;
  *) exit 44 ;;
esac
`, logPath, logPath),
			"curl": fmt.Sprintf("#!/usr/bin/env bash\necho curl >> %q\necho 409\n", logPath),
		}
	}, env)
}

func TestAzureWIFInputGuards(t *testing.T) {
	for _, template := range []string{"templates/azure-wif-cli.sh.tmpl", "templates/azure-wif-deploy.sh.tmpl"} {
		t.Run(template, func(t *testing.T) {
			for _, key := range []string{"CUDLY_FEDERATED_SUBJECT", "CUDLY_FEDERATED_AUDIENCE"} {
				for _, value := range []string{"", "a b", "a\tb", "a\nb", "a\rb", "a\vb", "a\fb", "a$b", "a*b"} {
					t.Run(key+fmt.Sprintf("%q", value), func(t *testing.T) {
						code, _, stderr, calls := runAzureWIF(t, template, baseData(), map[string]string{key: value})
						require.NotZero(t, code)
						require.Empty(t, calls)
						require.Contains(t, stderr, key)
					})
				}
			}
			for _, value := range []string{"", "http://example.com/oidc", "https://", "https://example.com/oidc/", "https://user@example.com/oidc", "https://example.com/?q=1", "https://example.com/#f", "https://example.com/a b", "https://example.com/a\nb", `https://example.com/a"b`, `https://example.com/a\b`} {
				t.Run(fmt.Sprintf("issuer%q", value), func(t *testing.T) {
					code, _, stderr, calls := runAzureWIF(t, template, baseData(), map[string]string{"CUDLY_ISSUER_URL": value})
					require.NotZero(t, code)
					require.Empty(t, calls)
					require.Contains(t, stderr, "CUDLY_ISSUER_URL")
				})
			}
			t.Run("missing rendered issuer", func(t *testing.T) {
				data := baseData()
				data.CUDlyAPIURL = ""
				code, _, _, calls := runAzureWIF(t, template, data, nil)
				require.NotZero(t, code)
				require.Empty(t, calls)
			})
			t.Run("missing jq", func(t *testing.T) {
				code, _, stderr, calls := runAzureWIF(t, template, baseData(), map[string]string{"PATH": t.TempDir()})
				require.NotZero(t, code)
				require.Empty(t, calls)
				require.Contains(t, stderr, "jq is required")
			})
		})
	}
}

func TestAzureWIFFederatedCredential(t *testing.T) {
	for _, template := range []string{"templates/azure-wif-cli.sh.tmpl", "templates/azure-wif-deploy.sh.tmpl"} {
		t.Run(template, func(t *testing.T) {
			for _, failure := range []string{"Authorization_RequestDenied", "TooManyRequests", "Conflict"} {
				t.Run(failure, func(t *testing.T) {
					code, stdout, stderr, calls := runAzureWIF(t, template, baseData(), map[string]string{"FAIL_CREDENTIAL": failure})
					require.Equal(t, 42, code)
					require.Contains(t, stdout+stderr, failure)
					require.NotContains(t, stdout, "=== Done ===")
					require.NotContains(t, strings.Join(calls, "\n"), "az deployment")
					require.NotContains(t, calls, "curl")
				})
			}
			for _, literal := range []string{"", "quote\"backslash\\backtick`apostrophe':é"} {
				t.Run("literal/"+literal, func(t *testing.T) {
					issuer, subject, audience := baseData().CUDlyAPIURL+"/oidc", "cudly-controller", "api://AzureADTokenExchange"
					env := map[string]string{}
					if literal != "" {
						issuer, subject, audience = "https://CUDly.example.com:8443/a_b/~v1%20/oidc", literal, literal
						env = map[string]string{"CUDLY_ISSUER_URL": issuer, "CUDLY_FEDERATED_SUBJECT": subject, "CUDLY_FEDERATED_AUDIENCE": audience}
					}
					code, stdout, stderr, calls := runAzureWIF(t, template, baseData(), env)
					require.Zero(t, code, "%s", stderr)
					require.Contains(t, stdout, "=== Done ===")
					require.Contains(t, calls, "curl")
					if strings.Contains(template, "deploy") {
						require.Contains(t, strings.Join(calls, "\n"), "az deployment sub create")
					}
					var credentials []map[string]any
					for _, call := range calls {
						if strings.HasPrefix(call, "{") {
							var credential map[string]any
							require.NoError(t, json.Unmarshal([]byte(call), &credential))
							credentials = append(credentials, credential)
						}
					}
					require.Len(t, credentials, 1)
					require.Len(t, credentials[0], 5)
					require.Equal(t, "cudly", credentials[0]["name"])
					require.Equal(t, issuer, credentials[0]["issuer"])
					require.Equal(t, subject, credentials[0]["subject"])
					require.Equal(t, []any{audience}, credentials[0]["audiences"])
				})
			}
		})
	}
}
