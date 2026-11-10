//go:build integration

package config

// Real PostgreSQL tests for the claim policy filters (issue #70, slice S8c2):
// the fixed term and payment option, and the run-start window. Nothing here
// uses mocks.

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// policyFixture saves single tranches whose run starts at a chosen instant.
type policyFixture struct {
	*claimFilterFixture
	due    time.Time
	amount float64
}

func newPolicyFixture(t *testing.T) *policyFixture {
	t.Helper()
	f := newClaimFilterFixture(t)
	return &policyFixture{claimFilterFixture: f, due: f.asOf.Add(-time.Hour), amount: 1024}
}

// add saves one compute-sp tranche dated at, planned by a run started at
// runStart. A nil runStart leaves the tranche without a run. Amounts are
// distinct powers of two so a netted sum identifies the rows counted.
func (p *policyFixture) add(t *testing.T, term ladder.Term, pay ladder.PaymentOption, at time.Time, runStart *time.Time) LadderTrancheDB {
	t.Helper()
	tr := claimableTranche(p.cfgID, p.amount, at)
	p.amount *= 2
	tr.Term, tr.PaymentOption = term, pay
	if runStart != nil {
		run, err := p.store.SaveLadderRun(p.ctx, &LadderRunDB{ConfigID: &p.cfgID, StartedAt: *runStart, Status: ladder.RunStatusPlanned, Mode: autoApproveMode()})
		require.NoError(t, err)
		tr.RunID = &run.ID
	}
	require.NoError(t, p.store.SaveLadderTranches(p.ctx, []LadderTrancheDB{tr}))
	return tr
}

// good adds a policy-conforming tranche dated at the fixture's due instant.
func (p *policyFixture) good(t *testing.T, runStart time.Time) LadderTrancheDB {
	t.Helper()
	return p.add(t, LadderTerm, LadderPayment, p.due, &runStart)
}

func sortedIDs(rows ...LadderTrancheDB) []string {
	ids := claimedIDs(rows)
	sort.Strings(ids)
	return ids
}

func TestClaimPolicy_TermAndPaymentAllowlistIsExact(t *testing.T) {
	p := newPolicyFixture(t)
	d := p.due
	ok1 := p.good(t, d)
	t3 := p.add(t, ladder.Term3Year, ladder.PaymentNoUpfront, d, &d)
	ua := p.add(t, ladder.Term1Year, ladder.PaymentAllUpfront, d, &d)
	up := p.add(t, ladder.Term1Year, ladder.PaymentPartialUpfront, d, &d)
	t3ua := p.add(t, ladder.Term3Year, ladder.PaymentAllUpfront, d, &d)
	odd1 := p.add(t, ladder.Term("1y"), ladder.PaymentNoUpfront, d, &d)
	odd2 := p.add(t, ladder.Term1Year, ladder.PaymentOption("No-Upfront"), d, &d)

	got, err := p.claim(10, computeOnly, ok1, t3, ua, up, t3ua, odd1, odd2)
	require.NoError(t, err)
	assert.Equal(t, []string{ok1.ID}, claimedIDs(got))
	assert.Equal(t, int64(1), p.calls.Load())
	p.requireUntouched(t, t3, ua, up, t3ua, odd1, odd2)
}

func TestClaimPolicy_NullAndOrphanedRunIsRefused(t *testing.T) {
	p := newPolicyFixture(t)
	d := p.due
	ok1 := p.good(t, d)
	nullRun := p.add(t, LadderTerm, LadderPayment, d, nil)
	orphan := p.good(t, d)
	_, err := p.store.db.Exec(p.ctx, `DELETE FROM ladder_runs WHERE id = $1`, *orphan.RunID)
	require.NoError(t, err)

	got, err := p.claim(10, computeOnly, ok1, nullRun, orphan)
	require.NoError(t, err)
	assert.Equal(t, []string{ok1.ID}, claimedIDs(got))
	p.requireUntouched(t, nullRun, orphan)
}

func TestClaimPolicy_RunStartWindowBoundaryIsInclusiveToTheMicrosecond(t *testing.T) {
	p := newPolicyFixture(t)
	d := p.due
	ok1 := p.good(t, d)
	in24 := p.good(t, d.Add(-24*time.Hour))
	in24m := p.good(t, d.Add(-24*time.Hour+time.Microsecond))
	out24 := p.good(t, d.Add(-24*time.Hour-time.Microsecond))
	later := p.good(t, d.Add(time.Microsecond))

	got, err := p.claim(10, computeOnly, ok1, in24, in24m, out24, later)
	require.NoError(t, err)
	assert.Equal(t, sortedIDs(ok1, in24, in24m, later), claimedIDs(got), "all share one date, so the id breaks the tie")
	p.requireUntouched(t, out24)
}

func TestClaimPolicy_RampStepBeyondADayIsRefusedAndTheGoodOneStillClaims(t *testing.T) {
	p := newPolicyFixture(t)
	d := p.due
	ok1 := p.good(t, d)
	far := p.good(t, d.Add(-72*time.Hour))

	got, err := p.claim(10, computeOnly, far, ok1)
	require.NoError(t, err)
	assert.Equal(t, []string{ok1.ID}, claimedIDs(got))
	assert.Equal(t, int64(1), p.calls.Load(), "refusal happens in SQL, not by failing the callback")
	p.requireUntouched(t, far)
}

