package server

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/testutil"
)

func TestLoadAnalyticsConfigEnabled(t *testing.T) {
	t.Setenv("ANALYTICS_RETENTION_MONTHS", "")
	t.Setenv("ANALYTICS_PARTITIONS_AHEAD", "")
	for _, tc := range []struct {
		name, value string
		wantEnabled bool
		wantError   bool
	}{
		{"unset", "", true, false},
		{"empty", "", true, false},
		{"true", "true", true, false},
		{"TRUE", "TRUE", true, false},
		{"1", "1", true, false},
		{"false", "false", false, false},
		{"FALSE", "FALSE", false, false},
		{"0", "0", false, false},
		{"no", "no", false, true},
		{"off", "off", false, true},
		{"trailing space", "False ", false, true},
		{"disabled", "disabled", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ANALYTICS_COLLECTION_ENABLED", tc.value)
			if tc.name == "unset" {
				if err := os.Unsetenv("ANALYTICS_COLLECTION_ENABLED"); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := LoadAnalyticsConfig()
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "ANALYTICS_COLLECTION_ENABLED") {
					t.Fatalf("expected enabled parse error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			testutil.AssertEqual(t, tc.wantEnabled, cfg.Enabled)
			testutil.AssertNoError(t, cfg.Validate())
		})
	}
}

// TestLoadAnalyticsConfig_FailFastOnMalformedInt is the CR #1049 regression:
// a set-but-unparseable ANALYTICS_RETENTION_MONTHS / ANALYTICS_PARTITIONS_AHEAD
// must produce a config that Validate() rejects (fail-fast at startup) instead
// of silently falling back to the default. The pre-fix getEnvInt path returned
// the default for a bad value, so Validate() would have passed.
func TestLoadAnalyticsConfig_FailFastOnMalformedInt(t *testing.T) {
	t.Run("unset uses defaults and validates", func(t *testing.T) {
		t.Setenv("ANALYTICS_RETENTION_MONTHS", "")
		t.Setenv("ANALYTICS_PARTITIONS_AHEAD", "")
		cfg, err := LoadAnalyticsConfig()
		testutil.AssertNoError(t, err)
		testutil.AssertEqual(t, defaultAnalyticsRetentionMonths, cfg.RetentionMonths)
		testutil.AssertEqual(t, defaultAnalyticsPartitionsAhead, cfg.PartitionsAhead)
		testutil.AssertNoError(t, cfg.Validate())
	})

	t.Run("valid override is parsed", func(t *testing.T) {
		t.Setenv("ANALYTICS_RETENTION_MONTHS", "12")
		t.Setenv("ANALYTICS_PARTITIONS_AHEAD", "6")
		cfg, err := LoadAnalyticsConfig()
		testutil.AssertNoError(t, err)
		testutil.AssertEqual(t, 12, cfg.RetentionMonths)
		testutil.AssertEqual(t, 6, cfg.PartitionsAhead)
		testutil.AssertNoError(t, cfg.Validate())
	})

	t.Run("malformed retention is rejected by Validate", func(t *testing.T) {
		t.Setenv("ANALYTICS_RETENTION_MONTHS", "not-a-number")
		t.Setenv("ANALYTICS_PARTITIONS_AHEAD", "3")
		cfg, err := LoadAnalyticsConfig()
		testutil.AssertNoError(t, err)
		testutil.AssertEqual(t, 0, cfg.RetentionMonths) // sentinel
		testutil.AssertTrue(t, cfg.Validate() != nil, "malformed retention must fail Validate")
	})

	t.Run("malformed partitions-ahead is rejected by Validate", func(t *testing.T) {
		t.Setenv("ANALYTICS_RETENTION_MONTHS", "24")
		t.Setenv("ANALYTICS_PARTITIONS_AHEAD", "12x")
		cfg, err := LoadAnalyticsConfig()
		testutil.AssertNoError(t, err)
		testutil.AssertEqual(t, 0, cfg.PartitionsAhead) // sentinel
		testutil.AssertTrue(t, cfg.Validate() != nil, "malformed partitions-ahead must fail Validate")
	})
}

// TestHandleCollectAnalytics_DDLStepsAreBounded is the 06-N3 regression: each
// long-running partition/retention/refresh DDL step must run under a bounded
// context so a runaway statement cannot hang the scheduled run when no
// statement_timeout is enforced (RDS Proxy). The parent context here carries no
// deadline, so a deadline observed inside each step proves the per-step bound.
func TestHandleCollectAnalytics_DDLStepsAreBounded(t *testing.T) {
	// Deliberately a deadline-free parent so an observed per-step deadline can
	// only have come from the pipeline's own bounding (not inherited).
	ctx := context.Background()
	store := &mockAnalyticsStore{}
	app := &Application{
		appConfig:          ApplicationConfig{Analytics: AnalyticsConfig{Enabled: true, RetentionMonths: 24, PartitionsAhead: 3}},
		Analytics:          store,
		AnalyticsCollector: &mockAnalyticsCollector{},
	}

	_, err := app.handleCollectAnalytics(ctx)
	testutil.AssertNoError(t, err)
	testutil.AssertTrue(t, store.createFuturePartHadDeadline, "CreateFuturePartitions must run under a bounded context")
	testutil.AssertTrue(t, store.dropOldPartHadDeadline, "DropOldPartitions must run under a bounded context")
	testutil.AssertTrue(t, store.refreshHadDeadline, "RefreshMaterializedViews must run under a bounded context")
}
