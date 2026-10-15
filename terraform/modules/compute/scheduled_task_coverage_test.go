// Coverage test for issue #712: every task in the server roster must be wired
// to a scheduler on every platform, or be listed in knownGaps with the reason
// it is not yet. The contract test next to it checks the other direction
// (every scheduler reference names a real task).
package compute_test

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/server"
)

// exemptTasks are roster tasks no platform has to schedule.
var exemptTasks = map[string]string{
	"analytics_refresh":   "analytics_collect already refreshes the views",
	"ri_exchange_reshape": "opt-in feature, off by default",
	"ladder_run":          "opt-in feature, off by default",
}

// optInRequired tasks must be wired but default to off (no live purchasing or
// retention change by default). Flipping cleanup to default-on later means
// removing it from this set.
var optInRequired = map[string]bool{
	"fire_scheduled_purchases":    true,
	"process_scheduled_purchases": true,
	"send_notifications":          true,
	"cleanup":                     true,
}

const dimTask = "missingTask"

var (
	validDims = []string{dimTask}
	allEight  = []string{"analytics_collect", "cleanup", "collect_recommendations", "finalize_revocations",
		"fire_scheduled_purchases", "process_scheduled_purchases", "reap_stuck_purchases", "send_notifications"}
	sevenGCP = []string{"analytics_collect", "cleanup", "finalize_revocations", "fire_scheduled_purchases",
		"process_scheduled_purchases", "reap_stuck_purchases", "send_notifications"}
)

// knownGaps documents what is not wired yet, per platform and dimension. Each
// PR that wires a task removes it here; the test fails when actual != documented.
var knownGaps = map[string]map[string][]string{
	"aws-lambda": {dimTask: {"cleanup", "finalize_revocations", "process_scheduled_purchases", "send_notifications"}},
	"aws-fargate": {dimTask: {"analytics_collect", "cleanup", "finalize_revocations",
		"process_scheduled_purchases", "reap_stuck_purchases", "send_notifications"}},
	"cloudformation": {dimTask: {"analytics_collect", "cleanup", "finalize_revocations",
		"fire_scheduled_purchases", "reap_stuck_purchases", "send_notifications"}},
	"gcp-cloud-run": {dimTask: sevenGCP},
	"gcp-gke":       {dimTask: sevenGCP},
	"azure-container-apps": {dimTask: {"analytics_collect", "finalize_revocations", "fire_scheduled_purchases",
		"process_scheduled_purchases", "reap_stuck_purchases", "send_notifications"}},
	"azure-aks": {dimTask: allEight}, // no scheduler resources at all: #893
}

type platform struct{ name, dir string }

var platforms = []platform{
	{name: "aws-lambda", dir: "aws/lambda"},
	{name: "aws-fargate", dir: "aws/fargate"},
	{name: "cloudformation", dir: "../../../cloudformation/stacks/CUDly"},
	{name: "gcp-cloud-run", dir: "gcp/cloud-run"},
	{name: "gcp-gke", dir: "gcp/gke"},
	{name: "azure-container-apps", dir: "azure/container-apps"},
	{name: "azure-aks", dir: "azure/aks"},
}

// Copied from aws/cross_account_sts_discovery_test.go (commentLinePattern):
// whole-line `#` and `//` comments only. A /* */ pass would read the `/*` in
// an ARN glob or the `*/` in `0 */6 * * *` as a delimiter and swallow real
// declarations; the comment on the original explains the history.
var commentLinePattern = regexp.MustCompile(`(?m)^[ \t]*(?:#|//).*$`)

func stripComments(s string) string { return commentLinePattern.ReplaceAllString(s, "") }

// sweepKeyRe reads the keys of the sweep_tasks map: entries must stay on one
// line each (`key = { rule = ... }`), the shape the plan fixes.
var (
	sweepKeyRe = regexp.MustCompile(`(?m)^\s*(\w+)\s*=\s*\{`)
	// literalRes are the task-name patterns of the contract test (schedulerRefs)
	// plus the CloudFormation Input JSON.
	literalRes = func() []*regexp.Regexp {
		res := make([]*regexp.Regexp, 1, 1+len(schedulerRefs))
		res[0] = regexp.MustCompile(`"action":\s*"([^"]+)"`)
		for _, ref := range schedulerRefs {
			res = append(res, ref.re)
		}
		return res
	}()
)

// block returns the brace-balanced body after the first match of header, a
// pattern that ends at the opening brace.
func block(src, header string) (string, bool) {
	loc := regexp.MustCompile(header).FindStringIndex(src)
	if loc == nil {
		return "", false
	}
	for i, depth := loc[1]-1, 0; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return src[loc[1]:i], true
			}
		}
	}
	return "", false
}

// scanDir reads the non-test *.tf files (or template.yaml) directly in dir.
// Non-recursive on purpose: that skips tests/ and .terraform/.
func scanDir(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || (filepath.Ext(e.Name()) != ".tf" && e.Name() != "template.yaml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		files[e.Name()] = string(b)
	}
	return files, nil
}

// wiredFrom returns the tasks the files schedule and the names the server
// does not recognize (task literals and sweep_tasks keys).
func wiredFrom(files map[string]string) (wired map[string]bool, unknown []string) {
	wired = map[string]bool{}
	add := func(name string) {
		if _, err := server.ParseScheduledTaskType(name); err != nil {
			unknown = append(unknown, name)
			return
		}
		wired[name] = true
	}
	for _, raw := range files {
		src := stripComments(raw)
		for _, re := range literalRes {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				add(m[1])
			}
		}
		if body, ok := block(src, `sweep_tasks\s*=\s*\{`); ok {
			for _, m := range sweepKeyRe.FindAllStringSubmatch(body, -1) {
				add(m[1])
			}
		}
	}
	return wired, unknown
}

