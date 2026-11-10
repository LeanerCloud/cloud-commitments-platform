//go:build integration

package api

// Real PostgreSQL: the reconciler's D5 disable against a concurrent tranche
// amend. Amend locks the config row, then the tranche row; the reconciler holds
// the tranche row first, so its config lock must be NOWAIT or the two deadlock.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func isDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

func TestLadderD5Amend_ReconcileAndAmendNeverDeadlock(t *testing.T) {
	r := newLadderRetryRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var accountID string
	require.NoError(t, r.store.WithTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT cloud_account_id::text FROM ladder_configs WHERE id = $1`, r.cfgID).Scan(&accountID)
	}))

	var deferredRounds, committedRounds int
	const rounds = 25
	for round := 0; round < rounds; round++ {
		if round > 0 {
			r.addFiredTranche(t)
			_, err := r.pool.Exec(ctx, `UPDATE ladder_configs SET enabled = true WHERE id = $1`, r.cfgID)
			require.NoError(t, err)
		}
		_, err := r.pool.Exec(ctx, `UPDATE purchase_executions SET status = 'canceled', no_submission = false WHERE execution_id = $1`, r.failedExec.ExecutionID)
		require.NoError(t, err)

		paused := &pausedChainStore{PostgresStore: r.store, locked: make(chan struct{}), release: make(chan struct{})}
		roundCtx, roundCancel := context.WithTimeout(ctx, 10*time.Second)
		type reconcileOutcome struct {
			res purchase.LadderReconcileResult
			err error
		}
		reconciled := make(chan reconcileOutcome, 1)
		go func() {
			res, recErr := purchase.ReconcileLadderTranches(roundCtx, paused, visibleNever{}, time.Now())
			reconciled <- reconcileOutcome{res, recErr}
		}()
		<-paused.locked

		revision := int64(0)
		amended := make(chan error, 1)
		go func() {
			_, amendErr := r.store.AmendLadderTranche(roundCtx, r.trancheID, accountID, "aws", "d5@example.com", config.LadderAmendment{
				ExpectedRevision: &revision, ScheduledDate: time.Now().Add(48 * time.Hour), AmountUSDHr: "6"})
			amended <- amendErr
		}()
		if round%2 == 0 {
			// The amend holds the config row and waits for the tranche row.
			if err, finished := testhelpers.AwaitBlockedBy(roundCtx, t, r.pool, paused.pid, amended); finished {
				close(paused.release)
				<-reconciled
				t.Fatalf("round %d: amend finished while the reconciler held the tranche row (err=%v)", round, err)
			}
		}
		close(paused.release)

		rec := <-reconciled
		amendErr := <-amended
		roundCancel()
		require.NoError(t, rec.err, "round %d", round)
		assert.False(t, isDeadlock(amendErr), "round %d: amend deadlocked: %v", round, amendErr)
		if amendErr != nil {
			require.ErrorIs(t, amendErr, config.ErrLadderAmendConflict, "round %d", round)
		}

		if rec.res.Deferred == 1 {
			deferredRounds++
			assert.Equal(t, "fired", r.trancheStatus(t), "round %d: a deferred tranche stays fired", round)
			res, err := purchase.ReconcileLadderTranches(ctx, r.store, visibleNever{}, time.Now())
			require.NoError(t, err)
			assert.Equal(t, 1, res.ConfigsDisabled, "round %d: the next sweep disables", round)
		} else {
			committedRounds++
			assert.Equal(t, 1, rec.res.ConfigsDisabled, "round %d", round)
		}
		assert.Equal(t, "failed", r.trancheStatus(t), "round %d", round)
		var enabled bool
		require.NoError(t, r.pool.QueryRow(ctx, `SELECT enabled FROM ladder_configs WHERE id = $1`, r.cfgID).Scan(&enabled))
		assert.False(t, enabled, "round %d", round)
	}
	assert.Positive(t, deferredRounds, "the amend never held the config row first: the NOWAIT path was not exercised")
	t.Logf("deferred=%d committed=%d", deferredRounds, committedRounds)
}
