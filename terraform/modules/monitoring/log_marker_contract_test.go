// Contract test for issue #714: the per-cloud monitoring modules alert on
// marker tokens the Go code logs when a purchase execution or a scheduled task
// fails. A rename on either side would silently stop the alerts, so every
// module must match the exact tokens the code emits.
package monitoring_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/server"
)

func TestMonitoringLogMarkersMatchGo(t *testing.T) {
	markers := map[string]string{
		"purchase.PurchaseFailedLogMarker":    purchase.PurchaseFailedLogMarker,
		"server.ScheduledTaskFailedLogMarker": server.ScheduledTaskFailedLogMarker,
	}
	// Each module's failure_markers map must carry the token as a `token = "..."`
	// attribute, so a token that merely appears in a comment does not count.
	for _, file := range []string{"aws/failure_alerts.tf", "gcp/failure_alerts.tf", "azure/failure_alerts.tf"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		src := string(data)
		for name, token := range markers {
			if token == "" {
				t.Fatalf("%s is empty", name)
			}
			re := regexp.MustCompile(`(?m)^\s*token\s*=\s*"` + regexp.QuoteMeta(token) + `"\s*$`)
			if !re.MatchString(src) {
				t.Errorf("%s: no failure_markers entry with token = %q (%s)", file, token, name)
			}
		}
	}
}

// The ladder alarm matches the free-text ERROR lines of the ladder fire task
// (issue #70), so every quoted term of its filter pattern must appear verbatim
// in the Go source that logs them.
func TestLadderAlarmPatternTermsMatchGo(t *testing.T) {
	tf, err := os.ReadFile("aws/ladder_alerts.tf")
	if err != nil {
		t.Fatalf("reading ladder_alerts.tf: %v", err)
	}
	goSrc, err := os.ReadFile("../../../internal/purchase/ladder_fire_notice.go")
	if err != nil {
		t.Fatalf("reading ladder_fire_notice.go: %v", err)
	}
	line := regexp.MustCompile(`(?m)^\s*ladder_failure_pattern\s*=\s*"(.*)"\s*$`).FindStringSubmatch(string(tf))
	if line == nil {
		t.Fatal("ladder_alerts.tf: no ladder_failure_pattern assignment")
	}
	terms := regexp.MustCompile(`\\"([^"\\]+)\\"`).FindAllStringSubmatch(line[1], -1)
	if len(terms) != 3 {
		t.Fatalf("ladder_failure_pattern: want 3 quoted terms, got %d in %q", len(terms), line[1])
	}
	for _, m := range terms {
		if !strings.Contains(string(goSrc), m[1]) {
			t.Errorf("ladder_failure_pattern term %q is not in ladder_fire_notice.go", m[1])
		}
	}
}
