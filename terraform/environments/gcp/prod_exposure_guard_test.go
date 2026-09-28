// Regression test for issue #128: prod's Cloud Run service is
// unauthenticated (allow_unauthenticated derives from enable_cdn) and
// internet-facing (cloud_run_ingress = INGRESS_TRAFFIC_ALL) with no
// network-layer gate, while enable_cloud_armor = true falsely told an
// operator reading github-prod.tfvars that a WAF protects the API --
// module.frontend, the only place Cloud Armor is attached, only exists
// when enable_cdn = true (frontend.tf).
package gcp_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// tfvarsBool extracts a top-level `key = true|false` assignment from a
// .tfvars file's raw contents. Fails the test if the key is absent so a
// renamed/removed key is caught rather than silently treated as false.
func tfvarsBool(t *testing.T, path, key string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `\s*=\s*(true|false)\s*$`)
	m := re.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("%s: could not find top-level %q assignment", path, key)
	}
	return m[1] == "true"
}

func TestProdAndStagingDoNotAdvertiseInertCloudArmor(t *testing.T) {
	for _, tfvars := range []string{"github-prod.tfvars", "github-staging.tfvars"} {
		enableCDN := tfvarsBool(t, tfvars, "enable_cdn")
		enableCloudArmor := tfvarsBool(t, tfvars, "enable_cloud_armor")

		if enableCloudArmor && !enableCDN {
			t.Errorf("%s: enable_cloud_armor = true with enable_cdn = false; module.frontend (the only place Cloud Armor is attached) does not exist in this configuration, so this setting protects nothing while claiming to (#128)", tfvars)
		}
	}
}

func TestConfigurationGuardsPresent(t *testing.T) {
	data, err := os.ReadFile("checks.tf")
	if err != nil {
		t.Fatalf("reading checks.tf: %v", err)
	}
	checks := string(data)

	for _, want := range []string{
		`check "prod_requires_network_authenticated_ingress"`,
		`check "cloud_armor_must_sit_in_the_request_path"`,
		`check "cdn_requires_restricted_ingress"`,
		`check "prod_cdn_requires_cloud_armor"`,
	} {
		if !strings.Contains(checks, want) {
			t.Errorf("checks.tf missing %s (#128 continuous-validation guard)", want)
		}
	}

	if !regexp.MustCompile(`condition\s*=\s*!\(var\.environment\s*==\s*"prod"\s*&&\s*!var\.enable_cdn\)`).MatchString(checks) {
		t.Error(`checks.tf: prod_requires_network_authenticated_ingress must assert !(var.environment == "prod" && !var.enable_cdn)`)
	}
	if !regexp.MustCompile(`condition\s*=\s*!\(var\.enable_cloud_armor\s*&&\s*!var\.enable_cdn\)`).MatchString(checks) {
		t.Error(`checks.tf: cloud_armor_must_sit_in_the_request_path must assert !(var.enable_cloud_armor && !var.enable_cdn)`)
	}
	if !regexp.MustCompile(`condition\s*=\s*!\(var\.enable_cdn\s*&&\s*var\.cloud_run_ingress\s*==\s*"INGRESS_TRAFFIC_ALL"\)`).MatchString(checks) {
		t.Error(`checks.tf: cdn_requires_restricted_ingress must assert !(var.enable_cdn && var.cloud_run_ingress == "INGRESS_TRAFFIC_ALL")`)
	}
	if !regexp.MustCompile(`condition\s*=\s*!\(var\.environment\s*==\s*"prod"\s*&&\s*var\.enable_cdn\s*&&\s*!var\.enable_cloud_armor\)`).MatchString(checks) {
		t.Error(`checks.tf: prod_cdn_requires_cloud_armor must assert !(var.environment == "prod" && var.enable_cdn && !var.enable_cloud_armor)`)
	}
}
