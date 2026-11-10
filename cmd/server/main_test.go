package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/server"
)

func TestGetTaskTimeout(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected time.Duration
	}{
		{
			name:     "default when env not set",
			envValue: "",
			expected: 15 * time.Minute,
		},
		{
			name:     "valid env value",
			envValue: "60",
			expected: 60 * time.Second,
		},
		{
			name:     "invalid env value falls back to default",
			envValue: "not-a-number",
			expected: 15 * time.Minute,
		},
		{
			name:     "zero falls back to default",
			envValue: "0",
			expected: 15 * time.Minute,
		},
		{
			name:     "negative falls back to default",
			envValue: "-10",
			expected: 15 * time.Minute,
		},
		{
			name:     "large value",
			envValue: "3600",
			expected: 3600 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := os.Getenv("TASK_TIMEOUT")
			defer func() {
				if old == "" {
					os.Unsetenv("TASK_TIMEOUT")
				} else {
					os.Setenv("TASK_TIMEOUT", old)
				}
			}()

			if tt.envValue != "" {
				os.Setenv("TASK_TIMEOUT", tt.envValue)
			} else {
				os.Unsetenv("TASK_TIMEOUT")
			}

			result := getTaskTimeout()
			if result != tt.expected {
				t.Errorf("getTaskTimeout() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestParseFlags_MigrateAcceptsEntrypointTrailingFlags(t *testing.T) {
	c, err := parseFlags([]string{"--migrate", "--mode=http", "--port=8080"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !c.migrate || c.mode != "http" || c.port != 8080 {
		t.Errorf("unexpected flags: %+v", c)
	}
	if c.migrateOptions().ForceVersion != nil {
		t.Error("ForceVersion must be nil unless --migrate-force-version is given")
	}
}

func TestParseFlags_ForceVersion(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int
	}{{[]string{"--migrate", "--migrate-force-version", "0"}, 0}, {[]string{"--migrate", "--migrate-force-version=12"}, 12}} {
		c, err := parseFlags(tc.args)
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		got := c.migrateOptions().ForceVersion
		if got == nil || *got != tc.want {
			t.Errorf("ForceVersion = %v, want %d", got, tc.want)
		}
	}
}

func TestRunMigrateMode_ExitCodes(t *testing.T) {
	var gotOpts server.MigrateOptions
	ok := func(_ context.Context, o server.MigrateOptions) (server.MigrateReport, error) {
		gotOpts = o
		return server.MigrateReport{From: 1, To: 2}, nil
	}
	fail := func(context.Context, server.MigrateOptions) (server.MigrateReport, error) {
		return server.MigrateReport{}, errors.New("dirty")
	}
	if code := runMigrateMode(context.Background(), server.MigrateOptions{Timeout: time.Minute}, ok); code != 0 {
		t.Errorf("success exit = %d, want 0", code)
	}
	if gotOpts.Timeout != time.Minute {
		t.Errorf("options not passed through: %+v", gotOpts)
	}
	if code := runMigrateMode(context.Background(), server.MigrateOptions{}, fail); code != 1 {
		t.Errorf("failure exit = %d, want 1", code)
	}
}

func TestParseFlags_UsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--migrate-force-version", "3"},
		{"--migrate-force-version=0"},
		{"--migrate", "--migrate-force-version=-5"},
		{"--migrate", "--task", "cleanup"},
		{"--bogus"},
		{"--port", "x"},
	} {
		_, err := parseFlags(args)
		if err == nil {
			t.Fatalf("parseFlags(%v) succeeded, want error", args)
		}
		if got := flagErrorCode(err); got != 2 {
			t.Errorf("flagErrorCode for %v = %d, want 2", args, got)
		}
	}
}

func TestFlagErrorCode_HelpExitsZero(t *testing.T) {
	_, err := parseFlags([]string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp", err)
	}
	if got := flagErrorCode(err); got != 0 {
		t.Errorf("exit = %d, want 0", got)
	}
}

// TestDispatch_Order pins that --migrate wins over --task and the listener,
// and --task over the listener, each running exactly one handler.
func TestDispatch_Order(t *testing.T) {
	cases := []struct {
		name string
		c    cliFlags
		want []string
	}{
		{"migrate only", cliFlags{migrate: true}, []string{"migrate"}},
		{"migrate beats task", cliFlags{migrate: true, task: "x"}, []string{"migrate"}},
		{"task beats serve", cliFlags{task: "x"}, []string{"task"}},
		{"serve", cliFlags{}, []string{"serve"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			rec := func(name string, code int) func(cliFlags) int {
				return func(cliFlags) int { calls = append(calls, name); return code }
			}
			code := dispatch(tc.c, handlers{migrate: rec("migrate", 7), task: rec("task", 8), serve: rec("serve", 9)})
			if !slices.Equal(calls, tc.want) {
				t.Errorf("calls = %v, want %v", calls, tc.want)
			}
			wantCode := map[string]int{"migrate": 7, "task": 8, "serve": 9}[tc.want[0]]
			if code != wantCode {
				t.Errorf("code = %d, want %d", code, wantCode)
			}
		})
	}
}
