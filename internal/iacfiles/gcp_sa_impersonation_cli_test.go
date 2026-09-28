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
