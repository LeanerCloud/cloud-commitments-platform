// Guards the rename-proof CI/CD deploy trust (cloud-commitments-platform#2).
// GitHub's `repository` claim and default `sub` follow the repo's current
// name, so a trust keyed on owner/name breaks on a rename and can be claimed
// by a new repo that reuses a freed name. The ci-cd-permissions modules must
// match only the immutable repository_id / repository_owner_id, and the
// bootstrap script must configure GitHub to mint the subject shape the
// modules expect and gate every environment the modules trust.
package environments_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	subPrefixRef = "${local.github_oidc_sub_prefix}:"
	bootstrap    = "../../scripts/bootstrap-github-deploy-config.sh"
)

var (
	// Identifiers that tie trust to the repository name rather than its ID.
	nameKeyedTrust = []string{
		"var.github_repo}", "var.github_repo ", `"repo:`,
		"assertion.repository ==", "attribute.repository/",
	}
	subPrefixDecl = regexp.MustCompile(`github_oidc_sub_prefix\s*=\s*"([^"]+)"`)
	prefixClaim   = regexp.MustCompile(`([a-z_]+):\$\{var\.github_[a-z_]+\}`)
)

// readHCL returns the file with `#` comment lines removed, so prose that
// mentions the old subject shape cannot satisfy or trip a check.
func readHCL(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func assertNotNameKeyed(t *testing.T, path, src string) {
	t.Helper()
	for _, bad := range nameKeyedTrust {
		if strings.Contains(src, bad) {
			t.Errorf("%s: trust references the repository name via %q; key it on github_repository_id / github_repository_owner_id", path, bad)
		}
	}
}

// subPrefixClaims returns the claim keys, in order, of the module's
// github_oidc_sub_prefix local, after checking it binds both immutable IDs.
func subPrefixClaims(t *testing.T, path, src string) []string {
	t.Helper()
	m := subPrefixDecl.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("%s: no github_oidc_sub_prefix local", path)
	}
	const want = "repository_owner_id:${var.github_repository_owner_id}:repository_id:${var.github_repository_id}"
	if m[1] != want {
		t.Fatalf("%s: github_oidc_sub_prefix = %q, want %q", path, m[1], want)
	}
	var keys []string
	for _, c := range prefixClaim.FindAllStringSubmatch(m[1], -1) {
		keys = append(keys, c[1])
	}
	return keys
}

func assertPrefixed(t *testing.T, path string, subjects []string) {
	t.Helper()
	if len(subjects) == 0 {
		t.Fatalf("%s: found no trusted subjects to check", path)
	}
	for _, s := range subjects {
		if !strings.HasPrefix(s, subPrefixRef) {
			t.Errorf("%s: subject %q does not start with %s", path, s, subPrefixRef)
		}
	}
}

func quoted(block string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(block, -1) {
		out = append(out, m[1])
	}
	return out
}

func environmentsOf(subjects []string) []string {
	var envs []string
	for _, s := range subjects {
		if env, ok := strings.CutPrefix(s, subPrefixRef+"environment:"); ok {
			envs = append(envs, env)
		}
	}
	return envs
}

func bootstrapScript(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(bootstrap)
	if err != nil {
		t.Fatalf("reading %s: %v", bootstrap, err)
	}
	return string(data)
}

func bootstrapClaimKeys(t *testing.T, script string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^SUB_CLAIM_KEYS='([^']*)'$`).FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("%s: no SUB_CLAIM_KEYS assignment", bootstrap)
	}
	return m[1]
}

func bootstrapEnvironments(t *testing.T, script string) map[string]bool {
	t.Helper()
	m := regexp.MustCompile(`(?s)\nENVIRONMENTS=\(\n(.*?)\n\)`).FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("%s: no ENVIRONMENTS array", bootstrap)
	}
	envs := map[string]bool{}
	for _, e := range strings.Fields(m[1]) {
		envs[e] = true
	}
	return envs
}

