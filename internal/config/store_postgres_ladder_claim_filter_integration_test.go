//go:build integration

package config

// Real PostgreSQL tests for the claim layer allowlist and resolved id set
// (issue #70, slice S5). Nothing here uses mocks.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type claimFilterFixture struct {
	ctx        context.Context
	store      *PostgresStore
	cfgID      string
	asOf       time.Time
	calls      atomic.Int64
	create     func(context.Context, pgx.Tx, LadderTrancheDB) (string, error)
	a, b, c, d LadderTrancheDB
	e, x       LadderTrancheDB
}

func claimTrancheAt(cfgID string, layer ladder.LayerType, amount float64, at time.Time) LadderTrancheDB {
	tr := claimableTranche(cfgID, amount, at)
	tr.LayerType = layer
	return tr
}

// newClaimFilterFixture seeds rows a..e on one config and x on another, with
// distinct power-of-two amounts so a netted sum identifies the rows counted.
// Rows are saved in reverse date order so heap order cannot mimic the ORDER BY.
func newClaimFilterFixture(t *testing.T) *claimFilterFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	store := setupLadderStore(ctx, t)
	f := &claimFilterFixture{ctx: ctx, store: store, asOf: time.Now().UTC().Truncate(time.Microsecond)}
	f.cfgID = seedLadderConfigWithExtID(ctx, t, store, "69"+uuid.NewString()[:10])
	otherID := seedLadderConfigWithExtID(ctx, t, store, "69"+uuid.NewString()[:10])
	f.create = newExecCreator(ctx, t, store, &f.calls)
	f.a = claimTrancheAt(f.cfgID, ladder.LayerComputeSP, 1, f.asOf.Add(-3*time.Hour))
	f.b = claimTrancheAt(f.cfgID, ladder.LayerConvertibleRI, 2, f.asOf.Add(-2*time.Hour))
	f.c = claimTrancheAt(f.cfgID, ladder.LayerEC2InstanceSP, 4, f.asOf.Add(-90*time.Minute))
	f.d = claimTrancheAt(f.cfgID, ladder.LayerComputeSP, 8, f.asOf.Add(-time.Hour))
	f.e = claimTrancheAt(f.cfgID, ladder.LayerComputeSP, 16, f.asOf.Add(time.Microsecond))
	f.x = claimTrancheAt(otherID, ladder.LayerComputeSP, 32, f.asOf.Add(-time.Hour))
	require.NoError(t, store.SaveLadderTranches(ctx, []LadderTrancheDB{f.x, f.e, f.d, f.c, f.b, f.a}))
	return f
}

func (f *claimFilterFixture) claim(limit int, layers []ladder.LayerType, ids ...LadderTrancheDB) ([]LadderTrancheDB, error) {
	filter := claimFilter(ids...)
	filter.Layers = layers
	return f.store.ClaimDueLadderTranches(f.ctx, f.cfgID, f.asOf, limit, filter, f.create)
}

func (f *claimFilterFixture) claimIDs(limit int, layers []ladder.LayerType, ids []string) ([]LadderTrancheDB, error) {
	return f.store.ClaimDueLadderTranches(f.ctx, f.cfgID, f.asOf, limit,
		LadderClaimFilter{Layers: layers, TrancheIDs: ids}, f.create)
}

func (f *claimFilterFixture) all() []LadderTrancheDB {
	return []LadderTrancheDB{f.a, f.b, f.c, f.d, f.e}
}

func claimedIDs(rows []LadderTrancheDB) []string {
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return ids
}

func (f *claimFilterFixture) requireUntouched(t *testing.T, rows ...LadderTrancheDB) {
	t.Helper()
	for _, r := range rows {
		var status string
		var execID, reason *string
		require.NoError(t, f.store.db.QueryRow(f.ctx,
			`SELECT status, execution_id::text, status_reason FROM ladder_tranches WHERE id = $1`, r.ID).Scan(&status, &execID, &reason))
		assert.Equal(t, string(ladder.TrancheStatusScheduled), status, r.ID)
		assert.Nil(t, execID, r.ID)
		assert.Nil(t, reason, r.ID)
	}
}

var (
	computeOnly = []ladder.LayerType{ladder.LayerComputeSP}
	ec2Only     = []ladder.LayerType{ladder.LayerEC2InstanceSP}
)

func TestClaimFilter_LayerNotInAllowlistIsNeverClaimed(t *testing.T) {
	f := newClaimFilterFixture(t)
	before := inFlight(f.ctx, t, f.store, f.cfgID, f.asOf)
	assert.Equal(t, 1.0+2+4+8+16, before)

	got, err := f.claim(10, computeOnly, f.all()...)
	require.NoError(t, err)
	assert.Equal(t, []string{f.a.ID, f.d.ID}, claimedIDs(got))
	assert.Equal(t, int64(2), f.calls.Load())
	f.requireUntouched(t, f.b, f.c, f.e, f.x)
	assert.Equal(t, before, inFlight(f.ctx, t, f.store, f.cfgID, f.asOf), "claiming moves scheduled -> fired without changing netting")
}

