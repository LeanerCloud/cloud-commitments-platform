//go:build integration
// +build integration

package config

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shiftClaimClock moves every claim timestamp into the past, which is the same as
// advancing the database clock by d for the claim rules (they only compare the
// stored instants with now()).
func shiftClaimClock(ctx context.Context, t *testing.T, store *PostgresStore, d time.Duration) {
	t.Helper()
	_, err := store.db.Exec(ctx, `UPDATE purchase_claims
		SET expires_at = expires_at - make_interval(secs => $1::float8),
		    last_renewed_at = last_renewed_at - make_interval(secs => $1::float8)`, d.Seconds())
	require.NoError(t, err)
}

// TestPostgresPurchaseClaimer_Conformance holds the Postgres store to the same
// rules as the in-memory reference in cloud-commitments-go.
func TestPostgresPurchaseClaimer_Conformance(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)
	common.RunPurchaseClaimerConformance(t, func(t *testing.T) (common.PurchaseClaimer, func(time.Duration)) {
		_, err := store.db.Exec(ctx, `TRUNCATE purchase_claims`)
		require.NoError(t, err)
		return store, func(d time.Duration) { shiftClaimClock(ctx, t, store, d) }
	})
}

// TestPostgresPurchaseClaimer_ConcurrentAcquireIsExclusive is the cross-process
// proof: many callers race for one token on real connections and exactly one wins.
func TestPostgresPurchaseClaimer_ConcurrentAcquireIsExclusive(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)
	const callers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var won, held int
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.AcquireClaim(ctx, "race-token", fmt.Sprintf("holder-%d", i), time.Minute, time.Hour)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case assert.ErrorIs(t, err, common.ErrClaimHeld):
				held++
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, won)
	assert.Equal(t, callers-1, held)
}

// TestPostgresPurchaseClaimer_ConcurrentTakeoverBumpsGenerationOnce: callers racing
// for one expired row produce exactly one takeover (generation 2).
func TestPostgresPurchaseClaimer_ConcurrentTakeoverBumpsGenerationOnce(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)
	_, err := store.AcquireClaim(ctx, "tk", "old", time.Minute, time.Hour)
	require.NoError(t, err)
	shiftClaimClock(ctx, t, store, 2*time.Minute)
	const callers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var gens []int64
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := store.AcquireClaim(ctx, "tk", fmt.Sprintf("new-%d", i), time.Minute, time.Hour)
			if err == nil {
				mu.Lock()
				gens = append(gens, c.Generation)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, []int64{2}, gens)
}

// TestPostgresPurchaseClaimer_StoreSideClock: a caller cannot influence the lease
// with its own clock because it passes only durations; the lease end equals the
// database now() plus the ttl.
func TestPostgresPurchaseClaimer_StoreSideClock(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)
	_, err := store.AcquireClaim(ctx, "clk", "a", 90*time.Second, time.Hour)
	require.NoError(t, err)
	var secs float64
	require.NoError(t, store.db.QueryRow(ctx,
		`SELECT extract(epoch FROM expires_at - created_at)::float8 FROM purchase_claims WHERE idempotency_token = 'clk'`).Scan(&secs))
	assert.InDelta(t, 90, secs, 1)
}

// TestPostgresPurchaseClaimer_StaleHandleCannotTouchSuccessor: after a successor
// (same holder, so only the generation differs) takes the token, the old handle cannot Advance, complete or delete the new row.
func TestPostgresPurchaseClaimer_StaleHandleCannotTouchSuccessor(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)
	old, err := store.AcquireClaim(ctx, "stale", "a", time.Minute, time.Hour)
	require.NoError(t, err)
	require.NoError(t, store.ReleaseClaim(ctx, old, common.ClaimOutcome{}))
	cur, err := store.AcquireClaim(ctx, "stale", "a", time.Minute, time.Hour)
	require.NoError(t, err, "same holder, new row, generation restarts at 1")
	shiftClaimClock(ctx, t, store, 2*time.Minute)
	cur2, err := store.AcquireClaim(ctx, "stale", "a", time.Minute, time.Hour)
	require.NoError(t, err)
	require.EqualValues(t, 2, cur2.Generation)

	for _, stale := range []common.Claim{old, cur} {
		require.ErrorIs(t, store.AdvanceClaim(ctx, stale, common.ClaimSending), common.ErrClaimLost)
		require.ErrorIs(t, store.ReleaseClaim(ctx, stale, common.ClaimOutcome{OrderID: "o-stale"}), common.ErrClaimLost)
		require.ErrorIs(t, store.ReleaseClaim(ctx, stale, common.ClaimOutcome{}), common.ErrClaimLost)
	}
	var holder, phase string
	var gen int64
	var orderID *string
	require.NoError(t, store.db.QueryRow(ctx,
		`SELECT holder, generation, phase, order_id FROM purchase_claims WHERE idempotency_token = 'stale'`).Scan(&holder, &gen, &phase, &orderID))
	assert.Equal(t, "a", holder)
	assert.EqualValues(t, 2, gen)
	assert.Equal(t, "claimed", phase)
	assert.Nil(t, orderID)
}
