// Issue #1588: every compute module hands the API whether it schedules the
// fire_scheduled_purchases sweep as FIRE_SWEEP_SCHEDULED, so PUT /api/config
// can refuse a purchase delay that would never execute. The expression must
// be exactly the condition under which the module creates the sweep job.
package compute_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	fireSweepVar = `var.enable_fire_scheduled_purchases_schedule`
	fireSweepOn  = ` ? "true" : "false"`
)

var fireSweepEnv = map[string]string{
	"aws/lambda/main.tf":           fireSweepVar + fireSweepOn,
	"aws/fargate/main.tf":          fireSweepVar + fireSweepOn,
	"gcp/cloud-run/main.tf":        `var.enable_scheduled_tasks && ` + fireSweepVar + fireSweepOn,
	"gcp/gke/main.tf":              `var.enable_scheduled_tasks && ` + fireSweepVar + fireSweepOn,
	"azure/container-apps/main.tf": `var.enable_scheduled_tasks && ` + fireSweepVar + fireSweepOn,
	// AKS gates its sweep jobs on deploy_kubernetes_resources (scheduled-tasks.tf).
	"azure/aks/main.tf": `var.deploy_kubernetes_resources && ` + fireSweepVar + fireSweepOn,
}

var (
	fireSweepMapRe  = regexp.MustCompile(`(?m)^\s*FIRE_SWEEP_SCHEDULED\s*=\s*(.+?)\s*$`)
	fireSweepListRe = regexp.MustCompile(`(?s)name\s*=\s*"FIRE_SWEEP_SCHEDULED"\s*,?\s*value\s*=\s*([^\n]+?)\s*(?:,|\n)`)
)

func TestFireSweepEnvWiring(t *testing.T) {
	for file, want := range fireSweepEnv {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			src := stripComments(string(raw))
			var got []string
			for _, re := range []*regexp.Regexp{fireSweepMapRe, fireSweepListRe} {
				for _, m := range re.FindAllStringSubmatch(src, -1) {
					got = append(got, strings.TrimSpace(m[1]))
				}
			}
			if len(got) != 1 {
				t.Fatalf("want exactly one FIRE_SWEEP_SCHEDULED env entry, got %d: %q", len(got), got)
			}
			if got[0] != want {
				t.Errorf("FIRE_SWEEP_SCHEDULED = %s; want %s", got[0], want)
			}
		})
	}
}