func TestClaimPolicy_RefusedRowsNeverConsumeTheLimit(t *testing.T) {
	p := newPolicyFixture(t)
	a := p.asOf
	at := func(d time.Duration) time.Time { return a.Add(d) }
	out24x := p.add(t, LadderTerm, LadderPayment, at(-3*time.Hour), ptr(at(-27*time.Hour-time.Microsecond)))
	farx := p.add(t, LadderTerm, LadderPayment, at(-2*time.Hour), ptr(at(-74*time.Hour)))
	t3x := p.add(t, ladder.Term3Year, LadderPayment, at(-150*time.Minute), ptr(at(-150*time.Minute)))
	g1 := p.add(t, LadderTerm, LadderPayment, at(-90*time.Minute), ptr(at(-90*time.Minute)))
	g2 := p.add(t, LadderTerm, LadderPayment, at(-time.Hour), ptr(at(-time.Hour)))
	all := []LadderTrancheDB{out24x, farx, t3x, g1, g2}

	got, err := p.claim(1, computeOnly, all...)
	require.NoError(t, err)
	assert.Equal(t, []string{g1.ID}, claimedIDs(got))
	got, err = p.claim(1, computeOnly, all...)
	require.NoError(t, err)
	assert.Equal(t, []string{g2.ID}, claimedIDs(got))
	p.requireUntouched(t, out24x, farx, t3x)
}

func ptr(t time.Time) *time.Time { return &t }

func TestClaimPolicy_DeterministicOrderAcrossRuns(t *testing.T) {
	p := newPolicyFixture(t)
	a := p.asOf
	mk := func(d time.Duration) LadderTrancheDB {
		return p.add(t, LadderTerm, LadderPayment, a.Add(d), ptr(a.Add(d)))
	}
	// The two sharing a date are saved higher id first, to pin the id tie-break.
	r1, r2, r3 := mk(-time.Hour), mk(-2*time.Hour), mk(-3*time.Hour)
	lo, hi := mk(-30*time.Minute), mk(-30*time.Minute)
	if lo.ID > hi.ID {
		lo, hi = hi, lo
	}
	want := []string{r3.ID, r2.ID, r1.ID, lo.ID, hi.ID}
	all := []LadderTrancheDB{hi, lo, r1, r2, r3}

	order := make([]string, 0, len(want))
	for _, wantPage := range [][]string{want[:2], want[2:4], want[4:]} {
		got, err := p.claim(2, computeOnly, all...)
		require.NoError(t, err)
		assert.Equal(t, wantPage, claimedIDs(got))
		order = append(order, claimedIDs(got)...)
	}
	assert.Equal(t, want, order)
}

func TestClaimPolicy_RunRowsAreNeverLockedOrSkipped(t *testing.T) {
	for _, mode := range []string{"FOR UPDATE", "FOR NO KEY UPDATE"} {
		t.Run(mode, func(t *testing.T) {
			p := newPolicyFixture(t)
			tr := p.good(t, p.due)
			holder, err := p.store.db.Begin(p.ctx)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, holder.Rollback(context.Background())) })
			_, err = holder.Exec(p.ctx, `SELECT 1 FROM ladder_runs WHERE id = $1 `+mode, *tr.RunID)
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
			defer cancel()
			got, err := p.store.ClaimDueLadderTranches(ctx, p.cfgID, p.asOf, 10, claimFilter(tr), p.create)
			require.NoError(t, err)
			assert.Equal(t, []string{tr.ID}, claimedIDs(got), "a held run row must neither block nor skip the tranche")

			probe, err := p.store.db.Begin(p.ctx)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, probe.Rollback(context.Background())) })
			_, err = probe.Exec(p.ctx, `SELECT 1 FROM ladder_runs WHERE id = $1 FOR UPDATE NOWAIT`, *tr.RunID)
			var pgErr *pgconn.PgError
			require.ErrorAs(t, err, &pgErr)
			assert.Equal(t, "55P03", pgErr.Code, "the claim must not have released the holder's lock")
		})
	}
}

func TestClaimPolicy_RefusedRowsStayNettedUntilTheOverdueSweep(t *testing.T) {
	p := newPolicyFixture(t)
	d := p.due
	base := inFlight(p.ctx, t, p.store, p.cfgID, p.asOf)
	ok1 := p.good(t, d)
	refused := []LadderTrancheDB{
		p.add(t, ladder.Term3Year, LadderPayment, d, &d),
		p.add(t, LadderTerm, ladder.PaymentAllUpfront, d, &d),
		p.add(t, LadderTerm, LadderPayment, d, nil),
	}
	sum := ok1.AmountUSDHr
	for i := range refused {
		sum += refused[i].AmountUSDHr
	}

	got, err := p.claim(10, computeOnly, append(refused, ok1)...)
	require.NoError(t, err)
	assert.Equal(t, []string{ok1.ID}, claimedIDs(got))
	assert.Equal(t, base+sum, inFlight(p.ctx, t, p.store, p.cfgID, p.asOf), "refused rows keep netting")

	sweepRun := &LadderRunDB{ConfigID: &p.cfgID, StartedAt: d.Add(LadderFireGrace + time.Hour), Status: ladder.RunStatusPlanned}
	_, err = p.store.SaveLadderRunWithTranches(p.ctx, sweepRun, nil)
	require.NoError(t, err)
	for _, r := range refused {
		var status string
		var reason *string
		require.NoError(t, p.store.db.QueryRow(p.ctx,
			`SELECT status, status_reason FROM ladder_tranches WHERE id = $1`, r.ID).Scan(&status, &reason))
		assert.Equal(t, string(ladder.TrancheStatusCanceled), status, r.ID)
		require.NotNil(t, reason, r.ID)
		assert.Equal(t, "expired", *reason, r.ID)
	}
	assert.Equal(t, ladder.TrancheStatusFired, trancheStatus(p.ctx, t, p.store, ok1.ID))
}
