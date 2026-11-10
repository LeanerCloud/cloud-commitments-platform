package config

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ceiling shares the cap's validation but names its own field.
func TestLadderHourlyMicros_FieldNameLabelsEveryError(t *testing.T) {
	t.Parallel()
	for _, in := range []float64{0, -1, 1.1234567, 1e13, math.NaN()} {
		_, err := ladderHourlyMicros("max_hourly_commit_per_sweep", in)
		require.Error(t, err, in)
		assert.Contains(t, err.Error(), "max_hourly_commit_per_sweep", in)
		_, err = LadderCapMicros(in)
		require.Error(t, err, in)
		assert.Contains(t, err.Error(), "max_hourly_commit_per_run", in)
		assert.NotContains(t, err.Error(), "per_sweep", in)
	}
	got, err := ladderHourlyMicros("max_hourly_commit_per_sweep", 12.5)
	require.NoError(t, err)
	assert.Equal(t, int64(12_500_000), got)
}
