//go:build integration

package config

// Real PostgreSQL tests for the claim's sweep ceiling (issue #70, slice
// S8c4). Amounts are USD per hour of commitment; ceilings are micro-dollars.
// Nothing here uses mocks.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const usd = int64(1_000_000)

type sweepCeilingFixture struct {
	ctx    context.Context
	store  *PostgresStore
	cfgID  string
	planID string
	asOf   time.Time
	calls  atomic.Int64
	create func(context.Context, pgx.Tx, LadderTrancheDB) (string, error)
}

func newSweepCeilingFixture(t *testing.T) *sweepCeilingFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	store := setupLadderStore(ctx, t)
	f := &sweepCeilingFixture{ctx: ctx, store: store, asOf: time.Now().UTC().Truncate(time.Microsecond)}
	f.cfgID = seedLadderConfigWithExtID(ctx, t, store, "70"+uuid.NewString()[:10])
	f.create = newExecCreator(ctx, t, store, &f.calls)
	require.NoError(t, store.db.QueryRow(ctx, `SELECT id FROM purchase_plans LIMIT 1`).Scan(&f.planID))
	return f
}

// due saves one claimable compute-sp row per amount, in date order, due
// before asOf.
func (f *sweepCeilingFixture) due(t *testing.T, amounts ...float64) []LadderTrancheDB {
	t.Helper()
	rows := make([]LadderTrancheDB, len(amounts))
	for i, a := range amounts {
		rows[i] = claimableTranche(f.cfgID, a, f.asOf.Add(-time.Duration(len(amounts)-i)*time.Hour))
	}
	saveClaimable(f.ctx, t, f.store, rows)
	return rows
}

// history saves a tranche of cfgID in the given status whose execution was
// created age before asOf.
func (f *sweepCeilingFixture) history(t *testing.T, cfgID string, amount float64, status ladder.TrancheStatus, age time.Duration) {
	t.Helper()
	tr := lifecycleTranche(cfgID, amount, status, f.asOf.Add(-48*time.Hour))
	require.NoError(t, f.store.SaveLadderTranches(f.ctx, []LadderTrancheDB{tr}))
	var execID string
	require.NoError(t, f.store.db.QueryRow(f.ctx,
		`INSERT INTO purchase_executions (plan_id, scheduled_date, created_at) VALUES ($1, NOW(), $2) RETURNING execution_id`,
		f.planID, f.asOf.Add(-age)).Scan(&execID))
	_, err := f.store.db.Exec(f.ctx, `UPDATE ladder_tranches SET execution_id = $2 WHERE id = $1`, tr.ID, execID)
	require.NoError(t, err)
}

func (f *sweepCeilingFixture) claim(ceiling int64, asOf time.Time, rows []LadderTrancheDB) ([]LadderTrancheDB, error) {
	filter := claimFilter(rows...)
	filter.CeilingMicros = ceiling
	return f.store.ClaimDueLadderTranches(f.ctx, f.cfgID, asOf, 10, filter, f.create)
}

func (f *sweepCeilingFixture) otherConfig(t *testing.T) string {
	t.Helper()
	return seedLadderConfigWithExtID(f.ctx, t, f.store, "70"+uuid.NewString()[:10])
}

func TestClaimCeiling_ClaimsTheLongestPrefixThatFits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ceiling int64
		want    int
	}{
		{"ceiling 5 fits the first two", 5 * usd, 2},
		{"exactly the sum", 9 * usd, 3},
		{"one micro short of the sum", 9*usd - 1, 2},
		{"below the head row", 2*usd - 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSweepCeilingFixture(t)
			rows := f.due(t, 2, 3, 4)
			got, err := f.claim(tc.ceiling, f.asOf, rows)
			require.NoError(t, err)
			assert.Equal(t, claimedIDs(rows[:tc.want]), claimedIDs(got))
			assert.Equal(t, int64(tc.want), f.calls.Load(), "no execution for a trimmed row")
			f.requireTrimmedUntouched(t, rows[tc.want:])
		})
	}
}

func (f *sweepCeilingFixture) requireTrimmedUntouched(t *testing.T, rows []LadderTrancheDB) {
	t.Helper()
	for _, r := range rows {
		var status string
		var execID *string
		require.NoError(t, f.store.db.QueryRow(f.ctx,
			`SELECT status, execution_id::text FROM ladder_tranches WHERE id = $1`, r.ID).Scan(&status, &execID))
		assert.Equal(t, string(ladder.TrancheStatusScheduled), status, r.ID)
		assert.Nil(t, execID, r.ID)
	}
}