func TestAWSDeployTrustIsKeyedOnRepositoryID(t *testing.T) {
	oidc := readHCL(t, "aws/ci-cd-permissions/github_oidc.tf")
	role := readHCL(t, "aws/ci-cd-permissions/role.tf")
	assertNotNameKeyed(t, "aws github_oidc.tf", oidc)
	assertNotNameKeyed(t, "aws role.tf", role)
	checkClaimKeysMatchBootstrap(t, "aws", subPrefixClaims(t, "aws github_oidc.tf", oidc))

	m := regexp.MustCompile(`(?s)"token\.actions\.githubusercontent\.com:sub"\s*=\s*\[(.*?)\]`).FindStringSubmatch(role)
	if m == nil {
		t.Fatal("aws role.tf: no token.actions.githubusercontent.com:sub condition")
	}
	subjects := quoted(m[1])
	assertPrefixed(t, "aws role.tf", subjects)
	checkEnvironmentsGated(t, "aws", environmentsOf(subjects))
}

func TestAzureDeployTrustIsKeyedOnRepositoryID(t *testing.T) {
	sp := readHCL(t, "azure/ci-cd-permissions/sp.tf")
	assertNotNameKeyed(t, "azure sp.tf", sp)
	checkClaimKeysMatchBootstrap(t, "azure", subPrefixClaims(t, "azure sp.tf", sp))

	var subjects []string
	for _, m := range regexp.MustCompile(`(?m)^\s*subject\s*=\s*"([^"]*)"`).FindAllStringSubmatch(sp, -1) {
		subjects = append(subjects, m[1])
	}
	assertPrefixed(t, "azure sp.tf", subjects)

	vars := readHCL(t, "azure/ci-cd-permissions/variables.tf")
	m := regexp.MustCompile(`(?s)variable "github_environments".*?default\s*=\s*\[([^\]]*)\]`).FindStringSubmatch(vars)
	if m == nil {
		t.Fatal("azure variables.tf: no github_environments default")
	}
	checkEnvironmentsGated(t, "azure", quoted(m[1]))
}

func TestGCPDeployTrustIsKeyedOnRepositoryID(t *testing.T) {
	src := readHCL(t, "gcp/ci-cd-permissions/github_oidc.tf")
	assertNotNameKeyed(t, "gcp github_oidc.tf", src)

	for _, want := range []string{
		`"attribute.repository_id"       = "assertion.repository_id"`,
		`"attribute.repository_owner_id" = "assertion.repository_owner_id"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("gcp github_oidc.tf: attribute_mapping missing %s", want)
		}
	}

	m := regexp.MustCompile(`attribute_condition\s*=\s*"([^"]*)"`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("gcp github_oidc.tf: no attribute_condition")
	}
	for _, want := range []string{
		"assertion.repository_owner_id == '${var.github_repository_owner_id}'",
		"assertion.repository_id == '${var.github_repository_id}'",
		"assertion.ref == '${var.deploy_ref}'",
	} {
		if !strings.Contains(m[1], want) {
			t.Errorf("gcp attribute_condition %q missing %q", m[1], want)
		}
	}
	if strings.Contains(m[1], "||") {
		t.Errorf("gcp attribute_condition %q contains ||; every clause must be required", m[1])
	}

	if !strings.Contains(src, `/attribute.repository_id/${var.github_repository_id}"`) {
		t.Error("gcp github_oidc.tf: workloadIdentityUser member must be the attribute.repository_id principalSet")
	}
}

// The bootstrap script's sub claim template decides the subject GitHub mints;
// the modules' prefix decides what the clouds accept. A mismatch fails every
// deploy with an opaque auth error.
func checkClaimKeysMatchBootstrap(t *testing.T, cloud string, prefixKeys []string) {
	t.Helper()
	want := `["` + strings.Join(append(prefixKeys, "context"), `","`) + `"]`
	if got := bootstrapClaimKeys(t, bootstrapScript(t)); got != want {
		t.Errorf("%s: bootstrap SUB_CLAIM_KEYS = %s, want %s to match github_oidc_sub_prefix", cloud, got, want)
	}
}

// An environment subject is ref-agnostic, so a trusted environment the
// bootstrap script does not limit to main can be deployed from any branch.
func checkEnvironmentsGated(t *testing.T, cloud string, envs []string) {
	t.Helper()
	if len(envs) == 0 {
		t.Fatalf("%s: found no trusted environments to check", cloud)
	}
	gated := bootstrapEnvironments(t, bootstrapScript(t))
	for _, e := range envs {
		if !gated[e] {
			t.Errorf("%s trusts environment %q, which %s does not limit to main", cloud, e, bootstrap)
		}
	}
}
