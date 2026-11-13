package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func jsonObject(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// The history and inventory rows carry amount_basis only for legacy estimates
// (issue #669); normal rows omit the key.
func TestAmountBasisJSON_HistoryAndInventoryRows(t *testing.T) {
	t.Parallel()
	legacy := config.PurchaseHistoryRecord{AccountID: "a", PurchaseID: "p", Term: 1, Timestamp: time.Now(),
		AmountBasis: config.PurchaseAmountBasisLegacyWindow}
	normal := legacy
	normal.AmountBasis = ""

	assert.Equal(t, "legacy_window", jsonObject(t, legacy)["amount_basis"])
	assert.NotContains(t, jsonObject(t, normal), "amount_basis")
	assert.Equal(t, "legacy_window", jsonObject(t, buildInventoryCommitment(legacy, ""))["amount_basis"])
	assert.NotContains(t, jsonObject(t, buildInventoryCommitment(normal, "")), "amount_basis")
}

// The dashboard always reports the excluded count, including zero.
func TestAmountBasisJSON_DashboardExcludedCount(t *testing.T) {
	t.Parallel()
	got := jsonObject(t, DashboardSummaryResponse{ExcludedLegacyEstimateCount: 0})
	assert.Contains(t, got, "excluded_legacy_estimate_count")
	assert.EqualValues(t, 3, jsonObject(t, DashboardSummaryResponse{ExcludedLegacyEstimateCount: 3})["excluded_legacy_estimate_count"])
}