// The window is a policy value: a change must be a deliberate edit of it.
func TestClaimCeiling_WindowPin(t *testing.T) {
	assert.Equal(t, 24*time.Hour, LadderCeilingWindow)
}

func TestClaimCeiling_InFlightWindowIsExact(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status ladder.TrancheStatus
		age    time.Duration
		want   int
	}{
		{"fired 1h ago counts", ladder.TrancheStatusFired, time.Hour, 2},
		{"completed counts", ladder.TrancheStatusCompleted, time.Hour, 2},
		{"failed counts", ladder.TrancheStatusFailed, time.Hour, 2},
		{"24h minus a microsecond counts", ladder.TrancheStatusFired, 24*time.Hour - time.Microsecond, 2},
		{"exactly 24h has aged out", ladder.TrancheStatusFired, 24 * time.Hour, 3},
		{"older has aged out", ladder.TrancheStatusFired, 25 * time.Hour, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSweepCeilingFixture(t)
			f.history(t, f.cfgID, 4, tc.status, tc.age)
			rows := f.due(t, 2, 3, 4)
			got, err := f.claim(9*usd, f.asOf, rows)
			require.NoError(t, err)
			assert.Len(t, got, tc.want)
		})
	}
}

func TestClaimCeiling_AnotherConfigsHistoryDoesNotCount(t *testing.T) {
	f := newSweepCeilingFixture(t)
	f.history(t, f.otherConfig(t), 4, ladder.TrancheStatusFired, time.Hour)
	rows := f.due(t, 2, 3, 4)
	got, err := f.claim(9*usd, f.asOf, rows)
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

// The first claim's own executions count, so repeated sweeps cannot exceed
// the ceiling.
func TestClaimCeiling_RepeatedSweepsCannotExceedIt(t *testing.T) {
	f := newSweepCeilingFixture(t)
	rows := f.due(t, 2, 3, 4)
	first, err := f.claim(5*usd, f.asOf, rows)
	require.NoError(t, err)
	require.Len(t, first, 2)

	second, err := f.claim(5*usd, time.Now().UTC(), rows)
	require.NoError(t, err)
	assert.Empty(t, second)
	assert.Equal(t, int64(2), f.calls.Load())
}

// A refused row must not consume budget: the layer, id, and policy filters run
// before the trim.
func TestClaimCeiling_FilteredRowsNeverConsumeBudget(t *testing.T) {
	f := newSweepCeilingFixture(t)
	refused := claimableTranche(f.cfgID, 3, f.asOf.Add(-3*time.Hour))
	refused.Term = ladder.Term3Year
	unlisted := claimableTranche(f.cfgID, 4, f.asOf.Add(-2*time.Hour))
	good := claimableTranche(f.cfgID, 2, f.asOf.Add(-time.Hour))
	saveClaimable(f.ctx, t, f.store, []LadderTrancheDB{refused, unlisted, good})

	got, err := f.claim(2*usd, f.asOf, []LadderTrancheDB{refused, good})
	require.NoError(t, err)
	assert.Equal(t, []string{good.ID}, claimedIDs(got))
	f.requireTrimmedUntouched(t, []LadderTrancheDB{refused, unlisted})
}

func TestClaimCeiling_ExhaustedBudgetClaimsNothingWithoutError(t *testing.T) {
	for _, history := range []float64{5, 6} {
		f := newSweepCeilingFixture(t)
		f.history(t, f.cfgID, history, ladder.TrancheStatusFired, time.Hour)
		rows := f.due(t, 1)
		got, err := f.claim(5*usd, f.asOf, rows)
		require.NoError(t, err, history)
		assert.Empty(t, got, history)
		assert.Zero(t, f.calls.Load(), "the callback never runs")
		f.requireTrimmedUntouched(t, rows)
	}
}

func TestClaimCeiling_NonPositiveCeilingFailsBeforeLockingAnything(t *testing.T) {
	f := newSweepCeilingFixture(t)
	rows := f.due(t, 1)
	for _, ceiling := range []int64{0, -usd} {
		got, err := f.claim(ceiling, f.asOf, rows)
		require.ErrorContains(t, err, "ceiling", ceiling)
		assert.Nil(t, got)
	}
	assert.Zero(t, f.calls.Load())
	f.requireTrimmedUntouched(t, rows)
}
