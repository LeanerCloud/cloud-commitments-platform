//go:build integration

package purchase

// Real PostgreSQL tests for the ladder claim sweep (RunLadderFire). Tranches,
// runs, executions and the claim are real rows. The notifier is a FIXTURE that
// is never called by the sweep in this slice.

import (
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fireRigOwnAccount = "1234" + "5678" + "9012" // the external id newLadderRig gives the config's account.

type fireRig struct {
	*ladderRig
	now   time.Time
	runID string
	ids   [3]string // T1 2.5, T2 3.0, T3 4.0 USD/hr, due 2h, 1h and 30m before now.
}

func newFireRig(t *testing.T) *fireRig {
	r := newLadderRig(t)
	f := &fireRig{ladderRig: r, now: time.Now().UTC().Truncate(time.Millisecond)}
	f.arm()
	run, err := r.store.SaveLadderRun(r.ctx, &config.LadderRunDB{ConfigID: &r.cfgID, StartedAt: f.now, Status: ladder.RunStatusPlanned, Mode: ptr(string(ladder.ModeAutoApprove))})
	require.NoError(t, err)
	f.runID = run.ID
	basis, rate := config.LadderAmountCommitment, "1/2"
	trs := make([]config.LadderTrancheDB, 0, 3)
	for i, c := range []struct {
		amount float64
		ago    time.Duration
	}{{2.5, 2 * time.Hour}, {3.0, time.Hour}, {4.0, 30 * time.Minute}} {
		f.ids[i] = uuid.New().String()
		trs = append(trs, config.LadderTrancheDB{ID: f.ids[i], ConfigID: &r.cfgID, RunID: &run.ID, LayerType: ladder.LayerComputeSP,
			Term: ladder.Term1Year, PaymentOption: ladder.PaymentNoUpfront, Status: ladder.TrancheStatusScheduled,
			AmountUSDHr: c.amount, ScheduledDate: f.now.Add(-c.ago), AmountBasis: &basis, CommitmentRate: &rate})
	}
	require.NoError(t, r.store.SaveLadderTranches(r.ctx, trs))
	return f
}

// arm puts the config, the run's mode and the global switches in the state a
// sweep may claim in; gate tests break one thing and call arm again.
func (f *fireRig) arm() {
	_, err := f.pool.Exec(f.ctx, `UPDATE ladder_configs SET mode = 'auto_approve', enabled = true, provider = 'aws',
		max_hourly_commit_per_run = 10 WHERE id = $1`, f.cfgID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.store.SetLadderSweepCeiling(f.ctx, f.cfgID, ptr(6.0)))
	f.global(nil)
}

func (f *fireRig) global(mut func(*config.GlobalConfig)) {
	g, err := f.store.GetGlobalConfig(f.ctx)
	require.NoError(f.t, err)
	to := "ops@example.com"
	g.LadderingEnabled, g.LadderExecutionEnabled, g.RequireDifferentApprover = true, true, false
	g.PurchaseDelayHours, g.NotificationEmail = 48, &to
	if mut != nil {
		mut(g)
	}
	require.NoError(f.t, f.store.SaveGlobalConfig(f.ctx, g))
}

func (f *fireRig) deps() LadderFireDeps {
	return LadderFireDeps{Store: f.store, Registry: NewLadderInstrumentRegistry(), Notifier: stubScheduledNotifier{},
		OwnAccountID: fireRigOwnAccount, Region: "us-east-1", DashboardURL: "https://dash.example.com",
		Now: func() time.Time { return f.now }}
}

func (f *fireRig) sweep(mut func(*LadderFireDeps)) (LadderFireResult, error) {
	d := f.deps()
	if mut != nil {
		mut(&d)
	}
	return RunLadderFire(f.ctx, d)
}

func (f *fireRig) executionCount() int {
	var n int
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM purchase_executions WHERE created_by_system AND plan_id IS NULL`).Scan(&n))
	return n
}

func (f *fireRig) status(i int) string {
	s, _ := f.state(f.ids[i])
	return s
}

func (f *fireRig) requireUntouched() {
	f.t.Helper()
	assert.Zero(f.t, f.executionCount())
	for i := range f.ids {
		assert.Equal(f.t, "scheduled", f.status(i))
	}
}

func TestLadderFire_FullSweepClaimsThePrefixUnderTheCeiling(t *testing.T) {
	buf := captureLadderLog(t)
	f := newFireRig(t)
	res, err := f.sweep(nil)
	require.NoError(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 3, Claimed: 2, Trimmed: 1, Announced: 2}, res)
	assert.Equal(t, []string{"fired", "fired", "scheduled"}, []string{f.status(0), f.status(1), f.status(2)})
	assert.Contains(t, buf.String(), "held back")

	rows, err := f.pool.Query(f.ctx, `SELECT status, plan_id IS NULL, scheduled_execution_at, idempotency_key, created_by_system,
		notification_sent IS NOT NULL, cloud_account_id IS NOT NULL, recommendations->0->>'term'
		FROM purchase_executions WHERE created_by_system ORDER BY idempotency_key`)
	require.NoError(t, err)
	defer rows.Close()
	want := map[string]bool{f.ids[0]: true, f.ids[1]: true}
	n := 0
	for rows.Next() {
		var status, key, term string
		var noPlan, system, announced, hasAcct bool
		var fireAt time.Time
		require.NoError(t, rows.Scan(&status, &noPlan, &fireAt, &key, &system, &announced, &hasAcct, &term))
		assert.True(t, want[key], key)
		assert.Equal(t, "scheduled", status)
		assert.True(t, noPlan && system && announced && hasAcct)
		assert.True(t, f.now.Add(48*time.Hour).Equal(fireAt), fireAt)
		assert.Equal(t, "1", term)
		n++
	}
	assert.Equal(t, 2, n)
}

func TestLadderFire_SecondSweepIsIdempotentAndWarnsAboutTheBudget(t *testing.T) {
	f := newFireRig(t)
	_, err := f.sweep(nil)
	require.NoError(t, err)
	buf := captureLadderLog(t)
	res, err := f.sweep(nil)
	require.NoError(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 1, Trimmed: 1}, res)
	assert.Equal(t, 2, f.executionCount())
	assert.Contains(t, buf.String(), "budget is used")
	assert.Equal(t, "scheduled", f.status(2))
}

func TestLadderFire_EveryRefusalCreatesNothing(t *testing.T) {
	f := newFireRig(t)
	sql := func(q string) func() {
		return func() { _, err := f.pool.Exec(f.ctx, q, f.cfgID); require.NoError(t, err) }
	}
	cases := []struct {
		name    string
		breakIt func()
		mut     func(*LadderFireDeps)
	}{
		{"laddering off", func() { f.global(func(g *config.GlobalConfig) { g.LadderingEnabled = false }) }, nil},
		{"execution off", func() { f.global(func(g *config.GlobalConfig) { g.LadderExecutionEnabled = false }) }, nil},
		{"4-eyes on", func() { f.global(func(g *config.GlobalConfig) { g.RequireDifferentApprover = true }) }, nil},
		{"delay 23", func() { f.global(func(g *config.GlobalConfig) { g.PurchaseDelayHours = 23 }) }, nil},
		{"no recipient", func() { f.global(func(g *config.GlobalConfig) { g.NotificationEmail = nil }) }, nil},
		{"no notifier", nil, func(d *LadderFireDeps) { d.Notifier = nil }},
		{"no dashboard", nil, func(d *LadderFireDeps) { d.DashboardURL = "" }},
		{"other account", nil, func(d *LadderFireDeps) { d.OwnAccountID = strings.Repeat("9", 12) }},
		{"config disabled", sql(`UPDATE ladder_configs SET enabled = false WHERE id = $1`), nil},
		{"email approval", sql(`UPDATE ladder_configs SET mode = 'email_approval' WHERE id = $1`), nil},
		{"azure", sql(`UPDATE ladder_configs SET provider = 'azure' WHERE id = $1`), nil},
		{"no cap", sql(`UPDATE ladder_configs SET max_hourly_commit_per_run = NULL WHERE id = $1`), nil},
		{"no ceiling", sql(`UPDATE ladder_configs SET max_hourly_commit_per_sweep = NULL WHERE id = $1`), nil},
		{"canceled run", func() { f.setRunStatus("canceled") }, nil},
		{"legacy canceled run", func() { f.setRunStatus("cancelled") }, nil}, //nolint:misspell // legacy stored spelling
		{"run planned for email approval", func() { f.setRunMode("email_approval") }, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parent := f.t
			f.t = t
			defer func() { f.t = parent }()
			f.arm()
			f.setRunStatus("planned")
			f.setRunMode("auto_approve")
			if c.breakIt != nil {
				c.breakIt()
			}
			res, err := f.sweep(c.mut)
			require.NoError(t, err)
			assert.Positive(t, res.Skipped+res.Unresolved, "something must have refused it")
			f.requireUntouched()
		})
	}
}

func (f *fireRig) setRunStatus(s string) {
	_, err := f.pool.Exec(f.ctx, `UPDATE ladder_runs SET status = $2 WHERE id = $1`, f.runID, s)
	require.NoError(f.t, err)
}

func (f *fireRig) setRunMode(m string) {
	_, err := f.pool.Exec(f.ctx, `UPDATE ladder_runs SET mode = $2 WHERE id = $1`, f.runID, m)
	require.NoError(f.t, err)
}

func TestLadderFire_CeilingTrimsAgainstTheRollingWindow(t *testing.T) {
	cases := []struct {
		name       string
		ceiling    float64
		fired      time.Duration // age of an already fired 4.0 tranche; 0 means none.
		wantClaims int
	}{
		{"below the head", 2.4, 0, 0},
		{"exactly the first two", 5.5, 0, 2},
		{"in-flight 1h ago leaves 2.0", 6.0, time.Hour, 0},
		{"in-flight 25h ago has aged out", 6.0, 25 * time.Hour, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFireRig(t)
			require.NoError(t, f.store.SetLadderSweepCeiling(f.ctx, f.cfgID, ptr(c.ceiling)))
			if c.fired > 0 {
				f.fireOlderTranche(4.0, c.fired)
			}
			res, err := f.sweep(nil)
			require.NoError(t, err)
			assert.Equal(t, c.wantClaims, res.Claimed)
		})
	}
}

// fireOlderTranche inserts a fired tranche whose execution was created age ago.
func (f *fireRig) fireOlderTranche(amount float64, age time.Duration) {
	var execID string
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `INSERT INTO purchase_executions (plan_id, scheduled_date, status, created_at)
		VALUES ($1, NOW(), 'completed', NOW() - $2::interval) RETURNING execution_id`, f.planID, age.String()).Scan(&execID))
	basis, rate := config.LadderAmountCommitment, "1/2"
	old := config.LadderTrancheDB{ID: uuid.New().String(), ConfigID: &f.cfgID, RunID: &f.runID, LayerType: ladder.LayerComputeSP,
		Term: ladder.Term1Year, PaymentOption: ladder.PaymentNoUpfront, Status: ladder.TrancheStatusScheduled,
		AmountUSDHr: amount, ScheduledDate: f.now.Add(-26 * time.Hour), AmountBasis: &basis, CommitmentRate: &rate}
	require.NoError(f.t, f.store.SaveLadderTranches(f.ctx, []config.LadderTrancheDB{old}))
	_, err := f.pool.Exec(f.ctx, `UPDATE ladder_tranches SET status = 'fired', execution_id = $2 WHERE id = $1`, old.ID, execID)
	require.NoError(f.t, err)
}

func TestLadderFire_SilentDrainGuardClaimsNothingAndSaysWhy(t *testing.T) {
	buf := captureLadderLog(t)
	f := newFireRig(t)
	res, err := f.sweep(func(d *LadderFireDeps) { d.Region = "" }) // every Build fails.
	require.NoError(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 3, Unresolved: 3}, res)
	f.requireUntouched()
	out := buf.String()
	assert.Contains(t, out, "ERROR")
	assert.Contains(t, out, "due=3 resolved=0")

	// Why the guard exists: a day later the planner's overdue sweep cancels them.
	_, err = f.store.PlanAndSaveLadderRun(f.ctx, f.cfgID, f.now.Add(25*time.Hour), func([]config.LadderInFlightRow) (*config.LadderRunDB, []config.LadderTrancheDB, error) {
		return &config.LadderRunDB{ConfigID: &f.cfgID, StartedAt: f.now.Add(25 * time.Hour), Status: ladder.RunStatusPlanned, Mode: ptr(string(ladder.ModeAutoApprove))}, nil, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "canceled", f.status(0))
}
