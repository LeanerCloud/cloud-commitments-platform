//go:build integration

package purchase

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// failingSaveStore fails the n-th execution save, which makes the claim
// callback of that tranche fail.
type failingSaveStore struct {
	*config.PostgresStore
	failOn int
	calls  int
}

func (s *failingSaveStore) SavePurchaseExecutionTx(ctx context.Context, tx pgx.Tx, e *config.PurchaseExecution) error {
	s.calls++
	if s.calls == s.failOn {
		return errors.New("save failed")
	}
	return s.PostgresStore.SavePurchaseExecutionTx(ctx, tx, e)
}

// barrierFireStore makes both concurrent sweeps finish their resolve read
// before either claims, so the claims genuinely contend.
type barrierFireStore struct {
	*config.PostgresStore
	arrived *sync.WaitGroup
}

func (s *barrierFireStore) ListDueLadderTranches(ctx context.Context, configID string, asOf time.Time, limit int, layers []ladder.LayerType) ([]config.LadderDueRow, error) {
	rows, err := s.PostgresStore.ListDueLadderTranches(ctx, configID, asOf, limit, layers)
	s.arrived.Done()
	s.arrived.Wait()
	return rows, err
}

func TestLadderFire_PartialFailureRollsTheWholeClaimBack(t *testing.T) {
	f := newFireRig(t)
	failing := &failingSaveStore{PostgresStore: f.store, failOn: 2}
	res, err := f.sweep(func(d *LadderFireDeps) { d.Store = failing })
	require.Error(t, err)
	assert.Equal(t, 1, res.Errored)
	f.requireUntouched()

	res, err = f.sweep(nil)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Claimed)
	assert.Equal(t, 2, f.executionCount())
}

func TestLadderFire_UnclaimableTrancheDoesNotBlockTheOthers(t *testing.T) {
	f := newFireRig(t)
	require.NoError(t, f.store.SetLadderSweepCeiling(f.ctx, f.cfgID, ptr(10.0)))
	_, err := f.pool.Exec(f.ctx, `UPDATE ladder_tranches SET term = $2 WHERE id = $1`, f.ids[1], string(ladder.Term3Year))
	require.NoError(t, err)
	res, err := f.sweep(nil)
	require.NoError(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 3, Unresolved: 1, Claimed: 2, Announced: 2}, res)
	assert.Equal(t, []string{"fired", "scheduled", "fired"}, []string{f.status(0), f.status(1), f.status(2)})
}

func TestLadderFire_ConcurrentSweepsClaimEachTrancheOnce(t *testing.T) {
	f := newFireRig(t)
	var wg, arrived sync.WaitGroup
	arrived.Add(2)
	store := &barrierFireStore{PostgresStore: f.store, arrived: &arrived}
	start := make(chan struct{})
	claimed := make([]int, 2)
	for i := range claimed {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			d := f.deps()
			d.Store = store
			res, err := RunLadderFire(f.ctx, d)
			assert.NoError(t, err)
			claimed[i] = res.Claimed
		}()
	}
	close(start)
	wg.Wait()
	assert.Equal(t, 2, claimed[0]+claimed[1])
	assert.Equal(t, 2, f.executionCount())
	assert.Equal(t, "scheduled", f.status(2))
}

func TestLadderFire_ReconcilerLeavesASweepsTranchesAlone(t *testing.T) {
	f := newFireRig(t)
	_, err := f.sweep(nil)
	require.NoError(t, err)
	res := f.reconcile(false)
	assert.Equal(t, 2, res.Checked, "the reconciler must have looked at both and left them")
	assert.Zero(t, res.Failed)
	assert.Zero(t, res.ConfigsDisabled)
	assert.Equal(t, []string{"fired", "fired"}, []string{f.status(0), f.status(1)})
	var enabled bool
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT enabled FROM ladder_configs WHERE id = $1`, f.cfgID).Scan(&enabled))
	assert.True(t, enabled)
}

// The sweep alone cannot buy: an execution it created is unannounced, so the
// execute-time recheck refuses it even after its window has passed, before any
// provider is reached (the provider factory mock has no expectation).
func TestLadderFire_UnannouncedExecutionIsRefusedAtFireTime(t *testing.T) {
	f := newFireRig(t)
	// The notice cannot be sent, so nothing is stamped.
	_, err := f.sweep(func(d *LadderFireDeps) { d.Notifier = &captureNotifier{err: errors.New("no mail")} })
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `UPDATE purchase_executions SET scheduled_execution_at = NOW() - interval '1 minute'
		WHERE created_by_system AND idempotency_key = $1`, f.ids[0])
	require.NoError(t, err)
	var execID string
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT execution_id FROM purchase_executions WHERE idempotency_key = $1`, f.ids[0]).Scan(&execID))
	row, err := f.store.GetExecutionByID(f.ctx, execID)
	require.NoError(t, err)

	mockEmail := new(MockEmailSender)
	mockEmail.On("SendPurchaseFailedNotification", mock.Anything, mock.Anything).Return(nil).Maybe()
	m := &Manager{config: f.store, email: mockEmail, providerFactory: new(MockProviderFactory),
		credStore: awsAccessKeyCredStore(), dashboardURL: "https://dashboard.example.com"}
	assert.Equal(t, fireErrored, m.fireOneDue(f.ctx, row))

	got, err := f.store.GetExecutionByID(f.ctx, row.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, "failed", got.Status)
	assert.True(t, got.NoSubmission)
	assert.Contains(t, got.Error, ladderNoticeNotSentNote)
}
