//go:build integration

package purchase

// Real PostgreSQL tests for D5 in the ladder reconciler: a tranche that ends
// without buying disables its config in the same transaction.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (r *ladderRig) setGlobal(mutate func(*config.GlobalConfig)) {
	_, err := r.store.UpdateGlobalConfigAtomic(r.ctx, func(g *config.GlobalConfig) error {
		mutate(g)
		return nil
	})
	require.NoError(r.t, err)
}

func (r *ladderRig) setSwitches(laddering, execution bool) {
	r.setGlobal(func(g *config.GlobalConfig) { g.LadderingEnabled, g.LadderExecutionEnabled = laddering, execution })
}

func (r *ladderRig) enabled() bool {
	var v bool
	require.NoError(r.t, r.pool.QueryRow(r.ctx, `SELECT enabled FROM ladder_configs WHERE id = $1`, r.cfgID).Scan(&v))
	return v
}

func (r *ladderRig) configUpdatedAt() time.Time {
	var v time.Time
	require.NoError(r.t, r.pool.QueryRow(r.ctx, `SELECT updated_at FROM ladder_configs WHERE id = $1`, r.cfgID).Scan(&v))
	return v
}

// otherConfig returns a rig over a second, enabled config in the same database.
func (r *ladderRig) otherConfig() *ladderRig {
	acct := uuid.New().String()
	_, err := r.pool.Exec(r.ctx, `INSERT INTO cloud_accounts (id, name, enabled, provider, external_id, aws_is_org_root, created_at, updated_at)
		VALUES ($1, 'd5-other', true, 'aws', '210987654321', false, now(), now())`, acct)
	require.NoError(r.t, err)
	saved, err := r.store.UpsertLadderConfig(r.ctx, &config.LadderConfigDB{
		CloudAccountID: acct, Provider: "aws", Enabled: true, Mode: "email_approval", Cadence: "daily",
		TargetCoverage: 80, BufferFraction: 0.1, BaselinePercentile: 5, LookbackDays: 30,
		BufferUtilizationThreshold: 50, MaxActionsPerRun: 5, RampSchedule: []byte(`{"steps":[{"after_days":0,"fraction":1.0}]}`)})
	require.NoError(r.t, err)
	other := *r
	other.cfgID = saved.ID
	return &other
}

// scheduledTranche saves a due, claimable 5 USD/hr tranche without claiming it.
func (r *ladderRig) scheduledTranche() string {
	basis, rate := config.LadderAmountCommitment, "1"
	tr := config.LadderTrancheDB{ID: uuid.New().String(), ConfigID: &r.cfgID, LayerType: ladder.LayerComputeSP,
		Term: ladder.Term1Year, PaymentOption: ladder.PaymentNoUpfront, Status: ladder.TrancheStatusScheduled,
		AmountUSDHr: 5, ScheduledDate: time.Now().Add(-time.Minute), AmountBasis: &basis, CommitmentRate: &rate}
	run, err := r.store.SaveLadderRun(r.ctx, &config.LadderRunDB{ConfigID: &r.cfgID, StartedAt: time.Now(), Status: ladder.RunStatusPlanned, Mode: ptr(string(ladder.ModeAutoApprove))})
	require.NoError(r.t, err)
	tr.RunID = &run.ID
	require.NoError(r.t, r.store.SaveLadderTranches(r.ctx, []config.LadderTrancheDB{tr}))
	return tr.ID
}

func (r *ladderRig) claim(ctx context.Context, trancheID string, ceilingMicros int64, hold func()) ([]config.LadderTrancheDB, error) {
	return r.store.ClaimDueLadderTranches(ctx, r.cfgID, time.Now(), 10,
		config.LadderClaimFilter{Layers: []ladder.LayerType{ladder.LayerComputeSP}, TrancheIDs: []string{trancheID}, CeilingMicros: ceilingMicros},
		func(ctx context.Context, tx pgx.Tx, _ config.LadderTrancheDB) (string, error) {
			if hold != nil {
				hold()
			}
			var id string
			scanErr := tx.QueryRow(ctx, `INSERT INTO purchase_executions (plan_id, scheduled_date, status, no_submission, created_at)
				VALUES ($1, NOW(), 'scheduled', false, NOW()) RETURNING execution_id`, r.planID).Scan(&id)
			return id, scanErr
		})
}

