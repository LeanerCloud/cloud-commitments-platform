//go:build integration

package purchase

// Real PostgreSQL tests for reconcile paging and concurrent reconcilers (#1830).

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListFiredLadderTranches_KeysetPagesCoverEveryRowInOrder(t *testing.T) {
	r := newLadderRig(t)
	want := map[string]bool{}
	for i := 0; i < 5; i++ {
		id, _ := r.firedTranche("running", false, time.Minute)
		want[id] = true
	}
	// Equal scheduled_date for two rows forces the id tiebreak across a page edge.
	_, err := r.pool.Exec(r.ctx, `UPDATE ladder_tranches SET scheduled_date = (SELECT min(scheduled_date) FROM ladder_tranches) WHERE id = ANY($1)`, keys(want))
	require.NoError(t, err)

	var got []config.LadderTrancheDB
	var after *config.LadderFiredCursor
	for {
		page, err := r.store.ListFiredLadderTranches(r.ctx, after, 2)
		require.NoError(t, err)
		got = append(got, page...)
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		after = &config.LadderFiredCursor{ScheduledDate: last.ScheduledDate, ID: last.ID}
	}

	require.Len(t, got, 5)
	assert.True(t, sort.SliceIsSorted(got, func(i, j int) bool {
		if !got[i].ScheduledDate.Equal(got[j].ScheduledDate) {
			return got[i].ScheduledDate.Before(got[j].ScheduledDate)
		}
		return got[i].ID < got[j].ID
	}), "pages must come back in (scheduled_date, id) order")
	for _, tr := range got {
		assert.True(t, want[tr.ID], "unexpected tranche %s", tr.ID)
		delete(want, tr.ID)
	}
	assert.Empty(t, want, "every fired tranche must appear exactly once")
}

func TestReconcileFiredTranches_ResolvableTrancheBehindFullPageOfUnresolvable(t *testing.T) {
	r := newLadderRig(t)
	for i := 0; i < 3; i++ {
		r.firedTranche("running", false, time.Minute)
	}
	// Scheduled last, so it sits on the second page of size 3.
	last, _ := r.firedTranche("failed", true, time.Minute)
	_, err := r.pool.Exec(r.ctx, `UPDATE ladder_tranches SET scheduled_date = now() WHERE id = $1`, last)
	require.NoError(t, err)

	res, err := reconcileFiredTranches(r.ctx, r.store, stubVisibility{true}, time.Now(), 3)

	require.NoError(t, err)
	assert.Equal(t, LadderReconcileResult{Checked: 4, Unresolved: 3, Failed: 1}, res)
	status, _ := r.state(last)
	assert.Equal(t, "failed", status)
}

// barrierStore makes both reconcilers list the tranche before either locks it,
// so the race is on the row lock, not on the list.
type barrierStore struct {
	*config.PostgresStore
	wg sync.WaitGroup
}

func (b *barrierStore) ListFiredLadderTranches(ctx context.Context, after *config.LadderFiredCursor, limit int) ([]config.LadderTrancheDB, error) {
	out, err := b.PostgresStore.ListFiredLadderTranches(ctx, after, limit)
	if after == nil && err == nil && len(out) > 0 {
		b.wg.Done()
		b.wg.Wait()
	}
	return out, err
}

// The tranche row lock serializes the two sweeps. The loser blocks in
// LockLadderTrancheChainTx, then re-reads the row as no longer fired and gets
// ErrLadderTrancheNotFired, which counts as LostRace (not Errored).
// The transition compare-and-swap is a second guard: this test reaches it only
// if the row lock is removed, so the lock is what it pins.
func TestReconcileLadderTranches_ConcurrentSweepsTransitionOnce(t *testing.T) {
	r := newLadderRig(t)
	id, _ := r.firedTranche("failed", true, time.Minute)
	store := &barrierStore{PostgresStore: r.store}
	store.wg.Add(2)

	results := make([]LadderReconcileResult, 2)
	errs := make([]error, 2)
	var done sync.WaitGroup
	for i := range results {
		done.Add(1)
		go func() {
			defer done.Done()
			results[i], errs[i] = ReconcileLadderTranches(r.ctx, store, stubVisibility{true}, time.Now())
		}()
	}
	done.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, 1, results[0].Failed+results[1].Failed, "exactly one sweep transitions the tranche")
	assert.Equal(t, 1, results[0].LostRace+results[1].LostRace, "the other sweep loses the race")
	assert.Zero(t, results[0].Errored+results[1].Errored)
	status, _ := r.state(id)
	assert.Equal(t, "failed", status)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
