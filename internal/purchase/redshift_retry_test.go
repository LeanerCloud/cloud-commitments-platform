package purchase

import (
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
)

func retryRow(attempt int, noSubmission bool, recs ...config.RecommendationRecord) *config.PurchaseExecution {
	return &config.PurchaseExecution{
		ExecutionID: "rs-failed", Status: "failed", RetryAttemptN: attempt, NoSubmission: noSubmission, Recommendations: recs,
	}
}

func durPtr(d time.Duration) *time.Duration { return &d }

func TestRedshiftRetryPermanentRefusal(t *testing.T) {
	rs := redshiftScopedRec("acct-A")
	ec2 := scopedTestRec("acct-A")
	ec2.Service = "ec2"
	h := redshiftRedriveHorizon
	tests := []struct {
		name  string
		exec  *config.PurchaseExecution
		since *time.Duration
		want  string
	}{
		{"no stamp refused", retryRow(0, false, rs), nil, redshiftRetryNoStampRefusal},
		{"no stamp attempt 1 no_submission refused", retryRow(1, true, rs), nil, redshiftRetryNoStampRefusal},
		{"no stamp attempt 0 no_submission skipped", retryRow(0, true, rs), nil, ""},
		{"fresh stamp allowed", retryRow(0, false, rs), durPtr(0), ""},
		{"just under horizon allowed", retryRow(1, false, rs), durPtr(h - time.Nanosecond), ""},
		{"exactly horizon refused", retryRow(1, false, rs), durPtr(h), redshiftRetryHorizonRefusal},
		{"past horizon refused", retryRow(1, false, rs), durPtr(h + time.Second), redshiftRetryHorizonRefusal},
		// A never-submitted attempt 0 is exempt: its successor mints a fresh stamp.
		{"past horizon attempt 0 no_submission exempt", retryRow(0, true, rs), durPtr(h), ""},
		{"non-redshift never refused", retryRow(0, false, ec2), nil, ""},
		{"unselected redshift rec never refused", retryRow(0, false, func() config.RecommendationRecord { r := rs; r.Selected = false; return r }()), nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RedshiftRetryPermanentRefusal(tc.exec, tc.since))
		})
	}
}

func TestRedshiftRetryWait(t *testing.T) {
	rs := redshiftScopedRec("acct-A")
	ec2 := scopedTestRec("acct-A")
	ec2.Service = "ec2"
	s := redshiftRetrySettle
	tests := []struct {
		name  string
		exec  *config.PurchaseExecution
		since time.Duration
		want  time.Duration
	}{
		{"just written waits full settle", retryRow(1, false, rs), 0, s},
		{"midway", retryRow(1, false, rs), 4 * time.Minute, 6 * time.Minute},
		{"one nanosecond short", retryRow(1, false, rs), s - time.Nanosecond, time.Nanosecond},
		{"exactly settled", retryRow(1, false, rs), s, 0},
		{"long settled", retryRow(1, false, rs), time.Hour, 0},
		{"never submitted attempt 0 skips wait", retryRow(0, true, rs), 0, 0},
		{"attempt 1 no_submission still waits", retryRow(1, true, rs), 0, s},
		{"non-redshift never waits", retryRow(1, false, ec2), 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RedshiftRetryWait(tc.exec, tc.since))
		})
	}
}

func TestRedshiftRetryRefusalsContainNoLegacyWording(t *testing.T) {
	for _, text := range []string{redshiftRetryNoStampRefusal, redshiftRetryHorizonRefusal} {
		assert.NotContains(t, text, "legacy")
	}
}
