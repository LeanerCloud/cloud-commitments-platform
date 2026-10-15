// Coverage test for issue #712: every task in the server roster must be wired
// to a scheduler on every platform, or be listed in knownGaps with the reason
// it is not yet. The contract test next to it checks the other direction
// (every scheduler reference names a real task).
package compute_test

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/server"
)

const cleanupDefaultOn = false

// exemptTasks are roster tasks no platform has to schedule.
var exemptTasks = map[string]string{
	"analytics_refresh":   "analytics_collect already refreshes the views",
	"ri_exchange_reshape": "opt-in feature, off by default",
	"ladder_run":          "opt-in feature, off by default",
}

// optInRequired tasks must be wired but default to off (no live purchasing or
// retention change by default).
var optInRequired = map[string]bool{
	"fire_scheduled_purchases":    true,
	"process_scheduled_purchases": true,
	"send_notifications":          true,
	"cleanup":                     true,
}

type taskVar struct{ enable, schedule string }

var taskVars = map[string]taskVar{
	"collect_recommendations":     {"enable_scheduled_tasks", "recommendation_schedule"},
	"reap_stuck_purchases":        {"enable_reap_stuck_purchases_schedule", "reap_stuck_purchases_schedule"},
	"analytics_collect":           {"enable_analytics_collect_schedule", "analytics_collect_schedule"},
	"fire_scheduled_purchases":    {"enable_fire_scheduled_purchases_schedule", "fire_scheduled_purchases_schedule"},
	"ladder_run":                  {"enable_ladder_run_schedule", "ladder_run_schedule"},
	"ri_exchange_reshape":         {"enable_ri_exchange_schedule", "ri_exchange_schedule"},
	"process_scheduled_purchases": {"enable_process_scheduled_purchases_schedule", "process_scheduled_purchases_schedule"},
	"send_notifications":          {"enable_send_notifications_schedule", "send_notifications_schedule"},
	"finalize_revocations":        {"enable_finalize_revocations_schedule", "finalize_revocations_schedule"},
	"cleanup":                     {"enable_cleanup_schedule", "cleanup_schedule"},
}

const (
	dimTask    = "missingTask"
	dimPass    = "missingEnvPassThrough"
	dimDefault = "missingEnvDefault"
)

var (
	allEight = []string{"analytics_collect", "cleanup", "collect_recommendations", "finalize_revocations",
		"fire_scheduled_purchases", "process_scheduled_purchases", "reap_stuck_purchases", "send_notifications"}
	sevenGCP = []string{"analytics_collect", "cleanup", "finalize_revocations", "fire_scheduled_purchases",
		"process_scheduled_purchases", "reap_stuck_purchases", "send_notifications"}
	reapAnalytics = []string{"analytics_collect", "reap_stuck_purchases"}
)

