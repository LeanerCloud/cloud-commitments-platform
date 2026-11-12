package tests

import (
	"context"
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
		for _, scenario := range []string{"healthy", "degraded", "unavailable", "recovery"} {
			t.Run(step+"/"+scenario, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempt := requests.Add(1)
					if r.URL.Path != "/ready" {
						t.Errorf("workflow requested %s instead of readiness", r.URL.Path)
						http.NotFound(w, r)
						return
					}
					if scenario == "unavailable" || scenario == "recovery" && attempt == 1 {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					status := "healthy"
					if scenario == "degraded" {
						status = "degraded"
					}
					_, _ = fmt.Fprintf(w, `{"status":%q}`, status)
				}))
				defer server.Close()
				body := azureWorkflowStep(t, step)
				body = strings.ReplaceAll(body, "${{ steps.get-url.outputs.url }}", server.URL)
				script := filepath.Join(t.TempDir(), "step.sh")
				if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\nset -euo pipefail\nsleep() { :; }\n"+body), 0o600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				output, err := exec.CommandContext(ctx, "bash", script).CombinedOutput()
				wantSuccess := scenario == "healthy" || scenario == "recovery" && step == "Test health endpoint"
				if (err == nil) != wantSuccess || ctx.Err() != nil {
					t.Fatalf("success=%t want=%t, err=%v: %s", err == nil, wantSuccess, err, output)
				}
				wantRequests := int32(1)
				if step == "Test health endpoint" {
					switch scenario {
					case "degraded", "unavailable":
						wantRequests = 5
					case "recovery":
						wantRequests = 2
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
