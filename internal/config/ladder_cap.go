package config

import (
	"fmt"
	"strconv"
	"strings"
)

// MinLadderHourlyCommit and MaxLadderHourlyCommitLimit bound
// ladder_configs.max_hourly_commit_per_run. Below one micro-dollar the
// NUMERIC(20,6) column rounds to 0; the upper bound keeps the micro-dollar
// value inside int64 and far above any real hourly commitment.
const (
	MinLadderHourlyCommit      = 0.000001
	MaxLadderHourlyCommitLimit = 1e12
)

// LadderCapMicros returns the per-run hourly cap in exact micro-dollars. Caps
// with more than 6 decimals are rejected instead of rounded, so the stored and
// the compared value are identical (same approach as StepCapCents).
func LadderCapMicros(v float64) (int64, error) {
	return ladderHourlyMicros("max_hourly_commit_per_run", v)
}

// ladderHourlyMicros is the validation shared by the per-run cap and the
// per-sweep ceiling: same bounds and precision, the field name only labels the
// error.
func ladderHourlyMicros(field string, v float64) (int64, error) {
	if !(v >= MinLadderHourlyCommit) || !(v <= MaxLadderHourlyCommitLimit) {
		return 0, fmt.Errorf("%s must be between %v and %v, got: %v", field, MinLadderHourlyCommit, MaxLadderHourlyCommitLimit, v)
	}
	whole, frac, _ := strings.Cut(strconv.FormatFloat(v, 'f', -1, 64), ".")
	if len(frac) > 6 {
		return 0, fmt.Errorf("%s must have at most 6 decimal places, got: %v", field, v)
	}
	n, err := strconv.ParseInt(whole+(frac + "000000")[:6], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is not representable: %w", field, err)
	}
	return n, nil
}