// knownGaps documents what is not wired yet, per platform and dimension. Each
// PR that wires a task removes it here; the test fails when actual != documented.
var knownGaps = map[string]map[string][]string{
	"aws-lambda": {
		dimTask:    {"cleanup", "finalize_revocations", "process_scheduled_purchases", "send_notifications"},
		dimPass:    reapAnalytics,
		dimDefault: reapAnalytics,
	},
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

type platform struct{ name, dir, envBlock string }

var platforms = []platform{
	{"aws-lambda", "aws/lambda", "compute_lambda"},
	{"aws-fargate", "aws/fargate", "compute_fargate"},
	{"cloudformation", "../../../cloudformation/stacks/CUDly", ""},
	{"gcp-cloud-run", "gcp/cloud-run", ""},
	{"gcp-gke", "gcp/gke", ""},
	{"azure-container-apps", "azure/container-apps", ""},
	{"azure-aks", "azure/aks", ""},
}

const awsEnvDir = "../../environments/aws"

// Copied from aws/cross_account_sts_discovery_test.go (commentLinePattern):
// whole-line `#` and `//` comments only. A /* */ pass would read the `/*` in
// an ARN glob or the `*/` in `0 */6 * * *` as a delimiter and swallow real
// declarations; the comment on the original explains the history.
var commentLinePattern = regexp.MustCompile(`(?m)^[ \t]*(?:#|//).*$`)

func stripComments(s string) string { return commentLinePattern.ReplaceAllString(s, "") }

var (
	passLineRe  = regexp.MustCompile(`(?m)^\s*(\w+)\s*=\s*var\.(\w+)\s*$`)
	sweepKeyRe  = regexp.MustCompile(`(?m)^\s*(\w+)\s*=\s*\{`)
	tfvarsOffRe = regexp.MustCompile(`(?m)^\s*(enable_\w+)\s*=\s*false\b`)
	defaultRe   = regexp.MustCompile(`(?m)^\s*default\s*=\s*(\S+)`)
	// literalRes are the task-name patterns of the contract test (schedulerRefs,
	// deduplicated) plus the CloudFormation Input JSON.
	literalRes = func() []*regexp.Regexp {
		res := []*regexp.Regexp{regexp.MustCompile(`"action":\s*"([^"]+)"`)}
		for _, ref := range schedulerRefs {
			if !slices.ContainsFunc(res, func(r *regexp.Regexp) bool { return r.String() == ref.re.String() }) {
				res = append(res, ref.re)
			}
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

func defaultOn(task string) bool {
	if task == "cleanup" {
		return cleanupDefaultOn
	}
	_, exempt := exemptTasks[task]
	return !exempt && !optInRequired[task]
}

// envDefaultProblem checks an enable var's default against the opt-in policy.
func envDefaultProblem(task, def string) string {
	if want := defaultOn(task); (def == "true") != want {
		return fmt.Sprintf("%s: enable var defaults to %s, policy wants default-on=%v", task, def, want)
	}
	return ""
}

// tfvarsOverrides returns the default-on tasks a tfvars file switches off.
func tfvarsOverrides(src string) []string {
	var bad []string
	for _, m := range tfvarsOffRe.FindAllStringSubmatch(stripComments(src), -1) {
		for task, v := range taskVars {
			if v.enable == m[1] && defaultOn(task) {
				bad = append(bad, task)
			}
		}
	}
	return bad
}

// sweepProblems cross-checks dir/sweeps.tf: each sweep target must point at
// the rule with the same key, and (Lambda) so must its invoke permission. An
// absent file is fine; an existing one must have the blocks.
func sweepProblems(dir string, needPermission bool) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "sweeps.tf"))
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	src := stripComments(string(raw))
	var problems []string
	check := func(typ, attr, ref string) {
		body, ok := block(src, `resource\s+"`+typ+`"\s+"sweep"\s*\{`)
		want := regexp.MustCompile(`(?m)^\s*` + attr + `\s*=\s*aws_cloudwatch_event_rule\.sweep\[each\.key\]\.` + ref + `\b`)
		if !ok || !want.MatchString(body) {
			problems = append(problems, typ+".sweep is missing or lacks "+attr+" = aws_cloudwatch_event_rule.sweep[each.key]."+ref)
		}
	}
	check("aws_cloudwatch_event_target", "rule", "name")
	if needPermission {
		check("aws_lambda_permission", "source_arn", "arn")
	}
	return problems, nil
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
	for _, name := range requiredTasks() {
		if _, ok := taskVars[name]; !ok {
			t.Errorf("required task %q has no taskVars entry; classify it as exempt or add one", name)
		}
	}
}

func TestRequiredTasksWiredPerPlatform(t *testing.T) {
	for _, p := range platforms {
		files, err := scanDir(p.dir)
		if err != nil {
			t.Fatalf("%s: %v", p.name, err)
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
		if p.envBlock == "" {
			continue
		}
		sweeps, err := sweepProblems(p.dir, strings.HasSuffix(p.dir, "lambda"))
		if err != nil {
			t.Fatal(err)
		}
		for _, prob := range sweeps {
			t.Errorf("%s: sweeps.tf: %s", p.name, prob)
		}
	}
}

func TestAWSEnvWiring(t *testing.T) {
	envVars, err := os.ReadFile(awsEnvDir + "/variables.tf")
	if err != nil {
		t.Fatal(err)
	}
	compute, err := os.ReadFile(awsEnvDir + "/compute.tf")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range platforms[:2] {
		files, err := scanDir(p.dir)
		if err != nil {
			t.Fatal(err)
		}
		wired, _ := wiredFrom(files)
		pass, ok := block(stripComments(string(compute)), `module\s+"`+p.envBlock+`"\s*\{`)
		if !ok {
			t.Fatalf("%s: module block not found in compute.tf", p.name)
		}
		passed := map[string]string{}
		for _, m := range passLineRe.FindAllStringSubmatch(pass, -1) {
			passed[m[1]] = m[2]
		}
		gaps := map[string][]string{}
		for _, task := range server.ScheduledTaskNames() {
			if !wired[task] {
				continue
			}
			tv := taskVars[task]
			for _, v := range []string{tv.enable, tv.schedule} {
				if !strings.Contains(files["variables.tf"], `variable "`+v+`"`) {
					t.Errorf("%s: module variables.tf does not declare %s", p.name, v)
				}
				if rhs, ok := passed[v]; !ok {
					gaps[dimPass] = append(gaps[dimPass], task)
				} else if rhs != v {
					t.Errorf("%s: compute.tf passes %s = var.%s; must be name-identical", p.name, v, rhs)
				}
			}
			body, declared := block(string(envVars), `variable\s+"`+tv.enable+`"\s*\{`)
			_, schedDeclared := block(string(envVars), `variable\s+"`+tv.schedule+`"\s*\{`)
			if m := defaultRe.FindStringSubmatch(body); !declared || !schedDeclared {
				gaps[dimDefault] = append(gaps[dimDefault], task)
			} else if m == nil {
				t.Errorf("%s: env variable %s has no default", p.name, tv.enable)
			} else if prob := envDefaultProblem(task, m[1]); prob != "" {
				t.Errorf("%s: %s", p.name, prob)
			}
		}
		for _, dim := range []string{dimPass, dimDefault} {
			slices.Sort(gaps[dim])
			for _, prob := range checkGaps(slices.Compact(gaps[dim]), knownGaps[p.name][dim]) {
				t.Errorf("%s %s: %s", p.name, dim, prob)
			}
		}
	}
}

func TestNoTfvarsDisablesDefaultOnTask(t *testing.T) {
	paths, err := filepath.Glob(awsEnvDir + "/*.tfvars*")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no tfvars files found: %v", err)
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range tfvarsOverrides(string(b)) {
			t.Errorf("%s disables default-on task %s", path, task)
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
		src         string
		wired       []string
		unknownKeys []string
	}{
		"https uri survives, whole-line comments do not": {"# \"--task\", \"cleanup\"\n// /api/scheduled/cleanup\n  uri = \"https://h/api/scheduled/finalize_revocations\"\n", []string{"finalize_revocations"}, nil},
		"ARN glob and cron do not truncate":              {"a = \"arn:aws:events:*:*:rule/*\"\n" + finalize + "\nb = \"cron(0 */6 * * ? *)\"\n", []string{"finalize_revocations"}, nil},
		"sweep key unknown to the server":                {"sweep_tasks = {\n  bogus_task = { rule = \"x\" }\n  cleanup = { rule = \"c\" }\n}\n", []string{"cleanup"}, []string{"bogus_task"}},
	} {
		wired, unknown := wiredFrom(map[string]string{"a.tf": c.src})
		if !slices.Equal(slices.Sorted(maps.Keys(wired)), c.wired) || !slices.Equal(unknown, c.unknownKeys) {
			t.Errorf("%s: wired = %v, unknown = %v", name, wired, unknown)
		}
	}
	files, err := scanDir(writeFiles(t, map[string]string{"tests/x.tftest.hcl": finalize, ".terraform/m.tf": finalize, "notes.md": finalize}))
	if wired, _ := wiredFrom(files); err != nil || len(wired) != 0 {
		t.Errorf("tests/, .terraform/ and non-tf files must not count: wired = %v, err = %v", wired, err)
	}
	if len(checkGaps([]string{"cleanup"}, []string{"cleanup"})) != 0 || len(checkGaps(nil, []string{"cleanup"})) == 0 ||
		len(checkGaps(nil, []string{"no_such_task"})) == 0 || len(checkGaps([]string{"cleanup"}, nil)) == 0 {
		t.Error("checkGaps must flag undocumented, stale (wired) and non-roster entries only")
	}
	if envDefaultProblem("finalize_revocations", "false") == "" || envDefaultProblem("fire_scheduled_purchases", "true") == "" ||
		envDefaultProblem("finalize_revocations", "true") != "" || envDefaultProblem("cleanup", "false") != "" {
		t.Error("default-on / opt-in policy not enforced")
	}
	if got := tfvarsOverrides("enable_finalize_revocations_schedule = false\nenable_fire_scheduled_purchases_schedule = false\n# enable_scheduled_tasks = false\n"); !slices.Equal(got, []string{"finalize_revocations"}) {
		t.Errorf("tfvarsOverrides = %v", got)
	}
}

func TestSweepCrossWiring(t *testing.T) {
	const target = "resource \"aws_cloudwatch_event_target\" \"sweep\" {\n  rule%s= aws_cloudwatch_event_rule.sweep[%s].name\n}\n"
	const perm = "resource \"aws_lambda_permission\" \"sweep\" {\n  source_arn%s= aws_cloudwatch_event_rule.sweep[%s].arn\n}\n"
	good, goodPerm := fmt.Sprintf(target, " ", "each.key"), fmt.Sprintf(perm, " ", "each.key")
	for name, c := range map[string]struct {
		src      string
		lambda   bool
		wantProb bool
	}{
		"aligned":               {fmt.Sprintf(target, "      ", "each.key") + fmt.Sprintf(perm, "   ", "each.key"), true, false},
		"unaligned":             {good + goodPerm, true, false},
		"fargate needs no perm": {good, false, false},
		"wrong key":             {fmt.Sprintf(target, " ", "each.value.rule") + goodPerm, true, true},
		"wrong resource type":   {strings.Replace(good, "event_rule", "event_target", 1) + goodPerm, true, true},
		"missing permission":    {good, true, true},
		"missing target":        {goodPerm, true, true},
	} {
		got, err := sweepProblems(writeFiles(t, map[string]string{"sweeps.tf": c.src}), c.lambda)
		if err != nil || (len(got) > 0) != c.wantProb {
			t.Errorf("%s: problems = %v, err = %v", name, got, err)
		}
	}
	if got, err := sweepProblems(t.TempDir(), true); err != nil || got != nil {
		t.Errorf("absent sweeps.tf must pass: problems = %v, err = %v", got, err)
	}
}
