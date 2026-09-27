// Regression test for issue #124: cleanup-function's source object pointed at
// a placeholder.zip that was never committed to the repository (a clean
// checkout could not apply the module at all), and its object name embedded
// timestamp(), which changes on every plan and would redeploy the Cloud
// Function from the placeholder archive on every unrelated apply, wiping out
// whatever real source was deployed out-of-band.
package gcp_test

import (
	"os"
	"regexp"
	"testing"
)

func TestCleanupFunctionSourceIsExternallySupplied(t *testing.T) {
	mainData, err := os.ReadFile("cleanup-function/main.tf")
	if err != nil {
		t.Fatalf("reading cleanup-function/main.tf: %v", err)
	}
	main := string(mainData)

	if regexp.MustCompile(`\btimestamp\s*\(`).MatchString(main) {
		t.Error(`cleanup-function/main.tf calls timestamp(); the source object name must be stable across plans (issue #124), not regenerated on every apply`)
	}

	if regexp.MustCompile(`resource\s+"google_storage_bucket_object"\s+"function_source"`).MatchString(main) {
		t.Error(`cleanup-function/main.tf still defines google_storage_bucket_object.function_source; the module must not own/generate the source archive (it previously pointed at an uncommitted placeholder.zip) -- take the object name as an externally-supplied variable instead`)
	}

	if !regexp.MustCompile(`object\s*=\s*var\.source_object_name`).MatchString(main) {
		t.Error(`cleanup-function/main.tf's storage_source block must set object = var.source_object_name, so the build/CI pipeline supplies the already-uploaded source archive instead of Terraform requiring a committed placeholder.zip`)
	}

	varsData, err := os.ReadFile("cleanup-function/variables.tf")
	if err != nil {
		t.Fatalf("reading cleanup-function/variables.tf: %v", err)
	}
	if !regexp.MustCompile(`variable\s+"source_object_name"\s*\{`).MatchString(string(varsData)) {
		t.Error(`cleanup-function/variables.tf must declare a source_object_name variable`)
	}

	if _, err := os.Stat("cleanup-function/placeholder.zip"); err == nil {
		t.Error(`cleanup-function/placeholder.zip exists but should not: the fix for issue #124 removes the module's dependency on a committed placeholder archive`)
	}
}