func (r *ladderRig) requireFailed(id, reason string) {
	status, got := r.state(id)
	assert.Equal(r.t, "failed", status)
	require.NotNil(r.t, got)
	assert.Equal(r.t, reason, *got)
}

func TestLadderD5_FailedVerdictsDisableTheConfig(t *testing.T) {
	cases := []struct {
		name                string
		head                string
		proof               bool
		laddering, executes bool
		wantReason          string
	}{
		{"canceled tail", "canceled", false, true, true, "approval_canceled"},
		{"expired tail", "expired", false, true, true, "approval_expired"},
		{"proven failure with every switch on", "failed", true, true, true, "no_submission"},
		{"a revoke is never exempt, switches off", "canceled", false, false, false, "approval_canceled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newLadderRig(t)
			r.setSwitches(c.laddering, c.executes)
			id, _ := r.firedTranche(c.head, c.proof, time.Minute)
			require.InDelta(t, 5, r.inFlight(), 1e-9)

			res := r.reconcile(true)

			r.requireFailed(id, c.wantReason)
			assert.False(t, r.enabled())
			assert.InDelta(t, 0, r.inFlight(), 1e-9)
			assert.Equal(t, LadderReconcileResult{Checked: 1, Failed: 1, ConfigsDisabled: 1}, res)
			again := r.reconcile(true)
			assert.Equal(t, LadderReconcileResult{}, again, "a second pass finds nothing fired")
			assert.False(t, r.enabled())
		})
	}
}

func TestLadderD5_SwitchCausedRefusalsKeepTheConfigEnabled(t *testing.T) {
	cases := map[string]func(r *ladderRig, trancheID string){
		"laddering off": func(r *ladderRig, _ string) { r.setSwitches(false, true) },
		"execution off": func(r *ladderRig, _ string) { r.setSwitches(true, false) },
		"4-eyes on": func(r *ladderRig, _ string) {
			r.setGlobal(func(g *config.GlobalConfig) { g.RequireDifferentApprover = true })
		},
		"RI with standard class": func(r *ladderRig, id string) {
			r.setGlobal(func(g *config.GlobalConfig) { g.OfferingClass = "standard" })
			_, err := r.pool.Exec(r.ctx, `UPDATE ladder_tranches SET layer_type = $2 WHERE id = $1`, id, string(ladder.LayerConvertibleRI))
			require.NoError(r.t, err)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			r := newLadderRig(t)
			r.setSwitches(true, true)
			id, _ := r.firedTranche("failed", true, time.Minute)
			setup(r, id)

			res := r.reconcile(true)

			r.requireFailed(id, "no_submission")
			assert.True(t, r.enabled())
			assert.Equal(t, LadderReconcileResult{Checked: 1, Failed: 1}, res)
		})
	}
}

func TestLadderD5_UnknownOutcomesAreUntouched(t *testing.T) {
	cases := []struct {
		name  string
		setup func(r *ladderRig) string
	}{
		{"failed without proof", func(r *ladderRig) string { id, _ := r.firedTranche("failed", false, time.Minute); return id }},
		{"partially completed", func(r *ladderRig) string {
			id, _ := r.firedTranche("partially_completed", false, time.Minute)
			return id
		}},
		{"scheduled tail", func(r *ladderRig) string { id, _ := r.firedTranche("scheduled", false, time.Minute); return id }},
		{"running", func(r *ladderRig) string { id, _ := r.firedTranche("running", true, time.Minute); return id }},
		{"retry chain with an unproven node", func(r *ladderRig) string {
			id, e1 := r.firedTranche("failed", false, time.Minute)
			r.link(e1, r.provenFailedExecution())
			return id
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newLadderRig(t)
			r.setSwitches(true, true)
			id := c.setup(r)
			before := r.configUpdatedAt()

			res := r.reconcile(true)

			status, _ := r.state(id)
			assert.Equal(t, "fired", status)
			assert.True(t, r.enabled())
			assert.True(t, before.Equal(r.configUpdatedAt()), "ladder_configs must not be written")
			assert.InDelta(t, 5, r.inFlight(), 1e-9)
			assert.Equal(t, 0, res.ConfigsDisabled)
		})
	}
}

