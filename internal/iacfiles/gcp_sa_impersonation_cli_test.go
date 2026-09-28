package iacfiles

import (
	"strings"
	"testing"
)

// Regression test for #129: gcp-sa-impersonation-cli.sh.tmpl (the CLI
// equivalent of iac/federation/gcp-sa-impersonation/terraform) granted
// roles/commerceorgpolicy.commitmentAdmin (organization-scoped) and
// roles/billing.viewer (billing-account-scoped) at project scope, which
// gcloud rejects. This runs the rendered script against a recording `gcloud`
// stub and asserts the actual invocations it makes, not just the template
// text, so a broken template (e.g. a role name reverted, or the custom-role
// creation step dropped) is caught even if a `mustContain` check on the raw
// text would still pass.

// simpleGcloudStubScript returns a stand-in `gcloud` executable that logs
// every invocation (newline-folded) to logPath and exits 0, never contacting
// GCP. Unlike gcloudStubScript (templates_gcp_wif_test.go), this script
// makes no pool/provider describe/create calls that need state modeling, so
// a plain recorder is enough.
func simpleGcloudStubScript(logPath string) string {
	return "#!/usr/bin/env bash\n" +
		"args=\"$*\"\n" +
		"printf '%s\\n' \"${args//$'\\n'/ }\" >> '" + logPath + "'\n"
}

func TestGCPSAImpersonationCLI_GrantsLeastPrivilegeRoles(t *testing.T) {
	data := baseData()
	// Cleared so the auto-register block (curl to a real URL) is not
	// rendered; this test is about the IAM role-granting calls only.
	data.CUDlyAPIURL = ""
	data.ProjectID = "target-project"
	data.ServiceAccountEmail = "cudly@target-project.iam.gserviceaccount.com"
	rendered := renderCLITemplate(t, "templates/gcp-sa-impersonation-cli.sh.tmpl", data)

	exitCode, _, stderr, calls := runRenderedScript(t, "gcp-sa-impersonation-cli.sh", rendered,
		func(logPath string) map[string]string {
			return map[string]string{"gcloud": simpleGcloudStubScript(logPath)}
		},
		map[string]string{"SOURCE_SERVICE_ACCOUNT": "cudly-host@source-project.iam.gserviceaccount.com"},
	)
	if exitCode != 0 {
		t.Fatalf("rendered script exited %d, stderr: %s\ncalls: %v", exitCode, stderr, calls)
	}

	joined := strings.Join(calls, "\n")

	for _, forbidden := range []string{
		"roles/commerceorgpolicy.commitmentAdmin",
		"roles/billing.viewer",
	} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("rendered script still grants %s (org/billing-account-scoped; 400s at project scope, #129); calls:\n%s", forbidden, joined)
		}
	}

	if !strings.Contains(joined, "iam roles create cudlyCommitmentWriter") {
		t.Errorf("rendered script must create the cudlyCommitmentWriter custom role; calls:\n%s", joined)
	}
	if !strings.Contains(joined, "--permissions=compute.commitments.create,compute.commitments.update") {
		t.Errorf("rendered script's custom role must hold exactly compute.commitments.create/update; calls:\n%s", joined)
	}

	for _, wantRole := range []string{
		"--role=projects/target-project/roles/cudlyCommitmentWriter",
		"--role=roles/compute.viewer",
		"--role=roles/recommender.viewer",
	} {
		if !strings.Contains(joined, wantRole) {
			t.Errorf("rendered script must bind %s to the service account; calls:\n%s", wantRole, joined)
		}
	}
}

// roleCreateGcloudStubScript is a recording `gcloud` stub that models the
// `iam roles create|describe|undelete|update` and
// `projects add-iam-policy-binding` calls the CLI template makes, so the
// three failure-mode tests below can exercise branches
// TestGCPSAImpersonationCLI_GrantsLeastPrivilegeRoles's always-succeeds
// stub never reaches:
//
//   - STUB_ROLE_CREATE_MODE=deleted: `iam roles create` fails, `describe`
//     reports the role as soft-deleted -- the script must undelete it and
//     still converge permissions and bind.
//   - STUB_ROLE_CREATE_MODE=describe_fails: `iam roles create` fails and
//     `describe` also fails (role genuinely doesn't exist / permission
//     denied) -- the script must exit non-zero and never reach the
//     binding loop.
//   - STUB_FAIL_BINDING_ROLE=<role>: `projects add-iam-policy-binding`
//     fails for that one role -- the script must exit non-zero rather
//     than swallow it and print "=== Done ===".
func roleCreateGcloudStubScript(logPath string) string {
	return `#!/usr/bin/env bash
args="$*"
printf '%s\n' "${args//$'\n'/ }" >> '` + logPath + `'

case "$1 $2 $3" in
  "iam roles create")
    case "${STUB_ROLE_CREATE_MODE:-}" in
      deleted|describe_fails) exit 1 ;;
      *) exit 0 ;;
    esac
    ;;
  "iam roles describe")
    case "${STUB_ROLE_CREATE_MODE:-}" in
      deleted) echo "True"; exit 0 ;;
      describe_fails) echo "gcloud: role not found" >&2; exit 1 ;;
      *) echo "False"; exit 0 ;;
    esac
    ;;
  "iam roles undelete") exit 0 ;;
  "iam roles update") exit 0 ;;
esac

if [ "$1 $2" = "projects add-iam-policy-binding" ]; then
  for a in "$@"; do
    case "$a" in
      --role=*)
        if [ "${a#--role=}" = "${STUB_FAIL_BINDING_ROLE:-}" ]; then
          exit 1
        fi
        ;;
    esac
  done
  exit 0
fi

exit 0
`
}