func TestClaimFilter_LimitCountsOnlyAllowlistedRows(t *testing.T) {
	f := newClaimFilterFixture(t)
	got, err := f.claim(1, ec2Only, f.all()...)
	require.NoError(t, err)
	assert.Equal(t, []string{f.c.ID}, claimedIDs(got), "older non-allowlisted rows must not use up the limit")
	got, err = f.claim(1, computeOnly, f.all()...)
	require.NoError(t, err)
	assert.Equal(t, []string{f.a.ID}, claimedIDs(got))
}

func TestClaimFilter_MultipleLayersKeepDateOrder(t *testing.T) {
	f := newClaimFilterFixture(t)
	// Two rows share a date and are saved higher id first, to pin the id tie-break.
	at := f.asOf.Add(-30 * time.Minute)
	t1, t2 := claimTrancheAt(f.cfgID, ladder.LayerComputeSP, 64, at), claimTrancheAt(f.cfgID, ladder.LayerComputeSP, 128, at)
	if t1.ID > t2.ID {
		t1, t2 = t2, t1
	}
	require.NoError(t, f.store.SaveLadderTranches(f.ctx, []LadderTrancheDB{t2, t1}))
	got, err := f.claim(10, []ladder.LayerType{ladder.LayerEC2InstanceSP, ladder.LayerComputeSP}, append(f.all(), t1, t2)...)
	require.NoError(t, err)
	assert.Equal(t, []string{f.a.ID, f.c.ID, f.d.ID, t1.ID, t2.ID}, claimedIDs(got))
	assert.Equal(t, int64(5), f.calls.Load())
	f.requireUntouched(t, f.b, f.e)
}

func TestClaimFilter_EmptyAndNilLayersClaimNothing(t *testing.T) {
	f := newClaimFilterFixture(t)
	for _, layers := range [][]ladder.LayerType{nil, {}} {
		got, err := f.claim(10, layers, f.all()...)
		require.Error(t, err)
		assert.Nil(t, got)
	}
	assert.Zero(t, f.calls.Load())
	f.requireUntouched(t, f.a, f.b, f.c, f.d)
	got, err := f.claim(10, computeOnly, f.all()...)
	require.NoError(t, err, "no lock may be left held by the rejected claims")
	assert.Len(t, got, 2)
}

func TestClaimFilter_IDSetIsAnIntersection(t *testing.T) {
	f := newClaimFilterFixture(t)
	got, err := f.claim(10, computeOnly, f.d)
	require.NoError(t, err)
	assert.Equal(t, []string{f.d.ID}, claimedIDs(got))
	f.requireUntouched(t, f.a)

	got, err = f.claim(10, computeOnly, f.a, f.d, f.e)
	require.NoError(t, err)
	assert.Equal(t, []string{f.a.ID}, claimedIDs(got), "d is already fired and e is not due")
}

func TestClaimFilter_NilAndEmptyIDsClaimNothing(t *testing.T) {
	f := newClaimFilterFixture(t)
	for _, ids := range [][]string{nil, {}} {
		got, err := f.claimIDs(10, computeOnly, ids)
		require.NoError(t, err)
		assert.Empty(t, got)
	}
	assert.Zero(t, f.calls.Load())
	f.requireUntouched(t, f.a, f.b, f.c, f.d)
}

func TestClaimFilter_IDOfAnotherConfigIsNeverClaimed(t *testing.T) {
	f := newClaimFilterFixture(t)
	got, err := f.claim(10, computeOnly, f.x, f.a)
	require.NoError(t, err)
	assert.Equal(t, []string{f.a.ID}, claimedIDs(got))
	f.requireUntouched(t, f.x)
}

func TestClaimFilter_MalformedIDFailsLoudAndClaimsNothing(t *testing.T) {
	f := newClaimFilterFixture(t)
	got, err := f.claimIDs(10, computeOnly, []string{"not-a-uuid"})
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr))
	assert.Equal(t, "22P02", pgErr.Code)
	assert.Nil(t, got)
	assert.Zero(t, f.calls.Load())
	f.requireUntouched(t, f.a)

	_, err = f.claimIDs(10, computeOnly, []string{f.a.ID, "not-a-uuid"})
	require.Error(t, err, "one bad id must not be skipped")
	f.requireUntouched(t, f.a)
}

func TestClaimFilter_DisjointAllowlistsRaceCleanly(t *testing.T) {
	f := newClaimFilterFixture(t)
	cases := []struct {
		layers []ladder.LayerType
		row    LadderTrancheDB
	}{{computeOnly, f.a}, {ec2Only, f.c}}
	start := make(chan struct{})
	got := make([][]LadderTrancheDB, len(cases))
	errs := make([]error, len(cases))
	var wg sync.WaitGroup
	for i := range cases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got[i], errs[i] = f.claim(10, cases[i].layers, cases[i].row)
		}()
	}
	close(start)
	wg.Wait()
	for i := range cases {
		require.NoError(t, errs[i])
		assert.Equal(t, []string{cases[i].row.ID}, claimedIDs(got[i]))
	}
	assert.Equal(t, int64(2), f.calls.Load())
}