func requiredTasks() []string {
	var req []string
	for _, n := range server.ScheduledTaskNames() {
		if _, exempt := exemptTasks[n]; !exempt {
			req = append(req, n)
		}
	}
	return req
}

// checkGaps compares the computed gap with the documented one.
func checkGaps(actual, documented []string) []string {
	var problems []string
	for _, a := range actual {
		if !slices.Contains(documented, a) {
			problems = append(problems, "undocumented gap: "+a)
		}
	}
	for _, d := range documented {
		switch {
		case !slices.Contains(server.ScheduledTaskNames(), d):
			problems = append(problems, "knownGaps names a task outside the roster: "+d)
		case !slices.Contains(actual, d):
			problems = append(problems, "knownGaps lists "+d+" but it is wired")
		}
	}
	return problems
}

func TestEveryServerTaskIsClassified(t *testing.T) {
	roster := server.ScheduledTaskNames()
	for name, reason := range exemptTasks {
		if !slices.Contains(roster, name) || reason == "" || optInRequired[name] {
			t.Errorf("exempt task %q must be in the roster, have a reason and not be opt-in required", name)
		}
	}
	for name := range optInRequired {
		if !slices.Contains(requiredTasks(), name) {
			t.Errorf("opt-in task %q is not a required roster task", name)
		}
	}
	for _, prob := range knownGapsProblems(knownGaps) {
		t.Error(prob)
	}
}

// knownGapsProblems rejects platform and dimension keys that do not exist.
func knownGapsProblems(gaps map[string]map[string][]string) []string {
	var problems []string
	for plat, dims := range gaps {
		if !slices.ContainsFunc(platforms, func(p platform) bool { return p.name == plat }) {
			problems = append(problems, "knownGaps names unknown platform "+plat)
		}
		for dim := range dims {
			if !slices.Contains(validDims, dim) {
				problems = append(problems, "knownGaps["+plat+"] has unknown dimension "+dim)
			}
		}
	}
	return problems
}

func TestRequiredTasksWiredPerPlatform(t *testing.T) {
	for _, p := range platforms {
		files, err := scanDir(p.dir)
		if err != nil {
			t.Errorf("%s: %v", p.name, err)
			continue
		}
		wired, unknown := wiredFrom(files)
		for _, u := range unknown {
			t.Errorf("%s: schedules task %q which the server does not recognize", p.name, u)
		}
		var missing []string
		for _, task := range requiredTasks() {
			if !wired[task] {
				missing = append(missing, task)
			}
		}
		for _, prob := range checkGaps(missing, knownGaps[p.name][dimTask]) {
			t.Errorf("%s: %s (missing now: %v)", p.name, prob, missing)
		}
	}
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCheckerNegatives(t *testing.T) {
	const finalize = `input = jsonencode({ action = "finalize_revocations" })`
	for name, c := range map[string]struct {
		src     string
		wired   []string
		unknown []string
	}{
		"https uri survives, whole-line comments do not": {"# \"--task\", \"cleanup\"\n// /api/scheduled/cleanup\n  uri = \"https://h/api/scheduled/finalize_revocations\"\n", []string{"finalize_revocations"}, nil},
		"ARN glob and cron do not truncate":              {"a = \"arn:aws:events:*:*:rule/*\"\n" + finalize + "\nb = \"cron(0 */6 * * ? *)\"\n", []string{"finalize_revocations"}, nil},
		"sweep key unknown to the server":                {"sweep_tasks = {\n  bogus_task = { rule = \"x\" }\n  cleanup = { rule = \"c\" }\n}\n", []string{"cleanup"}, []string{"bogus_task"}},
		"CloudFormation input JSON":                      {"Input: '{\"action\": \"finalize_revocations\"}'\n", []string{"finalize_revocations"}, nil},
	} {
		wired, unknown := wiredFrom(map[string]string{"a.tf": c.src})
		if !slices.Equal(slices.Sorted(maps.Keys(wired)), c.wired) || !slices.Equal(unknown, c.unknown) {
			t.Errorf("%s: wired = %v, unknown = %v", name, wired, unknown)
		}
	}
	files, err := scanDir(writeFiles(t, map[string]string{
		"tests/x.tftest.hcl": finalize, "x.tftest.hcl": finalize, ".terraform/m.tf": finalize, "notes.md": finalize, "dir.tf/y.tf": finalize,
	}))
	if wired, _ := wiredFrom(files); err != nil || len(wired) != 0 {
		t.Errorf("tests/, .terraform/, *.tftest.hcl and non-tf files must not count: wired = %v, err = %v", wired, err)
	}
	if len(knownGapsProblems(map[string]map[string][]string{"nope": {dimTask: nil}})) != 1 ||
		len(knownGapsProblems(map[string]map[string][]string{"aws-lambda": {"nope": nil}})) != 1 {
		t.Error("knownGapsProblems must reject unknown platforms and dimensions")
	}
	if len(checkGaps([]string{"cleanup"}, []string{"cleanup"})) != 0 || len(checkGaps(nil, []string{"cleanup"})) == 0 ||
		len(checkGaps(nil, []string{"no_such_task"})) == 0 || len(checkGaps([]string{"cleanup"}, nil)) == 0 {
		t.Error("checkGaps must flag undocumented, stale (wired) and non-roster entries only")
	}
}