func renderGCPSAImpersonationScript(t *testing.T) string {
	t.Helper()
	data := baseData()
	// Cleared so the auto-register block (curl to a real URL) is not
	// rendered; these tests are about the IAM role-granting calls only.
	data.CUDlyAPIURL = ""
	data.ProjectID = "target-project"
	data.ServiceAccountEmail = "cudly@target-project.iam.gserviceaccount.com"
	return renderCLITemplate(t, "templates/gcp-sa-impersonation-cli.sh.tmpl", data)
}

func TestGCPSAImpersonationCLI_SoftDeletedRole_UndeletesConvergesAndBinds(t *testing.T) {
	rendered := renderGCPSAImpersonationScript(t)
	exitCode, _, stderr, calls := runRenderedScript(t, "gcp-sa-impersonation-cli.sh", rendered,
		func(logPath string) map[string]string {
			return map[string]string{"gcloud": roleCreateGcloudStubScript(logPath)}
		},
		map[string]string{
			"SOURCE_SERVICE_ACCOUNT": "cudly-host@source-project.iam.gserviceaccount.com",
			"STUB_ROLE_CREATE_MODE":  "deleted",
		},
	)
	if exitCode != 0 {
		t.Fatalf("rendered script exited %d, stderr: %s\ncalls: %v", exitCode, stderr, calls)
	}
	joined := strings.Join(calls, "\n")

	if !strings.Contains(joined, "iam roles undelete cudlyCommitmentWriter") {
		t.Errorf("a soft-deleted role must be undeleted before use; calls:\n%s", joined)
	}
	if !strings.Contains(joined, "iam roles update cudlyCommitmentWriter") {
		t.Errorf("an existing role's permissions must be converged (gcloud iam roles update), not trusted as-is; calls:\n%s", joined)
	}
	for _, wantRole := range []string{
		"--role=projects/target-project/roles/cudlyCommitmentWriter",
		"--role=roles/compute.viewer",
		"--role=roles/recommender.viewer",
	} {
		if !strings.Contains(joined, wantRole) {
			t.Errorf("recovering from a soft-deleted role must still bind %s; calls:\n%s", wantRole, joined)
		}
	}
}

func TestGCPSAImpersonationCLI_RoleDescribeFails_ExitsNonZeroWithoutBinding(t *testing.T) {
	rendered := renderGCPSAImpersonationScript(t)
	exitCode, _, _, calls := runRenderedScript(t, "gcp-sa-impersonation-cli.sh", rendered,
		func(logPath string) map[string]string {
			return map[string]string{"gcloud": roleCreateGcloudStubScript(logPath)}
		},
		map[string]string{
			"SOURCE_SERVICE_ACCOUNT": "cudly-host@source-project.iam.gserviceaccount.com",
			"STUB_ROLE_CREATE_MODE":  "describe_fails",
		},
	)
	if exitCode == 0 {
		t.Fatalf("rendered script must exit non-zero when both role create and role describe fail; calls:\n%s", strings.Join(calls, "\n"))
	}
	if joined := strings.Join(calls, "\n"); strings.Contains(joined, "add-iam-policy-binding") {
		t.Errorf("a role create+describe failure must not fall through to binding roles it never confirmed exist; calls:\n%s", joined)
	}
}

func TestGCPSAImpersonationCLI_BindingFailure_ExitsNonZero(t *testing.T) {
	rendered := renderGCPSAImpersonationScript(t)
	exitCode, _, _, calls := runRenderedScript(t, "gcp-sa-impersonation-cli.sh", rendered,
		func(logPath string) map[string]string {
			return map[string]string{"gcloud": roleCreateGcloudStubScript(logPath)}
		},
		map[string]string{
			"SOURCE_SERVICE_ACCOUNT": "cudly-host@source-project.iam.gserviceaccount.com",
			"STUB_FAIL_BINDING_ROLE": "roles/recommender.viewer",
		},
	)
	if exitCode == 0 {
		t.Fatalf("rendered script must exit non-zero when a role binding fails, not print \"=== Done ===\" over a partially-granted service account; calls:\n%s", strings.Join(calls, "\n"))
	}
}