func TestLadderD5_MixedSweepDisablesOnlyTheFailedTranchesConfig(t *testing.T) {
	r := newLadderRig(t)
	r.setSwitches(true, true)
	other := r.otherConfig()
	canceled, _ := r.firedTranche("canceled", false, time.Minute)
	unproven, _ := r.firedTranche("failed", false, time.Minute)
	running, _ := other.firedTranche("running", true, time.Minute)

	res := r.reconcile(true)

	r.requireFailed(canceled, "approval_canceled")
	status, _ := r.state(unproven)
	assert.Equal(t, "fired", status)
	status, _ = r.state(running)
	assert.Equal(t, "fired", status)
	assert.False(t, r.enabled())
	assert.True(t, other.enabled())
	assert.InDelta(t, 5, r.inFlight(), 1e-9, "the unproven tranche keeps netting")
	assert.Equal(t, LadderReconcileResult{Checked: 3, Failed: 1, Unresolved: 2, Stale: 0, ConfigsDisabled: 1}, res)
}

func TestLadderD5_AlreadyDisabledConfigIsNotCounted(t *testing.T) {
	r := newLadderRig(t)
	_, err := r.pool.Exec(r.ctx, `UPDATE ladder_configs SET enabled = false WHERE id = $1`, r.cfgID)
	require.NoError(t, err)
	id, _ := r.firedTranche("canceled", false, time.Minute)

	res := r.reconcile(true)

	r.requireFailed(id, "approval_canceled")
	assert.Equal(t, LadderReconcileResult{Checked: 1, Failed: 1}, res)
}

type failingDisableStore struct{ *config.PostgresStore }

func (failingDisableStore) DisableLadderConfigTx(context.Context, pgx.Tx, string) (bool, error) {
	return false, errors.New("disable failed")
}

func TestLadderD5_FailedDisableRollsTheTransitionBack(t *testing.T) {
	r := newLadderRig(t)
	id, _ := r.firedTranche("canceled", false, time.Minute)

	res, err := ReconcileLadderTranches(r.ctx, failingDisableStore{r.store}, stubVisibility{true}, time.Now())

	require.Error(t, err)
	assert.Equal(t, LadderReconcileResult{Checked: 1, Errored: 1}, res)
	status, _ := r.state(id)
	assert.Equal(t, "fired", status)
	assert.True(t, r.enabled())

	res = r.reconcile(true)
	r.requireFailed(id, "approval_canceled")
	assert.False(t, r.enabled())
	assert.Equal(t, 1, res.ConfigsDisabled)
}

func TestLadderD5_CompletedAndVisibleKeepsTheConfigEnabled(t *testing.T) {
	r := newLadderRig(t)
	r.setSwitches(true, true)
	id, _ := r.firedTranche("completed", false, time.Minute)
	before := r.configUpdatedAt()

	res := r.reconcile(true)

	status, reason := r.state(id)
	assert.Equal(t, "completed", status)
	require.NotNil(t, reason)
	assert.Equal(t, "commitment_visible", *reason)
	assert.True(t, r.enabled())
	assert.True(t, before.Equal(r.configUpdatedAt()))
	assert.Equal(t, LadderReconcileResult{Checked: 1, Completed: 1}, res)
}
