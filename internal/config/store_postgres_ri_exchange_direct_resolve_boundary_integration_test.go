//go:build integration

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real-PostgreSQL boundary coverage for the #1836 resolve: the checked_at
// bounds and the replay variants.

// [fail-first] checked_at bounds: exactly the bound is accepted and one
// microsecond less is refused, for both statuses and both outcomes; a time in
// the future and one in another zone are judged as instants.
func TestPostgresStoreDB_ResolveRIExchangeDirectExecution_CheckedAtBounds(t *testing.T) {
	for _, outcome := range []string{"completed", "failed"} {
		t.Run("submitted_"+outcome, func(t *testing.T) {
			env := newRIExchangeIntegrationEnv(t)
			id := env.directAged(t, "submitted", "1 hour", "0")
			bound := env.directTimes(t, id).created.Add(RIExchangeDirectResolveSubmittedAfter)

			_, err := env.resolve(t, directResolveReq(id, outcome, "submitted", bound.Add(-time.Microsecond)))
			require.ErrorIs(t, err, ErrRIExchangeResolveInvalid)
			_, err = env.resolve(t, directResolveReq(id, outcome, "submitted", time.Now().UTC().Add(time.Hour)))
			require.ErrorIs(t, err, ErrRIExchangeResolveInvalid, "future")
			_, err = env.resolve(t, directResolveReq(id, outcome, "submitted", bound.Add(900*time.Nanosecond)))
			require.NoError(t, err, "a nanosecond tail past the bound truncates to the bound and is accepted")
		})
		t.Run("unknown_"+outcome, func(t *testing.T) {
			env := newRIExchangeIntegrationEnv(t)
			id := env.directAged(t, "unknown", "2 hours", "1 hour")
			bound := env.directTimes(t, id).completed.Add(RIExchangeDirectResolveUnknownCheckAfter)

			_, err := env.resolve(t, directResolveReq(id, outcome, "unknown", bound.Add(-time.Microsecond)))
			require.ErrorIs(t, err, ErrRIExchangeResolveInvalid)
			zone := time.FixedZone("UTC+14", 14*3600)
			_, err = env.resolve(t, directResolveReq(id, outcome, "unknown", bound.In(zone)))
			require.NoError(t, err, "exactly the bound, given in another zone")
		})
	}
}

// [fail-first] An unknown row has no age rule, only the check bound: 59s after
// completion is refused, 61s accepted.
func TestPostgresStoreDB_ResolveRIExchangeDirectExecution_UnknownBound(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	a := env.directAged(t, "unknown", "2 minutes", "2 minutes")
	completed := env.directTimes(t, a).completed

	_, err := env.resolve(t, directResolveReq(a, "failed", "unknown", completed.Add(59*time.Second)))
	require.ErrorIs(t, err, ErrRIExchangeResolveInvalid)
	_, err = env.resolve(t, directResolveReq(a, "failed", "unknown", completed.Add(61*time.Second)))
	require.NoError(t, err)
}

// [fail-first] A repeat of the same request replays and writes nothing; a
// different outcome or note conflicts.
func TestPostgresStoreDB_ResolveRIExchangeDirectExecution_Replay(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	id := env.directAged(t, "unknown", "2 hours", "1 hour")
	req := directResolveReq(id, "failed", "unknown", time.Now().UTC().Add(-time.Minute))
	first, err := env.resolve(t, req)
	require.NoError(t, err)

	again, err := env.resolve(t, req)
	require.NoError(t, err)
	assert.True(t, again.Replayed)
	assert.Equal(t, "unknown", again.Status)
	assert.True(t, first.ResolvedAt.Equal(again.ResolvedAt), "resolved_at is not rewritten")

	other := req
	other.Outcome = "completed"
	other.Note = "operator:completed: checked"
	_, err = env.resolve(t, other)
	require.ErrorIs(t, err, ErrRIExchangeResolveConflict)
	diffNote := req
	diffNote.Note = "a different note"
	_, err = env.resolve(t, diffNote)
	require.ErrorIs(t, err, ErrRIExchangeResolveConflict)
}
