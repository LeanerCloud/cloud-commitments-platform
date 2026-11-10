//go:build integration

package config

// Real PostgreSQL tests for the sweep ceiling's retry-chain accounting (issue
// #1927): a retry fires the commitment again, so a tranche counts while any
// execution of its retry chain is inside the window. Amounts are USD per hour.

import (
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retryChain saves a tranche of cfgID whose executions were created at the
// given ages before asOf (amount 4), first attempt first. Each execution points at the
// next through retry_execution_id; the tranche names only the first.
func (f *sweepCeilingFixture) retryChain(t *testing.T, cfgID string, ages ...time.Duration) []string {
	t.Helper()
	tr := lifecycleTranche(cfgID, 4, ladder.TrancheStatusFailed, f.asOf.Add(-72*time.Hour))
	require.NoError(t, f.store.SaveLadderTranches(f.ctx, []LadderTrancheDB{tr}))
	ids := make([]string, len(ages))
	for i, age := range ages {
		require.NoError(t, f.store.db.QueryRow(f.ctx,
			`INSERT INTO purchase_executions (plan_id, scheduled_date, created_at) VALUES ($1, NOW(), $2) RETURNING execution_id`,
			f.planID, f.asOf.Add(-age)).Scan(&ids[i]))
	}
	for i := 1; i < len(ids); i++ {
		_, err := f.store.db.Exec(f.ctx, `UPDATE purchase_executions SET retry_execution_id = $2 WHERE execution_id = $1`, ids[i-1], ids[i])
		require.NoError(t, err)
	}
	_, err := f.store.db.Exec(f.ctx, `UPDATE ladder_tranches SET execution_id = $2 WHERE id = $1`, tr.ID, ids[0])
	require.NoError(t, err)
	return ids
}

// Chain amount 4, due row 1, ceiling 4: the chain counts exactly when the due
// row has no budget left.
func TestClaimCeiling_RetryChainCountsWhileAnyExecutionIsInWindow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ages    []time.Duration
		counted bool
	}{
		{"first 48h old, retry 1h old", []time.Duration{48 * time.Hour, time.Hour}, true},
		{"depth 3, only the newest in window", []time.Duration{72 * time.Hour, 48 * time.Hour, time.Hour}, true},
		{"newest exactly one window old", []time.Duration{72 * time.Hour, 48 * time.Hour, 24 * time.Hour}, false},
		{"newest a microsecond inside the window", []time.Duration{72 * time.Hour, 24*time.Hour - time.Microsecond}, true},
		{"entirely older than the window", []time.Duration{72 * time.Hour, 48 * time.Hour, 30 * time.Hour}, false},
		{"single recent execution", []time.Duration{time.Hour}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSweepCeilingFixture(t)
			f.retryChain(t, f.cfgID, tc.ages...)
			rows := f.due(t, 1)
			got, err := f.claim(4*usd, f.asOf, rows)
			require.NoError(t, err)
			if tc.counted {
				assert.Empty(t, got)
				assert.Zero(t, f.calls.Load())
			} else {
				assert.Equal(t, claimedIDs(rows), claimedIDs(got))
			}
		})
	}
}

func TestClaimCeiling_RetryChainIgnoresOtherConfigAndMissingExecution(t *testing.T) {
	f := newSweepCeilingFixture(t)
	f.retryChain(t, f.otherConfig(t), 48*time.Hour, time.Hour)
	unfired := lifecycleTranche(f.cfgID, 4, ladder.TrancheStatusFailed, f.asOf.Add(-48*time.Hour))
	require.NoError(t, f.store.SaveLadderTranches(f.ctx, []LadderTrancheDB{unfired}))
	rows := f.due(t, 1)
	got, err := f.claim(4*usd, f.asOf, rows)
	require.NoError(t, err)
	assert.Equal(t, claimedIDs(rows), claimedIDs(got))
}

// Two in-window executions of one tranche are one commitment: with ceiling 5
// and chain amount 4 the due row of 1 still fits exactly.
func TestClaimCeiling_RetryChainCountsATrancheOnce(t *testing.T) {
	f := newSweepCeilingFixture(t)
	f.retryChain(t, f.cfgID, 48*time.Hour, 2*time.Hour, time.Hour)
	rows := f.due(t, 1)
	got, err := f.claim(5*usd, f.asOf, rows)
	require.NoError(t, err)
	assert.Equal(t, claimedIDs(rows), claimedIDs(got))
}

// retry_execution_id cycles should not exist; one must still terminate and
// count the tranche once.
func TestClaimCeiling_RetryCycleTerminatesAndCountsOnce(t *testing.T) {
	f := newSweepCeilingFixture(t)
	ids := f.retryChain(t, f.cfgID, 48*time.Hour, time.Hour)
	_, err := f.store.db.Exec(f.ctx, `UPDATE purchase_executions SET retry_execution_id = $2 WHERE execution_id = $1`, ids[1], ids[0])
	require.NoError(t, err)
	rows := f.due(t, 1)
	got, err := f.claim(5*usd, f.asOf, rows)
	require.NoError(t, err)
	assert.Equal(t, claimedIDs(rows), claimedIDs(got))
}
