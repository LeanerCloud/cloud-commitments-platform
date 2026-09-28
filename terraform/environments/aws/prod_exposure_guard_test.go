// Regression test for issue #128 (AWS half): prod's Lambda Function URL is
// unauthenticated (lambda_function_url_auth_type derives from enable_cdn,
// compute.tf) with no network-layer gate whenever enable_cdn = false. This
// mirrors terraform/environments/gcp/checks.tf's identical guard for the
// GCP half of the same defect.
package aws_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestAWSConfigurationGuardPresent(t *testing.T) {
	data, err := os.ReadFile("checks.tf")
	if err != nil {
		t.Fatalf("reading checks.tf: %v", err)
	}
	checks := string(data)

	if !strings.Contains(checks, `check "prod_requires_network_authenticated_ingress"`) {
		t.Fatal(`checks.tf missing check "prod_requires_network_authenticated_ingress" (#128 continuous-validation guard)`)
	}
	if !regexp.MustCompile(`condition\s*=\s*!\(var\.environment\s*==\s*"prod"\s*&&\s*!var\.enable_cdn\)`).MatchString(checks) {
		t.Error(`checks.tf: prod_requires_network_authenticated_ingress must assert !(var.environment == "prod" && !var.enable_cdn)`)
	}
}
