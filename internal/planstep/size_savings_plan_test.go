package planstep

import (
	"encoding/json"
	"fmt"
	"math/big"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sizerSPRec(count int, hourly string) *config.RecommendationRecord {
	return &config.RecommendationRecord{
		Service: "savings-plans-compute",
		Count:   count,
		Details: json.RawMessage(fmt.Sprintf(`{"hourly_commitment":%s,"plan_type":"Compute"}`, hourly)),
	}
}

func TestSizeSavingsPlan(t *testing.T) {
	cases := []struct {
		name, hourly, want string
		pct                int64
		ratio              float64
	}{
		{"half", "0.58", "0.29", 50, 0.5},
		{"floors", "0.10", "0.03", 33, 0.3},
		{"never rounds up", "0.017", "0.01", 99, 0},
		{"non-cent stored", "1.23456", "0.61", 50, 0},
		{"below a cent", "0.01", "", 50, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := sizerSPRec(1, tc.hourly)
			out := *stored
			ratio, err := SizeSavingsPlan(&out, stored, big.NewRat(tc.pct, 100))
			require.NoError(t, err)
			if tc.want == "" {
				assert.Zero(t, ratio)
				assert.JSONEq(t, string(stored.Details), string(out.Details))
				return
			}
			assert.JSONEq(t, fmt.Sprintf(`{"hourly_commitment":%s,"plan_type":"Compute"}`, tc.want), string(out.Details))
			assert.Equal(t, 1, out.Count)
			assert.Greater(t, ratio, 0.0)
			if tc.ratio != 0 {
				assert.InDelta(t, tc.ratio, ratio, 1e-9)
			}
		})
	}
}

func TestSizeSavingsPlanRefusals(t *testing.T) {
	out := sizerSPRec(1, "1")
	_, err := SizeSavingsPlan(out, sizerSPRec(2, "1"), big.NewRat(1, 2))
	require.ErrorIs(t, err, ErrUnsupportedPlan)
	bad := sizerSPRec(1, "1")
	bad.Details = json.RawMessage(`{"plan_type":"Compute"}`)
	_, err = SizeSavingsPlan(out, bad, big.NewRat(1, 2))
	require.Error(t, err)
}
