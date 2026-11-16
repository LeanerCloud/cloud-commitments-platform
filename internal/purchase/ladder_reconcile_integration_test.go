//go:build integration

package purchase

// Real PostgreSQL tests for the ladder tranche reconciler (issue #118, PR-C2).
// Executions, tranches and the claim are real rows; only the commitment
// visibility check is a stub (labeled FIXTURE).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FIXTURE: stands in for the provider layer listing.
type stubVisibility struct{ visible bool }

func (s stubVisibility) CommitmentVisible(context.Context, config.LadderTrancheDB, string) (bool, error) {
	return s.visible, nil
}

type ladderRig struct {
	ctx    context.Context
	t      *testing.T
	store  *config.PostgresStore
	pool   *pgxpool.Pool
	cfgID  string
	planID string
}

func newLadderRig(t *testing.T) *ladderRig {
	ctx := context.Background()
	store, pool := rampStepStore(ctx, t)
	r := &ladderRig{ctx: ctx, t: t, store: store, pool: pool}
	acct := uuid.New().String()
	_, err := pool.Exec(ctx, `INSERT INTO cloud_accounts (id, name, enabled, provider, external_id, aws_is_org_root, created_at, updated_at)
		VALUES ($1, 'c2', true, 'aws', '123456789012', false, now(), now())`, acct)
	require.NoError(t, err)
	saved, err := store.UpsertLadderConfig(ctx, &config.LadderConfigDB{
		CloudAccountID: acct, Provider: "aws", Enabled: true, Mode: "email_approval", Cadence: "daily",
		TargetCoverage: 80, BufferFraction: 0.1, BaselinePercentile: 5, LookbackDays: 30,
		BufferUtilizationThreshold: 50, MaxActionsPerRun: 5, RampSchedule: []byte(`{"steps":[{"after_days":0,"fraction":1.0}]}`)})
	require.NoError(t, err)
	r.cfgID = saved.ID
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO purchase_plans (name) VALUES ('c2') RETURNING id`).Scan(&r.planID))
	return r
}

// provenFailedExecution inserts a failed row carrying the no_submission proof.
func (r *ladderRig) provenFailedExecution() string {
	var id string
	require.NoError(r.t, r.pool.QueryRow(r.ctx, `
		INSERT INTO purchase_executions (plan_id, scheduled_date, status, no_submission, created_at)
		VALUES ($1, NOW(), 'failed', true, NOW()) RETURNING execution_id`,
		r.planID).Scan(&id))
	return id
}

func (r *ladderRig) link(from, to string) {
	_, err := r.pool.Exec(r.ctx, `UPDATE purchase_executions SET retry_execution_id = $2 WHERE execution_id = $1`, from, to)
	require.NoError(r.t, err)
}

// firedTranche claims a real scheduled tranche with a real execution row.
func (r *ladderRig) firedTranche(headStatus string, headProof bool, age time.Duration) (trancheID, headExec string) {
	return r.firedTrancheInRun("", headStatus, headProof, age)
}

// firedTrancheInRun is firedTranche on an existing run; an empty runID makes a new one.
func (r *ladderRig) firedTrancheInRun(runID, headStatus string, headProof bool, age time.Duration) (trancheID, headExec string) {
	basis, rate := config.LadderAmountCommitment, "1" // only commitment-dollar rows are claimable
	tr := config.LadderTrancheDB{ID: uuid.New().String(), ConfigID: &r.cfgID, LayerType: ladder.LayerComputeSP,
		Term: ladder.Term1Year, PaymentOption: ladder.PaymentNoUpfront, Status: ladder.TrancheStatusScheduled,
		AmountUSDHr: 5, ScheduledDate: time.Now().Add(-time.Hour), AmountBasis: &basis, CommitmentRate: &rate}
	if runID == "" {
		run, err := r.store.SaveLadderRun(r.ctx, &config.LadderRunDB{ConfigID: &r.cfgID, StartedAt: tr.ScheduledDate, Status: ladder.RunStatusPlanned, Mode: ptr(string(ladder.ModeAutoApprove))})
		require.NoError(r.t, err)
		runID = run.ID
	}
	tr.RunID = &runID
	require.NoError(r.t, r.store.SaveLadderTranches(r.ctx, []config.LadderTrancheDB{tr}))
	claimed, err := r.store.ClaimDueLadderTranches(r.ctx, r.cfgID, time.Now(), 10,
		config.LadderClaimFilter{Layers: []ladder.LayerType{ladder.LayerComputeSP}, TrancheIDs: []string{tr.ID}, CeilingMicros: 1_000_000_000_000},
		func(ctx context.Context, tx pgx.Tx, _ config.LadderTrancheDB) (string, error) {
			var id string
			scanErr := tx.QueryRow(ctx, `INSERT INTO purchase_executions (plan_id, scheduled_date, status, no_submission, created_at)
				VALUES ($1, NOW(), $2, $3, NOW() - $4::interval) RETURNING execution_id`, r.planID, headStatus, headProof, age.String()).Scan(&id)
			return id, scanErr
		})
	require.NoError(r.t, err)
	require.Len(r.t, claimed, 1)
	return tr.ID, *claimed[0].ExecutionID
}

func (r *ladderRig) reconcile(visible bool) LadderReconcileResult {
	res, err := ReconcileLadderTranches(r.ctx, r.store, stubVisibility{visible}, time.Now())
	require.NoError(r.t, err)
	return res
}

func (r *ladderRig) state(trancheID string) (status string, reason *string) {
	require.NoError(r.t, r.pool.QueryRow(r.ctx, `SELECT status, status_reason FROM ladder_tranches WHERE id = $1`, trancheID).Scan(&status, &reason))
	return
}

func (r *ladderRig) inFlight() float64 {
	var v float64
	abort := errors.New("read only")
	_, err := r.store.PlanAndSaveLadderRun(r.ctx, r.cfgID, time.Now(), func(rows []config.LadderInFlightRow) (*config.LadderRunDB, []config.LadderTrancheDB, error) {
		var err error
		v, err = config.LadderInFlightOnDemand(rows)
		require.NoError(r.t, err)
		return nil, nil, abort
	})
	require.ErrorIs(r.t, err, abort)
	return v
}

func TestReconcileLadderTranches_Mappings(t *testing.T) {
	type tc struct {
		name       string
		head       string
		wantStatus string
		wantReason string
		headProof  bool
		visible    bool
	}
	cases := []tc{
		{name: "failed with proof", head: "failed", headProof: true, visible: true, wantStatus: "failed", wantReason: "no_submission"},
		{name: "failed without proof (reaper / outcome unknown) keeps netting", head: "failed", headProof: false, visible: true, wantStatus: "fired", wantReason: ""},
		{name: "proof flag on a running row is ignored", head: "running", headProof: true, visible: true, wantStatus: "fired", wantReason: ""},
		{name: "canceled approval", head: "canceled", headProof: false, visible: true, wantStatus: "failed", wantReason: "approval_canceled"},
		{name: "expired approval", head: "expired", headProof: false, visible: true, wantStatus: "failed", wantReason: "approval_expired"},
		{name: "partially completed stays fired", head: "partially_completed", headProof: false, visible: true, wantStatus: "fired", wantReason: ""},
		{name: "completed and visible", head: "completed", headProof: false, visible: true, wantStatus: "completed", wantReason: "commitment_visible"},
		{name: "completed, commitment not visible yet", head: "completed", headProof: false, visible: false, wantStatus: "fired", wantReason: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newLadderRig(t)
			id, _ := r.firedTranche(c.head, c.headProof, time.Minute)
			require.InDelta(t, 5, r.inFlight(), 1e-9)
			r.reconcile(c.visible)
			status, reason := r.state(id)
			assert.Equal(t, c.wantStatus, status)
			if c.wantReason == "" {
				assert.Nil(t, reason)
			} else {
				require.NotNil(t, reason)
				assert.Equal(t, c.wantReason, *reason)
			}
			if c.wantStatus == "fired" {
				assert.InDelta(t, 5, r.inFlight(), 1e-9, "an unresolved tranche must keep netting")
			}
			if c.wantStatus == "failed" {
				assert.InDelta(t, 0, r.inFlight(), 1e-9, "a proven-failed tranche stops netting")
			}
			r.reconcile(c.visible) // idempotent
			again, _ := r.state(id)
			assert.Equal(t, c.wantStatus, again)
		})
	}
}

// First attempt was reaped (failed, no proof), then a retry failed before
// submission. The first attempt may have bought, so the tranche stays fired.
func TestReconcileLadderTranches_RetryChainNeedsProofOnEveryNode(t *testing.T) {
	r := newLadderRig(t)
	id, e1 := r.firedTranche("failed", false, time.Minute)
	e2 := r.provenFailedExecution()
	e3 := r.provenFailedExecution()
	r.link(e1, e2)
	r.link(e2, e3)
	r.reconcile(true)
	status, _ := r.state(id)
	assert.Equal(t, "fired", status)

	// With proof on the first node too, the whole chain is proven.
	_, err := r.pool.Exec(r.ctx, `UPDATE purchase_executions SET no_submission = true WHERE execution_id = $1`, e1)
	require.NoError(t, err)
	r.reconcile(true)
	status, reason := r.state(id)
	assert.Equal(t, "failed", status)
	require.NotNil(t, reason)
	assert.Equal(t, "no_submission", *reason)
}

func TestReconcileLadderTranches_CycleAndDepthCapStayFired(t *testing.T) {
	r := newLadderRig(t)
	cyc, a := r.firedTranche("failed", true, time.Minute)
	b := r.provenFailedExecution()
	r.link(a, b)
	r.link(b, a)

	long, head := r.firedTranche("failed", true, time.Minute)
	prev := head
	for i := 0; i < config.LadderChainMaxDepth+1; i++ { // one past the cap, every node proven
		next := r.provenFailedExecution()
		r.link(prev, next)
		prev = next
	}
	r.reconcile(true)
	for name, id := range map[string]string{"cycle": cyc, "too long": long} {
		status, _ := r.state(id)
		assert.Equal(t, "fired", status, name)
	}
}

func TestReconcileLadderTranches_StaleUnresolvedIsAlertedNotChanged(t *testing.T) {
	r := newLadderRig(t)
	old, _ := r.firedTranche("failed", false, LadderFiredStaleAfter+time.Hour)
	fresh, _ := r.firedTranche("failed", false, time.Minute)
	res := r.reconcile(true)
	assert.Equal(t, 2, res.Unresolved)
	assert.Equal(t, 1, res.Stale)
	for _, id := range []string{old, fresh} {
		status, _ := r.state(id)
		assert.Equal(t, "fired", status)
	}
}

func TestLockLadderTrancheChainTx_ReadsScheduledExecutionAt(t *testing.T) {
	r := newLadderRig(t)
	id, exec := r.firedTranche("scheduled", false, time.Minute)
	due := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Millisecond)
	_, err := r.pool.Exec(r.ctx, `UPDATE purchase_executions SET scheduled_execution_at = $2 WHERE execution_id = $1`, exec, due)
	require.NoError(t, err)

	var chain []config.LadderChainNode
	require.NoError(t, r.store.WithTx(r.ctx, func(tx pgx.Tx) error {
		var e error
		chain, e = r.store.LockLadderTrancheChainTx(r.ctx, tx, id)
		return e
	}))

	require.Len(t, chain, 1)
	require.NotNil(t, chain[0].ScheduledExecutionAt)
	assert.WithinDuration(t, due, *chain[0].ScheduledExecutionAt, time.Millisecond)
}
