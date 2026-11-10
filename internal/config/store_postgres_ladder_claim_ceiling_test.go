package config

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLadderCeilingPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		amounts []int64
		budget  int64
		want    int
	}{
		{"empty", nil, 10, 0},
		{"zero budget", []int64{1}, 0, 0},
		{"negative budget", []int64{1}, -5, 0},
		{"first row alone exceeds", []int64{8, 1}, 7, 0},
		{"exact fit", []int64{3_000_000, 4_000_000}, 7_000_000, 2},
		{"one micro short", []int64{3_000_000, 4_000_000}, 6_999_999, 1},
		{"stops at the first overflow, never skips ahead", []int64{5, 1}, 5, 1},
		{"an oversized head blocks later smaller rows", []int64{10, 1, 1}, 5, 0},
		{"all fit", []int64{1, 2, 3}, 100, 3},
		{"near-max amounts do not wrap", []int64{math.MaxInt64 - 1, math.MaxInt64 - 1}, math.MaxInt64, 1},
		{"max budget, max amount", []int64{math.MaxInt64}, math.MaxInt64, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, ladderCeilingPrefix(tc.amounts, tc.budget))
		})
	}
}
