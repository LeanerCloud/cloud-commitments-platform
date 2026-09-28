package iac

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// These tests assert that the two GCP onboarding bundles (gcp-sa-impersonation
// and gcp-target) grant an equivalent permission set to the CUDly service
// account, so they cannot drift back into granting different, organization-
// or billing-account-scoped roles at project scope (#129).

const (
	gcpSAImpersonationMainTF = "federation/gcp-sa-impersonation/terraform/main.tf"
	gcpSAImpersonationVarsTF = "federation/gcp-sa-impersonation/terraform/variables.tf"
	gcpTargetVarsTF          = "federation/gcp-target/terraform/variables.tf"
)

func readModuleFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := Modules.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// extractCustomRolePermissions pulls the quoted string literals out of the
// `variable "custom_role_permissions" { ... default = [ ... ] }` block in a
// federation module's variables.tf.
func extractCustomRolePermissions(t *testing.T, tf, path string) []string {
	t.Helper()
	varRe := regexp.MustCompile(`(?s)variable "custom_role_permissions" \{.*?default = \[(.*?)\]`)
	m := varRe.FindStringSubmatch(tf)
	if m == nil {
		t.Fatalf("%s: could not find variable \"custom_role_permissions\" default block", path)
	}
	litRe := regexp.MustCompile(`"([^"]+)"`)
	lits := litRe.FindAllStringSubmatch(m[1], -1)
	perms := make([]string, 0, len(lits))
	for _, l := range lits {
		perms = append(perms, l[1])
	}
	if len(perms) == 0 {
		t.Fatalf("%s: custom_role_permissions default has no permissions", path)
	}
	return perms
}

// TestGCPBundlesGrantEquivalentCommitmentPermissions guards #129: the
// sa-impersonation and gcp-target bundles must converge on the same
// commitment-write permission set rather than one granting a broader,
// project-scope-invalid role.
func TestGCPBundlesGrantEquivalentCommitmentPermissions(t *testing.T) {
	saImpersonationPerms := extractCustomRolePermissions(t, readModuleFile(t, gcpSAImpersonationVarsTF), gcpSAImpersonationVarsTF)
	targetPerms := extractCustomRolePermissions(t, readModuleFile(t, gcpTargetVarsTF), gcpTargetVarsTF)

	sortedSA := slices.Clone(saImpersonationPerms)
	slices.Sort(sortedSA)
	sortedTarget := slices.Clone(targetPerms)
	slices.Sort(sortedTarget)

	if !slices.Equal(sortedSA, sortedTarget) {
		t.Errorf("gcp-sa-impersonation custom_role_permissions %v != gcp-target custom_role_permissions %v; the two onboarding paths must grant the same permission set",
			sortedSA, sortedTarget)
	}
}

// TestGCPSAImpersonationDoesNotGrantInvalidProjectScopeRoles guards the two
// broken grants #129 reported: roles/commerceorgpolicy.commitmentAdmin is
// organization-scoped and roles/billing.viewer is billing-account-scoped, so
// granting either via google_project_iam_member (project scope) 400s. Neither
// may reappear in the bundle's main.tf.
func TestGCPSAImpersonationDoesNotGrantInvalidProjectScopeRoles(t *testing.T) {
	tf := readModuleFile(t, gcpSAImpersonationMainTF)

	// Match an actual `role = "roles/..."` assignment, not prose (this file's
	// own comments name both roles to explain why they were removed).
	if regexp.MustCompile(`role\s*=\s*"roles/commerceorgpolicy\.commitmentAdmin"`).MatchString(tf) {
		t.Errorf("%s: grants roles/commerceorgpolicy.commitmentAdmin, which is organization-scoped and 400s at project scope", gcpSAImpersonationMainTF)
	}
	if regexp.MustCompile(`role\s*=\s*"roles/billing\.viewer"`).MatchString(tf) {
		t.Errorf("%s: grants roles/billing.viewer, which is billing-account-scoped and 400s at project scope (see terraform/modules/compute/gcp/cloud-run/main.tf for the correct google_billing_account_iam_member shape if a billing read path is ever added here)", gcpSAImpersonationMainTF)
	}

	if !strings.Contains(tf, `resource "google_project_iam_custom_role" "cudly"`) {
		t.Errorf("%s: expected a google_project_iam_custom_role granting the minimum commitment-write permissions", gcpSAImpersonationMainTF)
	}
	if !strings.Contains(tf, `role    = "roles/compute.viewer"`) {
		t.Errorf("%s: expected roles/compute.viewer for read access (regions/zones/machineTypes/commitments.list/.get)", gcpSAImpersonationMainTF)
	}
}
