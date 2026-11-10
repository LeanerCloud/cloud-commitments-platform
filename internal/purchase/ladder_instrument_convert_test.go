package purchase

import (
	"math"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFloorSPHourly(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{0.123456, 0.12345},
		{0.12345, 0.12345},
		{0.29, 0.29},
		{0.30000000000000004, 0.3},
		{1.1, 1.1},
		{7.000009, 7},
		{999999.999999, 999999.99999},
		{0.001, 0.001},
		{0.001009, 0.001},
		{1000000, 1000000},
	} {
		got, err := floorSPHourly(tc.in)
		require.NoError(t, err, "%v", tc.in)
		assert.Equal(t, tc.want, got, "%v", tc.in)
	}
}

func TestFloorSPHourly_Refuses(t *testing.T) {
	for _, in := range []float64{0.000999, 1000000.000001, 0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := floorSPHourly(in)
		assert.Error(t, err, "%v", in)
	}
}

func TestLadderTermYears(t *testing.T) {
	for in, want := range map[ladder.Term]int{ladder.Term1Year: 1, ladder.Term3Year: 3} {
		got, err := ladderTermYears(in)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	for _, in := range []ladder.Term{"", "2yr", "1", "3", "1y", "1YR"} {
		_, err := ladderTermYears(in)
		assert.Error(t, err, "%q", in)
	}
}

func TestSPMonthlyCost(t *testing.T) {
	assert.InDelta(t, 90.1185, spMonthlyCost(0.12345, 1), 1e-9)
	assert.InDelta(t, 90.1185, spMonthlyCost(0.12345, 3), 1e-9)
}

func TestRequireNoUpfront(t *testing.T) {
	require.NoError(t, requireNoUpfront(ladder.PaymentNoUpfront))
	for _, p := range []ladder.PaymentOption{ladder.PaymentAllUpfront, ladder.PaymentPartialUpfront, "", "No Upfront", "upfront", "monthly", "NO-UPFRONT"} {
		assert.Error(t, requireNoUpfront(p), "%q", p)
	}
	assert.ErrorContains(t, requireNoUpfront(ladder.PaymentAllUpfront), "unsupported in v1")
	assert.ErrorContains(t, requireNoUpfront(ladder.PaymentPartialUpfront), "unsupported in v1")
}
