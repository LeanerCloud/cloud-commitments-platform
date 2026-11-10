//go:build integration

package purchase

// Real PostgreSQL tests for D5 lock behavior and the claim ceiling: concurrent
// sweeps, a busy config row, a held claim, and the 24h execution window.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLadderD5_ConcurrentSweepsDisableOnce(t *testing.T) {
	r := newLadderRig(t)
	id, _ := r.firedTranche("canceled", false, time.Minute)
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
	assert.Equal(t, 1, results[0].ConfigsDisabled+results[1].ConfigsDisabled)
	assert.Equal(t, 1, results[0].Failed+results[1].Failed)
	assert.Equal(t, 1, results[0].LostRace+results[1].LostRace)
	r.requireFailed(id, "approval_canceled")
	assert.False(t, r.enabled())
}

func TestLadderD5_BusyConfigRowDefersWithoutWaiting(t *testing.T) {
	r := newLadderRig(t)
	id, _ := r.firedTranche("canceled", false, time.Minute)
	writer, err := r.pool.Begin(r.ctx)
	require.NoError(t, err)
	released := false
	defer func() {
		if !released {
			assert.NoError(t, writer.Rollback(r.ctx))
		}
	}()
	_, err = writer.Exec(r.ctx, `UPDATE ladder_configs SET cadence = cadence WHERE id = $1`, r.cfgID)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	defer cancel()
	res, sweepErr := ReconcileLadderTranches(ctx, r.store, stubVisibility{true}, time.Now())

	require.NoError(t, sweepErr, "a busy config is deferred, not an error")
	assert.Equal(t, LadderReconcileResult{Checked: 1, Deferred: 1}, res)
	status, _ := r.state(id)
	assert.Equal(t, "fired", status)
	require.NoError(t, writer.Commit(r.ctx))
	released = true
	assert.True(t, r.enabled())

	res = r.reconcile(true)
	r.requireFailed(id, "approval_canceled")
	assert.False(t, r.enabled())
	assert.Equal(t, 1, res.ConfigsDisabled)
}

func TestLadderD5_HeldClaimDoesNotBlockTheDisable(t *testing.T) {
	r := newLadderRig(t)
	id, _ := r.firedTranche("canceled", false, time.Minute)
	next := r.scheduledTranche()
	held, release := make(chan struct{}), make(chan struct{})
	claimErr := make(chan error, 1)
	go func() {
		_, err := r.claim(r.ctx, next, 1_000_000_000_000, func() { close(held); <-release })
		claimErr <- err
	}()
	<-held

	ctx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	defer cancel()
	res, err := ReconcileLadderTranches(ctx, r.store, stubVisibility{true}, time.Now())
	close(release)

	require.NoError(t, err)
	assert.Equal(t, 1, res.ConfigsDisabled)
	r.requireFailed(id, "approval_canceled")
	require.NoError(t, <-claimErr)
}

func TestLadderD5_CeilingStillCountsTheFailedTranche(t *testing.T) {
	r := newLadderRig(t)
	id, exec := r.firedTranche("canceled", false, time.Hour)
	r.reconcile(true)
	r.requireFailed(id, "approval_canceled")
	next := r.scheduledTranche()
	_, err := r.pool.Exec(r.ctx, `UPDATE ladder_configs SET enabled = true WHERE id = $1`, r.cfgID)
	require.NoError(t, err)

	claimed, err := r.claim(r.ctx, next, 5_000_000, nil)
	require.NoError(t, err)
	assert.Empty(t, claimed, "the failed tranche's execution still uses the whole 5 USD/hr budget for 24h")

	_, err = r.pool.Exec(r.ctx, `UPDATE purchase_executions SET created_at = NOW() - interval '25 hours' WHERE execution_id = $1`, exec)
	require.NoError(t, err)
	claimed, err = r.claim(r.ctx, next, 5_000_000, nil)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, next, claimed[0].ID)
}
