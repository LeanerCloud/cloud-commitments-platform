package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
)

// TestMatchDuplicateInList pins each clause of the Go-side duplicate check
// that persistExecutionAndSuppressions runs over ListRecentSubmitsTx's rows:
// web source, window start (inclusive), creator (nil creator equals ""),
// and the recomputed idempotency key (which also hashes the creator, so the
// creator cases hold through either check). The SQL predicate applies the same
// source, window and creator filters; this keeps the Go side honest if the
// rows it is given ever widen (#266).
func TestMatchDuplicateInList(t *testing.T) {
	t.Parallel()
	now := time.Now()
	creator := "11111111-1111-1111-1111-111111111111"
	other := "22222222-2222-2222-2222-222222222222"
	recs := []config.RecommendationRecord{validRec()}
	key := mustPurchaseIdempotencyKey(creator, recs, 100)
	exec := func(mut func(e *config.PurchaseExecution)) config.PurchaseExecution {
		e := config.PurchaseExecution{
			ExecutionID: "cand", Status: "pending", Source: common.PurchaseSourceWeb,
			ScheduledDate: now.Add(-30 * time.Second), CreatedByUserID: &creator,
			Recommendations: append([]config.RecommendationRecord(nil), recs...), CapacityPercent: 100,
		}
		if mut != nil {
			mut(&e)
		}
		return e
	}
	for _, tc := range []struct {
		name    string
		creator string
		key     string
		cand    config.PurchaseExecution
		wantDup bool
	}{
		{name: "exact match", cand: exec(nil), creator: creator, key: key, wantDup: true},
		{name: "window start is inclusive", cand: exec(func(e *config.PurchaseExecution) { e.ScheduledDate = now.Add(-purchaseIdempotencyWindow) }), creator: creator, key: key, wantDup: true},
		{name: "before the window", cand: exec(func(e *config.PurchaseExecution) {
			e.ScheduledDate = now.Add(-purchaseIdempotencyWindow - time.Second)
		}), creator: creator, key: key, wantDup: false},
		{name: "not web-submitted", cand: exec(func(e *config.PurchaseExecution) { e.Source = common.PurchaseSourceCLI }), creator: creator, key: key, wantDup: false},
		{name: "other creator", cand: exec(func(e *config.PurchaseExecution) { e.CreatedByUserID = &other }), creator: creator, key: key, wantDup: false},
		{name: "no creator vs a creator", cand: exec(func(e *config.PurchaseExecution) { e.CreatedByUserID = nil }), creator: creator, key: key, wantDup: false},
		{name: "no creator on both sides", cand: exec(func(e *config.PurchaseExecution) { e.CreatedByUserID = nil }), creator: "", key: mustPurchaseIdempotencyKey("", recs, 100), wantDup: true},
		{name: "different capacity", cand: exec(func(e *config.PurchaseExecution) { e.CapacityPercent = 50 }), creator: creator, key: key, wantDup: false},
		{name: "different rec set", cand: exec(func(e *config.PurchaseExecution) { e.Recommendations[0].Count++ }), creator: creator, key: key, wantDup: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := matchDuplicateInList([]config.PurchaseExecution{tc.cand}, tc.creator, tc.key, now)
			if tc.wantDup {
				assert.NotNil(t, got)
			} else {
				assert.Nil(t, got)
			}
		})
	}
}

// TestMatchDuplicateInList_ReturnsTheMatchingRow covers what the removed
// findDuplicatePendingExecution test pinned beyond the clause table: the scan
// returns the matching row itself (not just "some row") from a mixed list,
// and a candidate whose key cannot be derived is skipped without ending the
// scan (#760).
func TestMatchDuplicateInList_ReturnsTheMatchingRow(t *testing.T) {
	t.Parallel()
	now := time.Now()
	creator := "11111111-1111-1111-1111-111111111111"
	recs := []config.RecommendationRecord{validRec()}
	key := mustPurchaseIdempotencyKey(creator, recs, 100)
	cand := func(id string, mut func(e *config.PurchaseExecution)) config.PurchaseExecution {
		e := config.PurchaseExecution{
			ExecutionID: id, Status: "pending", Source: common.PurchaseSourceWeb,
			ScheduledDate: now.Add(-10 * time.Second), CreatedByUserID: &creator,
			Recommendations: append([]config.RecommendationRecord(nil), recs...), CapacityPercent: 100,
		}
		mut(&e)
		return e
	}
	list := []config.PurchaseExecution{
		cand("other-count", func(e *config.PurchaseExecution) { e.Recommendations[0].Count = 99 }),
		cand("undecodable", func(e *config.PurchaseExecution) { e.Recommendations[0].Details = json.RawMessage(`{"not":`) }),
		cand("cli", func(e *config.PurchaseExecution) { e.Source = common.PurchaseSourceCLI }),
		cand("the-dup", func(*config.PurchaseExecution) {}),
	}
	got := matchDuplicateInList(list, creator, key, now)
	if assert.NotNil(t, got) {
		assert.Equal(t, "the-dup", got.ExecutionID)
	}
	assert.Nil(t, matchDuplicateInList(list[:3], creator, key, now), "no candidate matches without the-dup")
}
