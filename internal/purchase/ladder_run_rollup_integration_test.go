//go:build integration

package purchase

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (r *ladderRig) newRun() string {
	run, err := r.store.SaveLadderRun(r.ctx, &config.LadderRunDB{ConfigID: &r.cfgID, StartedAt: time.Now().Add(-time.Hour), Status: ladder.RunStatusPlanned, Mode: ptr(string(ladder.ModeAutoApprove))})
	require.NoError(r.t, err)
	return run.ID
}

func (r *ladderRig) runStatus(runID string) ladder.RunStatus {
	var s string
	require.NoError(r.t, r.pool.QueryRow(r.ctx, `SELECT status FROM ladder_runs WHERE id = $1`, runID).Scan(&s))
	return ladder.RunStatus(s)
}

func TestReconcileLadderTranches_RunRollup(t *testing.T) {
	t.Run("one settled tranche leaves the run planned, both completed completes it, re-run is a no-op", func(t *testing.T) {
		r := newLadderRig(t)
		run := r.newRun()
		first, _ := r.firedTrancheInRun(run, "completed", false, time.Minute)
		second, _ := r.firedTrancheInRun(run, "running", false, time.Minute)
		r.reconcile(true)
		st, _ := r.state(first)
		assert.Equal(t, "completed", st)
		assert.Equal(t, ladder.RunStatusPlanned, r.runStatus(run), "the other tranche is still fired")

		_, err := r.pool.Exec(r.ctx, `UPDATE purchase_executions SET status = 'completed' WHERE execution_id = (SELECT execution_id FROM ladder_tranches WHERE id = $1)`, second)
		require.NoError(t, err)
		r.reconcile(true)
		assert.Equal(t, ladder.RunStatusCompleted, r.runStatus(run))
		r.reconcile(true)
		assert.Equal(t, ladder.RunStatusCompleted, r.runStatus(run))
	})

	t.Run("a failed tranche fails the run once all are settled", func(t *testing.T) {
		r := newLadderRig(t)
		run := r.newRun()
		r.firedTrancheInRun(run, "completed", false, time.Minute)
		r.firedTrancheInRun(run, "failed", true, time.Minute)
		r.reconcile(true)
		assert.Equal(t, ladder.RunStatusFailed, r.runStatus(run))
	})

	t.Run("a canceled run stays canceled", func(t *testing.T) {
		r := newLadderRig(t)
		run := r.newRun()
		r.firedTrancheInRun(run, "completed", false, time.Minute)
		_, err := r.pool.Exec(r.ctx, `UPDATE ladder_runs SET status = 'canceled' WHERE id = $1`, run)
		require.NoError(t, err)
		r.reconcile(true)
		assert.Equal(t, ladder.RunStatus("canceled"), r.runStatus(run))
	})

	t.Run("a still-scheduled sibling stays amendable while the run is planned", func(t *testing.T) {
		r := newLadderRig(t)
		run := r.newRun()
		first, _ := r.firedTrancheInRun(run, "completed", false, time.Minute)
		basis, rate := config.LadderAmountCommitment, "1"
		second := config.LadderTrancheDB{ID: uuid.New().String(), ConfigID: &r.cfgID, RunID: &run, LayerType: ladder.LayerComputeSP,
			Term: ladder.Term1Year, PaymentOption: ladder.PaymentNoUpfront, Status: ladder.TrancheStatusScheduled,
			AmountUSDHr: 5, ScheduledDate: time.Now().Add(time.Hour), AmountBasis: &basis, CommitmentRate: &rate}
		require.NoError(t, r.store.SaveLadderTranches(r.ctx, []config.LadderTrancheDB{second}))
		r.reconcile(true)
		st, _ := r.state(first)
		require.Equal(t, "completed", st)
		assert.Equal(t, ladder.RunStatusPlanned, r.runStatus(run))

		var accountID string
		require.NoError(t, r.pool.QueryRow(r.ctx, `SELECT cloud_account_id FROM ladder_configs WHERE id = $1`, r.cfgID).Scan(&accountID))
		zero := int64(0)
		_, err := r.store.AmendLadderTranche(r.ctx, second.ID, accountID, "aws", "actor",
			config.LadderAmendment{ExpectedRevision: &zero, ScheduledDate: time.Now().Add(2 * time.Hour), AmountUSDHr: "2.000000"})
		require.NoError(t, err)
	})

	t.Run("two racing settles of the last tranches make exactly one run transition", func(t *testing.T) {
		r := newLadderRig(t)
		run := r.newRun()
		r.firedTrancheInRun(run, "completed", false, time.Minute)
		r.firedTrancheInRun(run, "completed", false, time.Minute)
		fired, err := r.store.ListFiredLadderTranches(r.ctx, nil, 10)
		require.NoError(t, err)
		require.Len(t, fired, 2)
		store := &transitionCountingStore{PostgresStore: r.store}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, tr := range fired {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				var res LadderReconcileResult
				assert.NoError(t, reconcileOne(r.ctx, store, stubVisibility{true}, time.Now(), tr, &res))
			}()
		}
		close(start)
		wg.Wait()
		assert.Equal(t, ladder.RunStatusCompleted, r.runStatus(run))
		assert.Equal(t, 1, store.wins, "exactly one racing rollup wins the planned -> completed update")
	})
}

// transitionCountingStore counts run transitions that actually updated a row.
type transitionCountingStore struct {
	*config.PostgresStore
	mu   sync.Mutex
	wins int
}

func (s *transitionCountingStore) TransitionLadderRunStatus(ctx context.Context, id string, from []ladder.RunStatus, to ladder.RunStatus) (*config.LadderRunDB, error) {
	row, err := s.PostgresStore.TransitionLadderRunStatus(ctx, id, from, to)
	if err == nil && row != nil {
		s.mu.Lock()
		s.wins++
		s.mu.Unlock()
	}
	return row, err
}
