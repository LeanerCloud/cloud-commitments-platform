//go:build integration

package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real-PostgreSQL coverage for the #1836 store resolve. Fixtures set times
// relative to the database clock; the store must compare on that clock.

// directAged inserts an AWS row aged on the DB clock: created_at is now()-age
// and, for a non-submitted row, completed_at is now()-completedAgo.
func (e *riExchangeIntegrationEnv) directAged(t *testing.T, status, age, completedAgo string) string {
	t.Helper()
	id := e.directRow(t, "aws", status, "ri-g1836-"+status+"-"+age)
	_, err := e.poolA.Exec(e.ctx, `
		UPDATE ri_exchange_direct_executions
		SET created_at = now() - ($2::text)::interval,
		    completed_at = CASE WHEN status = 'submitted' THEN NULL ELSE now() - ($3::text)::interval END
		WHERE id = $1`, id, age, completedAgo)
	require.NoError(t, err)
	return id
}

type directTimes struct{ created, completed time.Time }

func (e *riExchangeIntegrationEnv) directTimes(t *testing.T, id string) directTimes {
	t.Helper()
	var d directTimes
	var completed *time.Time
	require.NoError(t, e.poolA.QueryRow(e.ctx,
		`SELECT created_at, completed_at FROM ri_exchange_direct_executions WHERE id = $1`, id).Scan(&d.created, &completed))
	if completed != nil {
		d.completed = *completed
	}
	return d
}

func directResolveReq(id, outcome, expected string, checkedAt time.Time) RIExchangeDirectResolve {
	return RIExchangeDirectResolve{ID: id, Outcome: outcome, ExpectedStatus: expected, Note: "operator:" + outcome + ": checked",
		CheckedAt: checkedAt, ResolvedBy: "ops@example.com"}
}

func (e *riExchangeIntegrationEnv) resolve(t *testing.T, req RIExchangeDirectResolve) (*RIExchangeDirectResolveResult, error) {
	t.Helper()
	return e.storeA.ResolveRIExchangeDirectExecution(e.ctx, req)
}

// [fail-first] A submitted row resolves only after the settle age, measured on
// the DB clock; the original status and completed_at stay, the resolution is
// recorded, and the guard stops blocking.
func TestPostgresStoreDB_ResolveRIExchangeDirectExecution_SubmittedAgeBoundary(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	young := env.directAged(t, "submitted", "9 minutes 59 seconds", "0")
	old := env.directAged(t, "submitted", "10 minutes 1 second", "0")

	_, err := env.resolve(t, directResolveReq(young, "failed", "submitted", time.Now().UTC()))
	require.ErrorIs(t, err, ErrRIExchangeResolveConflict)
	assert.Contains(t, err.Error(), "wait until")
	require.ErrorIs(t, env.reserve(t, testAccountID(836100000001), "ri-g1836-submitted-9 minutes 59 seconds"), ErrRIExchangeSourceInFlight)

	times := env.directTimes(t, old)
	uid := "7d444840-9dc0-11d1-b245-5ffdce74fad2"
	req := directResolveReq(old, "failed", "submitted", times.created.Add(RIExchangeDirectResolveSubmittedAfter))
	req.ResolvedByUserID = &uid
	res, err := env.resolve(t, req)
	require.NoError(t, err)
	assert.Equal(t, "submitted", res.Status, "the original status is returned unchanged")
	assert.Equal(t, "failed", res.ResolvedStatus)
	assert.False(t, res.Replayed)

	var status, rs, by, note, userID string
	var completedNull bool
	require.NoError(t, env.poolA.QueryRow(env.ctx, `
		SELECT status, resolved_status, resolved_by, resolution_note, resolved_by_user_id::text, completed_at IS NULL
		FROM ri_exchange_direct_executions WHERE id = $1`, old).Scan(&status, &rs, &by, &note, &userID, &completedNull))
	assert.Equal(t, []any{"submitted", "failed", "ops@example.com", req.Note, uid, true}, []any{status, rs, by, note, userID, completedNull})
	require.NoError(t, env.reserve(t, testAccountID(836100000002), "ri-g1836-submitted-10 minutes 1 second"), "resolved row releases its RI")
}

// [fail-first] Rows that must not resolve: Azure and unknown ids are not found,
// finalized rows and a wrong expected status conflict, bad input is invalid.
func TestPostgresStoreDB_ResolveRIExchangeDirectExecution_Refusals(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	now := time.Now().UTC()
	az := env.insertDirect(t, directSpec{provider: "azure"})
	_, err := env.resolve(t, directResolveReq(az, "failed", "unknown", now))
	require.ErrorIs(t, err, ErrRIExchangeResolveNotFound)
	_, err = env.resolve(t, directResolveReq("00000000-0000-0000-0000-000000000000", "failed", "unknown", now))
	require.ErrorIs(t, err, ErrRIExchangeResolveNotFound)

	for _, status := range []string{"completed", "failed"} {
		id := env.directAged(t, status, "2 hours", "1 hour")
		_, err = env.resolve(t, directResolveReq(id, "failed", "unknown", now))
		require.ErrorIs(t, err, ErrRIExchangeResolveConflict, status)
		assert.Contains(t, err.Error(), status)
	}

	unk := env.directAged(t, "unknown", "2 hours", "1 hour")
	_, err = env.resolve(t, directResolveReq(unk, "failed", "submitted", now))
	require.ErrorIs(t, err, ErrRIExchangeResolveConflict, "expected_status mismatch")

	bad := []RIExchangeDirectResolve{
		directResolveReq(unk, "unknown", "unknown", now),
		directResolveReq(unk, "failed", "completed", now),
		directResolveReq(unk, "failed", "unknown", time.Time{}),
	}
	noNote := directResolveReq(unk, "failed", "unknown", now)
	noNote.Note = ""
	longNote := directResolveReq(unk, "failed", "unknown", now)
	longNote.Note = strings.Repeat("x", RIExchangeDirectResolveNoteMax+1)
	noBy := directResolveReq(unk, "failed", "unknown", now)
	noBy.ResolvedBy = ""
	for i, req := range append(bad, noNote, longNote, noBy) {
		_, err = env.resolve(t, req)
		require.ErrorIs(t, err, ErrRIExchangeResolveInvalid, "case %d", i)
	}
}
