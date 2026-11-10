//go:build integration

package config

// Real PostgreSQL tests for ListDueLadderTranches and the claim predicate it
// shares with ClaimDueLadderTranches (issue #70, slice S8c3). Nothing here uses
// mocks.

import (
	"context"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func modePtr(m ladder.LadderMode) *string {
	s := string(m)
	return &s
}

func autoApproveMode() *string { return modePtr(ladder.ModeAutoApprove) }

type dueFixture struct {
	ctx    context.Context
	store  *PostgresStore
	cfgID  string
	asOf   time.Time
	calls  atomic.Int64
	create func(context.Context, pgx.Tx, LadderTrancheDB) (string, error)
}

func newDueFixture(t *testing.T) *dueFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	f := &dueFixture{ctx: ctx, store: setupLadderStore(ctx, t), asOf: time.Now().UTC().Truncate(time.Microsecond)}
	f.cfgID = seedLadderConfigWithExtID(ctx, t, f.store, "69"+uuid.NewString()[:10])
	f.create = newExecCreator(ctx, t, f.store, &f.calls)
	return f
}

// run saves a run of the config started at start and returns its id. A status
// the typed API refuses to write (the legacy spelling) is forced by SQL.
func (f *dueFixture) run(t *testing.T, start time.Time, status string, mode *string) *string {
	t.Helper()
	run, err := f.store.SaveLadderRun(f.ctx, &LadderRunDB{ConfigID: &f.cfgID, StartedAt: start, Status: ladder.RunStatusPlanned, Mode: mode})
	require.NoError(t, err)
	_, err = f.store.db.Exec(f.ctx, `UPDATE ladder_runs SET status = $2 WHERE id = $1`, run.ID, status)
	require.NoError(t, err)
	return &run.ID
}

// add saves one compute-sp commitment tranche dated at, planned by runID, after
// mutate changed it.
func (f *dueFixture) add(t *testing.T, amount float64, at time.Time, runID *string, mutate func(*LadderTrancheDB)) LadderTrancheDB {
	t.Helper()
	tr := claimableTranche(f.cfgID, amount, at)
	tr.RunID = runID
	if mutate != nil {
		mutate(&tr)
	}
	require.NoError(t, f.store.SaveLadderTranches(f.ctx, []LadderTrancheDB{tr}))
	return tr
}

func (f *dueFixture) linked(t *testing.T, at time.Time) LadderTrancheDB {
	t.Helper()
	tr := f.good(t, at)
	var execID string
	require.NoError(t, f.store.db.QueryRow(f.ctx, `INSERT INTO purchase_executions (scheduled_date) VALUES (NOW()) RETURNING execution_id`).Scan(&execID))
	_, err := f.store.db.Exec(f.ctx, `UPDATE ladder_tranches SET execution_id = $2 WHERE id = $1`, tr.ID, execID)
	require.NoError(t, err)
	return tr
}

func (f *dueFixture) good(t *testing.T, at time.Time) LadderTrancheDB {
	t.Helper()
	return f.add(t, 1, at, f.run(t, at, string(ladder.RunStatusPlanned), autoApproveMode()), nil)
}

func (f *dueFixture) list(t *testing.T) []LadderDueRow {
	t.Helper()
	rows, err := f.store.ListDueLadderTranches(f.ctx, f.cfgID, f.asOf, 100, []ladder.LayerType{ladder.LayerComputeSP})
	require.NoError(t, err)
	return rows
}

func (f *dueFixture) claimAll(t *testing.T, rows ...LadderTrancheDB) []LadderTrancheDB {
	t.Helper()
	claimed, err := f.store.ClaimDueLadderTranches(f.ctx, f.cfgID, f.asOf, 100, claimFilter(rows...), f.create)
	require.NoError(t, err)
	return claimed
}

