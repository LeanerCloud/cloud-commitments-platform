package modules

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// modulesDir lets the checks run against another tree (fail-before proof).
func modulesDir() string {
	if d := os.Getenv("TF_MODULES_DIR"); d != "" {
		return d
	}
	return "."
}

func readModule(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(modulesDir(), rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Each rule pins one fix from the 2026-07-28 modules review (#131).
func TestModuleHardening131(t *testing.T) {
	rules := []struct {
		name, file, want string
		forbid           bool
	}{
		{"ecr lambda pull needs a source ARN", "registry/aws/main.tf", `(?s)AllowPullFromLambda".*?ArnLike\s*=\s*\{\s*"aws:sourceARN"`, false},
		{"aws additional_secrets is sensitive", "secrets/aws/variables.tf", `(?s)variable "additional_secrets".*?sensitive\s*=\s*true`, false},
		{"aws additional_secrets keys are unwrapped", "secrets/aws/main.tf", `for_each = toset\(nonsensitive\(keys\(var\.additional_secrets\)\)\)`, false},
		{"empty database_password is regenerated", "secrets/aws/main.tf", `count\s*=\s*var\.database_password == null \|\| var\.database_password == "" \?`, false},
		{"db output follows create_password", "database/aws/outputs.tf", `var\.create_password \? aws_secretsmanager_secret\.db_password`, false},
		{"rds exports logs", "database/aws/main.tf", `enabled_cloudwatch_logs_exports\s*=\s*\["postgresql",\s*"upgrade"\]`, false},
		{"flow log policy is not wildcard-only", "networking/aws/main.tf", `aws_cloudwatch_log_group\.flow_logs\[0\]\.arn,\s*"\$\{aws_cloudwatch_log_group\.flow_logs\[0\]\.arn\}:\*"`, false},
		{"nsg uses the Internet tag", "networking/azure/main.tf", `\? "\*" : "VirtualNetwork"`, true},
		{"schedule hour is zero padded", "compute/azure/container-apps/scheduled-tasks.tf", `%02s`, true},
		{"container app console logs shipped", "compute/azure/container-apps/main.tf", `ContainerAppConsoleLogs`, false},
		{"lambda email domain validated", "compute/aws/lambda/variables.tf", `(?s)variable "email_from_domain".*?condition\s*=\s*var\.email_from_domain == "" \|\| can\(regex\("\^\[A-Za-z0-9\]\(\[A-Za-z0-9-\]\*\[A-Za-z0-9\]\)\?\(\\\\\.`, false},
		{"fargate email domain validated", "compute/aws/fargate/variables.tf", `(?s)variable "email_from_domain".*?condition\s*=\s*var\.email_from_domain == "" \|\| can\(regex\("\^\[A-Za-z0-9\]\(\[A-Za-z0-9-\]\*\[A-Za-z0-9\]\)\?\(\\\\\.`, false},
		{"email/azure does not interpolate the resource group", "email/azure/main.tf", `\$\{var\.resource_group_name\}`, true},
	}
	for _, r := range rules {
		got := regexp.MustCompile(r.want).MatchString(readModule(t, r.file))
		if got == r.forbid {
			t.Errorf("%s: %s (pattern %q, forbid=%v)", r.name, r.file, r.want, r.forbid)
		}
	}
	if n := len(regexp.MustCompile(`ignore_changes\s*=\s*\[start_time\]`).FindAllString(readModule(t, "compute/azure/container-apps/scheduled-tasks.tf"), -1)); n != 3 {
		t.Errorf("want 3 recurrence triggers with ignore_changes=[start_time], got %d", n)
	}
}
