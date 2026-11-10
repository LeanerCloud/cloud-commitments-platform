//go:build integration

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Real-PostgreSQL pins for the guards around the direct-exchange Finalize and
// resolve that the pgxmock SQL-string tests and the +1h future case cannot
// catch if loosened (#1869). Schema 163 (the resolution columns) is present
// here, so Finalize takes the checked path.

func directFinalizeOutcome() RIExchangeDirectOutcome {
	return RIExchangeDirectOutcome{Status: RIExchangeDirectStatusCompleted, ProviderReference: "riex-1",
		NetPayable: strPtr("40.25"), NetPayableCurrency: "USD"}
}

// [fail-first] Finalize's AND status = 'submitted' refuses a finalize of a row
// that already left 'submitted' (completed, failed, unknown) and rewrites
// nothing. A second finalize of the same row is pinned by
// TestPostgresStoreDB_RIExchangeDirectExecution.
func TestPostgresStoreDB_FinalizeRIExchangeDirectExecution_StatusCAS(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)

	for _, status := range []string{RIExchangeDirectStatusCompleted, RIExchangeDirectStatusFailed, RIExchangeDirectStatusUnknown} {
		t.Run("already "+status, func(t *testing.T) {
			id := env.directRow(t, "aws", status, "ri-g1869-"+status)
			err := env.storeA.FinalizeRIExchangeDirectExecution(env.ctx, id, directFinalizeOutcome())
			require.ErrorIs(t, err, ErrRIExchangeDirectExecutionNotSubmitted)

			var got string
			var ref *string
			require.NoError(t, env.poolA.QueryRow(env.ctx,
				`SELECT status, provider_reference FROM ri_exchange_direct_executions WHERE id = $1`, id).Scan(&got, &ref))
			require.Equal(t, status, got, "a refused finalize must not change the status")
			require.Nil(t, ref, "a refused finalize must not write the provider outcome")
		})
	}
}

// [fail-first] A checked_at a few seconds or a minute past the database clock is
// refused, so a future tolerance of 5s or more fails here (the existing case only
// reaches now+1h). The exact edge is pinned by the checkRIExchangeDirectResolve
// unit test, which a live clock cannot probe without flaking.
func TestPostgresStoreDB_ResolveRIExchangeDirectExecution_CheckedAtFutureBound(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	id := env.directAged(t, "unknown", "2 hours", "1 hour")

	var dbNow time.Time
	require.NoError(t, env.poolA.QueryRow(env.ctx, `SELECT clock_timestamp()`).Scan(&dbNow))
	for _, ahead := range []time.Duration{5 * time.Second, time.Minute} {
		_, err := env.resolve(t, directResolveReq(id, "failed", "unknown", dbNow.Add(ahead)))
		require.ErrorIs(t, err, ErrRIExchangeResolveInvalid, "%s past the database clock", ahead)
	}
	_, err := env.resolve(t, directResolveReq(id, "failed", "unknown", dbNow.Add(-time.Second)))
	require.NoError(t, err, "a second behind the database clock is accepted")
}
