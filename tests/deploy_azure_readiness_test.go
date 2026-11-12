package tests

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAzureReadinessWorkflow(t *testing.T) {
	for _, step := range []string{"Test health endpoint", "Run smoke tests"} {
		for _, scenario := range []string{"healthy", "degraded", "unavailable", "malformed", "recovery", "late recovery"} {
			t.Run(step+"/"+scenario, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempt := requests.Add(1)
					if r.URL.Path != "/ready" {
						t.Errorf("workflow requested %s instead of readiness", r.URL.Path)
						http.NotFound(w, r)
						return
					}
					if scenario == "unavailable" || scenario == "recovery" && attempt == 1 || scenario == "late recovery" && attempt <= 30 {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					if scenario == "malformed" {
						_, _ = fmt.Fprint(w, "not JSON")
						return
					}
					status := "healthy"
					if scenario == "degraded" {
						status = "degraded"
					}
					_, _ = fmt.Fprintf(w, `{"status":%q}`, status)
				}))
				defer server.Close()
				body := azureWorkflowStep(t, step)
				clock := "sleep() { :; }\n"
				if step == "Test health endpoint" {
					if strings.Count(body, "SECONDS + 300") != 1 {
						t.Fatal("readiness gate must use exactly one 300-second deadline")
					}
					clock = "unset SECONDS\nSECONDS=0\nsleep() { SECONDS=$((SECONDS + $1)); }\n"
				}
				body = strings.ReplaceAll(body, "${{ steps.get-url.outputs.url }}", server.URL)
				script := filepath.Join(t.TempDir(), "step.sh")
				if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\nset -euo pipefail\n"+clock+body), 0o600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				output, err := exec.CommandContext(ctx, "bash", script).CombinedOutput()
				wantSuccess := scenario == "healthy" || (scenario == "recovery" || scenario == "late recovery") && step == "Test health endpoint"
				if (err == nil) != wantSuccess || ctx.Err() != nil {
					t.Fatalf("success=%t want=%t, err=%v: %s", err == nil, wantSuccess, err, output)
				}
				wantRequests := int32(1)
				if step == "Test health endpoint" {
					switch scenario {
					case "degraded", "unavailable", "malformed":
						wantRequests = 60
					case "recovery":
						wantRequests = 2
					case "late recovery":
						wantRequests = 31
					}
				} else if scenario == "healthy" {
					wantRequests = 3
				}
				if requests.Load() != wantRequests {
					t.Fatalf("requests=%d want=%d", requests.Load(), wantRequests)
				}
			})
		}
	}
}

func TestAzureReadinessDeadline(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		t.Run(fmt.Sprintf("stalled=%t", stalled), func(t *testing.T) {
			release := make(chan struct{})
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/ready" {
					t.Errorf("workflow requested %s instead of readiness", r.URL.Path)
				}
				if stalled {
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			defer close(release)
			body := azureWorkflowStep(t, "Test health endpoint")
			if strings.Count(body, "SECONDS + 300") != 1 {
				t.Fatal("readiness gate must use exactly one 300-second deadline")
			}
			body = strings.Replace(body, "SECONDS + 300", "SECONDS + 2", 1)
			body = strings.ReplaceAll(body, "${{ steps.get-url.outputs.url }}", server.URL)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", body).CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || ctx.Err() != nil {
				t.Fatalf("deadline did not exit 1 before outer timeout: %v, context=%v: %s", err, ctx.Err(), output)
			}
			if requests.Load() == 0 {
				t.Fatal("deadline made no readiness request")
			}
		})
	}
}

func azureWorkflowStep(t *testing.T, name string) string {
	t.Helper()
	workflow, err := os.ReadFile("../.github/workflows/deploy-azure.yml")
	if err != nil {
		t.Fatal(err)
	}
	_, step, ok := strings.Cut(string(workflow), "      - name: "+name+"\n        run: |\n")
	if !ok {
		t.Fatalf("workflow step %q not found", name)
	}
	step, _, _ = strings.Cut(step, "\n      - name:")
	var body strings.Builder
	for _, line := range strings.Split(step, "\n") {
		body.WriteString(strings.TrimPrefix(line, "          "))
		body.WriteByte('\n')
	}
	return body.String()
}