func claimableIDs(rows []LadderDueRow) []string {
	ids := []string{}
	for _, r := range rows {
		if r.SQLClaimable {
			ids = append(ids, r.Tranche.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func TestListDueLadderTranches_SQLClaimableEqualsWhatTheClaimTakes(t *testing.T) {
	f := newDueFixture(t)
	due := f.asOf.Add(-time.Hour)
	planned := string(ladder.RunStatusPlanned)
	okRun := func() *string { return f.run(t, due, planned, autoApproveMode()) }
	basis := LadderAmountOnDemand

	good1 := f.add(t, 1, due, okRun(), nil)
	good2 := f.add(t, 2, due.Add(time.Minute), okRun(), nil)
	others := []LadderTrancheDB{
		f.add(t, 3, due, okRun(), func(tr *LadderTrancheDB) { tr.LayerType = ladder.LayerConvertibleRI }),
		f.add(t, 4, due, okRun(), func(tr *LadderTrancheDB) { tr.Term = ladder.Term3Year }),
		f.add(t, 5, due, okRun(), func(tr *LadderTrancheDB) { tr.PaymentOption = ladder.PaymentAllUpfront }),
		f.add(t, 6, due, okRun(), func(tr *LadderTrancheDB) { tr.AmountBasis, tr.CommitmentRate = &basis, nil }),
		f.add(t, 7, due, nil, nil),
		f.add(t, 8, due, f.run(t, due.Add(-LadderFireGrace-time.Microsecond), planned, autoApproveMode()), nil),
		f.add(t, 9, due, f.run(t, due, string(ladder.RunStatusCanceled), autoApproveMode()), nil),
		f.add(t, 10, due, f.run(t, due, "cancelled", autoApproveMode()), nil), //nolint:misspell // legacy stored ladder status value
		f.add(t, 11, due, f.run(t, due, planned, modePtr(ladder.ModeEmailApproval)), nil),
	}
	notListed := []LadderTrancheDB{
		f.linked(t, due),
		f.add(t, 12, f.asOf.Add(time.Hour), okRun(), nil),
		f.add(t, 13, f.asOf.Add(-LadderFireGrace), okRun(), nil),
		f.add(t, 14, due, okRun(), func(tr *LadderTrancheDB) { tr.Status = ladder.TrancheStatusFired }),
	}

	rows := f.list(t)
	assert.Len(t, rows, 2+len(others), "not yet due, past grace, fired and execution-linked rows are not listed")
	want := []string{good1.ID, good2.ID}
	sort.Strings(want)
	assert.Equal(t, want, claimableIDs(rows), "golden: of 11 due rows, 2 are claimable")

	all := append([]LadderTrancheDB{good1, good2}, others...)
	all = append(all, notListed...)
	claimed := f.claimAll(t, all...)
	got := claimedIDs(claimed)
	sort.Strings(got)
	assert.Equal(t, want, got, "the list's claimable set is exactly the claim's set")
	assert.Equal(t, []string{good1.ID, good2.ID}, claimedIDs(claimed), "claim keeps date order")
}

func TestListDueLadderTranches_OrderedByDateThenID(t *testing.T) {
	f := newDueFixture(t)
	tie := f.asOf.Add(-time.Hour)
	tied := make([]string, 0, 8)
	for range 8 {
		tied = append(tied, f.good(t, tie).ID)
	}
	sort.Strings(tied)
	early := f.good(t, f.asOf.Add(-2*time.Hour))
	rows := f.list(t)
	require.Len(t, rows, 9)
	got := make([]string, len(rows))
	for i := range rows {
		got[i] = rows[i].Tranche.ID
	}
	assert.Equal(t, append([]string{early.ID}, tied...), got)
}

func TestListDueLadderTranches_HonoursLimitAndAllowlist(t *testing.T) {
	f := newDueFixture(t)
	f.good(t, f.asOf.Add(-2*time.Hour))
	f.good(t, f.asOf.Add(-time.Hour))
	rows, err := f.store.ListDueLadderTranches(f.ctx, f.cfgID, f.asOf, 1, []ladder.LayerType{ladder.LayerComputeSP})
	require.NoError(t, err)
	assert.Len(t, rows, 1)
	rows, err = f.store.ListDueLadderTranches(f.ctx, f.cfgID, f.asOf, 10, []ladder.LayerType{ladder.LayerConvertibleRI})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Empty(t, claimableIDs(rows), "a layer outside the allowlist is listed but not claimable")
}

func TestClaimDueLadderTranches_CanceledRunIsNotClaimed(t *testing.T) {
	for _, status := range []string{string(ladder.RunStatusCanceled), "cancelled"} { //nolint:misspell // legacy stored ladder status value
		t.Run(status, func(t *testing.T) {
			f := newDueFixture(t)
			due := f.asOf.Add(-time.Hour)
			canceled := f.add(t, 1, due, f.run(t, due, status, autoApproveMode()), nil)
			assert.Empty(t, f.claimAll(t, canceled))
			assert.Zero(t, f.calls.Load())
			assert.Equal(t, ladder.TrancheStatusScheduled, trancheStatus(f.ctx, t, f.store, canceled.ID))
		})
	}
}

func TestClaimDueLadderTranches_EmailApprovalRunIsNotClaimed(t *testing.T) {
	f := newDueFixture(t)
	due := f.asOf.Add(-time.Hour)
	emailRun := f.add(t, 1, due, f.run(t, due, string(ladder.RunStatusPlanned), modePtr(ladder.ModeEmailApproval)), nil)
	nullMode := f.add(t, 2, due, f.run(t, due, string(ladder.RunStatusPlanned), nil), nil)
	assert.Empty(t, f.claimAll(t, emailRun, nullMode))
	assert.Zero(t, f.calls.Load())
}

func TestListDueLadderTranches_ConfigModeFlipDoesNotMakeAnEmailApprovalRunClaimable(t *testing.T) {
	f := newDueFixture(t)
	due := f.asOf.Add(-time.Hour)
	_, err := f.store.db.Exec(f.ctx, `UPDATE ladder_configs SET mode = $2 WHERE id = $1`, f.cfgID, string(ladder.ModeEmailApproval))
	require.NoError(t, err)
	emailRun := f.add(t, 1, due, f.run(t, due, string(ladder.RunStatusPlanned), modePtr(ladder.ModeEmailApproval)), nil)
	_, err = f.store.db.Exec(f.ctx, `UPDATE ladder_configs SET mode = $2 WHERE id = $1`, f.cfgID, string(ladder.ModeAutoApprove))
	require.NoError(t, err)
	autoRun := f.good(t, due.Add(time.Minute))

	rows := f.list(t)
	require.Len(t, rows, 2)
	assert.Equal(t, []string{autoRun.ID}, claimableIDs(rows))
	assert.Equal(t, []string{autoRun.ID}, claimedIDs(f.claimAll(t, emailRun, autoRun)))
}

func TestListDueLadderTranches_CorruptRowIsReturnedNotAnError(t *testing.T) {
	f := newDueFixture(t)
	good1 := f.good(t, f.asOf.Add(-3*time.Hour))
	bad := f.good(t, f.asOf.Add(-2*time.Hour))
	good2 := f.good(t, f.asOf.Add(-time.Hour))
	_, err := f.store.db.Exec(f.ctx, `UPDATE ladder_tranches SET commitment_rate = 'not-a-rate' WHERE id = $1`, bad.ID)
	require.NoError(t, err)

	rows := f.list(t)
	require.Len(t, rows, 3)
	assert.Equal(t, []string{good1.ID, bad.ID, good2.ID}, []string{rows[0].Tranche.ID, rows[1].Tranche.ID, rows[2].Tranche.ID})
	_, rateErr := trancheCommitmentRate(&rows[1].Tranche)
	require.Error(t, rateErr, "the caller can see the row is corrupt")
}
