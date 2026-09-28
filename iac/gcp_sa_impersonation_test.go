package iac

import (
	"regexp"
	"slices"
	"testing"
)

// These tests assert that the two GCP onboarding bundles (gcp-sa-impersonation
// and gcp-target) grant an equivalent permission set to the CUDly service
// account, so they cannot drift back into granting different, organization-
// or billing-account-scoped roles at project scope (#129), and that neither
// silently drops a built-in role the other grants (also #129: missing
// roles/recommender.viewer doesn't fail the apply or any visible API call --
// providers/gcp/recommendations.go catches the resulting 403 and only
// warn-logs it, so an onboarded account silently returns zero GCP
// recommendations).

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

// extractVarDefaultList pulls the quoted string literals out of a
// `variable "<name>" { ... default = [ ... ] ... }` block in a federation
// module's variables.tf.
func extractVarDefaultList(t *testing.T, tf, varName, path string) []string {
	t.Helper()
	varRe := regexp.MustCompile(`(?s)variable "` + regexp.QuoteMeta(varName) + `" \{.*?default = \[(.*?)\]`)
	m := varRe.FindStringSubmatch(tf)
	if m == nil {
		t.Fatalf("%s: could not find variable %q default block", path, varName)
	}
	litRe := regexp.MustCompile(`"([^"]+)"`)
	lits := litRe.FindAllStringSubmatch(m[1], -1)
	values := make([]string, 0, len(lits))
	for _, l := range lits {
		values = append(values, l[1])
	}
	if len(values) == 0 {
		t.Fatalf("%s: variable %q default has no values", path, varName)
	}
	return values
}

func assertEquivalentLists(t *testing.T, a, b []string, aName, bName string) {
	t.Helper()
	sortedA := slices.Clone(a)
	slices.Sort(sortedA)
	sortedB := slices.Clone(b)
	slices.Sort(sortedB)
	if !slices.Equal(sortedA, sortedB) {
		t.Errorf("%s %v != %s %v; the two onboarding paths must grant the same permission set", aName, sortedA, bName, sortedB)
	}
}

// TestGCPBundlesGrantEquivalentCommitmentPermissions guards #129: the
// sa-impersonation and gcp-target bundles must converge on the same
// commitment-write custom-role permission set rather than one granting a
// broader, project-scope-invalid role.
func TestGCPBundlesGrantEquivalentCommitmentPermissions(t *testing.T) {
	saPerms := extractVarDefaultList(t, readModuleFile(t, gcpSAImpersonationVarsTF), "custom_role_permissions", gcpSAImpersonationVarsTF)
	targetPerms := extractVarDefaultList(t, readModuleFile(t, gcpTargetVarsTF), "custom_role_permissions", gcpTargetVarsTF)
	assertEquivalentLists(t, saPerms, targetPerms, "gcp-sa-impersonation custom_role_permissions", "gcp-target custom_role_permissions")
}

// TestGCPBundlesGrantEquivalentBuiltInRoles guards the same drift for the
// built-in project-scoped roles (roles/compute.viewer,
// roles/recommender.viewer): gcp-sa-impersonation's built_in_project_roles
// must match gcp-target's service_account_project_roles.
func TestGCPBundlesGrantEquivalentBuiltInRoles(t *testing.T) {
	saRoles := extractVarDefaultList(t, readModuleFile(t, gcpSAImpersonationVarsTF), "built_in_project_roles", gcpSAImpersonationVarsTF)
	targetRoles := extractVarDefaultList(t, readModuleFile(t, gcpTargetVarsTF), "service_account_project_roles", gcpTargetVarsTF)
	assertEquivalentLists(t, saRoles, targetRoles, "gcp-sa-impersonation built_in_project_roles", "gcp-target service_account_project_roles")

	for _, want := range []string{"roles/compute.viewer", "roles/recommender.viewer"} {
		if !slices.Contains(saRoles, want) {
			t.Errorf("gcp-sa-impersonation built_in_project_roles missing %s", want)
		}
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

	if !regexp.MustCompile(`resource "google_project_iam_custom_role" "cudly" \{`).MatchString(tf) {
		t.Errorf("%s: expected a google_project_iam_custom_role granting the minimum commitment-write permissions", gcpSAImpersonationMainTF)
	}
	if !regexp.MustCompile(`resource "google_project_iam_member" "cudly_built_in" \{`).MatchString(tf) {
		t.Errorf("%s: expected a google_project_iam_member granting var.built_in_project_roles for read access", gcpSAImpersonationMainTF)
	}
}
