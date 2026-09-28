// Regression test for issue #123: the Cloud Run module derived
// SCHEDULED_TASK_AUTH_MODE from whether a Cloud Scheduler job was enabled,
// falling back to "disabled" whenever both var.enable_scheduled_tasks and
// var.enable_ri_exchange_schedule were false. That conflates "does
// Terraform create a scheduler" with "does the app require auth on
// /api/scheduled/*", and the latter is reachable from the open internet
// independently of the scheduler flags whenever var.allow_unauthenticated
// is true. A single tfvars flip could silently turn off request
// authentication on money-path handlers on an already-public service.
package gcp_test

import (
	"os"
	"regexp"
	"testing"
)

func TestCloudRunScheduledTaskAuthModeFailsClosed(t *testing.T) {
	mainData, err := os.ReadFile("cloud-run/main.tf")
	if err != nil {
		t.Fatalf("reading cloud-run/main.tf: %v", err)
	}
	main := string(mainData)

	// The bug: falling back to the bare literal "disabled" whenever
	// neither scheduler is enabled, with no way to require auth anyway.
	if regexp.MustCompile(`scheduled_task_auth_mode\s*=\s*\([^)]*\)\s*\?\s*"oidc"\s*:\s*"disabled"`).MatchString(main) {
		t.Error(`cloud-run/main.tf derives scheduled_task_auth_mode = ... ? "oidc" : "disabled" directly from the scheduler flags (#123); disabling both schedulers must not silently disable request auth on an internet-reachable service`)
	}

	if !regexp.MustCompile(`scheduled_task_auth_mode\s*=\s*\(\s*\n?\s*var\.enable_scheduled_tasks\s*\|\|\s*var\.enable_ri_exchange_schedule\s*\n?\s*\?\s*"oidc"\s*\n?\s*:\s*var\.scheduled_task_auth_mode_override`).MatchString(main) {
		t.Error(`cloud-run/main.tf must fall back to var.scheduled_task_auth_mode_override (not a bare "disabled" literal) when neither scheduler is enabled`)
	}

	varsData, err := os.ReadFile("cloud-run/variables.tf")
	if err != nil {
		t.Fatalf("reading cloud-run/variables.tf: %v", err)
	}
	vars := string(varsData)

	if !regexp.MustCompile(`variable\s+"scheduled_task_auth_mode_override"\s*\{`).MatchString(vars) {
		t.Fatal(`cloud-run/variables.tf must declare scheduled_task_auth_mode_override`)
	}

	// Fail-closed default: the override must default to "oidc", the same
	// safe posture terraform/modules/compute/gcp/gke already uses for the
	// identical defect, not the "disabled" the bug produced.
	overrideBlock := regexp.MustCompile(`(?s)variable\s+"scheduled_task_auth_mode_override"\s*\{.*?\n\}`).FindString(vars)
	if overrideBlock == "" {
		t.Fatal(`could not isolate the scheduled_task_auth_mode_override variable block`)
	}
	if !regexp.MustCompile(`default\s*=\s*"oidc"`).MatchString(overrideBlock) {
		t.Error(`scheduled_task_auth_mode_override must default to "oidc" (fail closed); a deploy with no scheduler SA must still require the validator to be configured rather than defaulting to unauthenticated`)
	}
	if !regexp.MustCompile(`contains\(\["oidc",\s*"bearer",\s*"disabled"\],\s*var\.scheduled_task_auth_mode_override\)`).MatchString(overrideBlock) {
		t.Error(`scheduled_task_auth_mode_override must validate against the exact set the Go validator (internal/server/scheduledauth) accepts: "oidc", "bearer", "disabled"`)
	}
}
