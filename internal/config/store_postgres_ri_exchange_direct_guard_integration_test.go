//go:build integration

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real-PostgreSQL coverage for #1829: an AWS direct execute that may still be
// committing (submitted) or whose outcome is unknown holds its source RIs, so
// no history reservation (AUTO or approval) takes them again.

// directRow inserts an audit row for the given provider and status, as the
// direct-execute handler would have left it.
func (e *riExchangeIntegrationEnv) directRow(t *testing.T, provider, status string, sources ...string) string {
	t.Helper()
	var id string
	require.NoError(t, e.poolA.QueryRow(e.ctx, `
		INSERT INTO ri_exchange_direct_executions
			(id, provider, source_ids, targets, max_payment_due, max_payment_currency, status, executed_by, completed_at)
		VALUES (gen_random_uuid(), $1, $2, '[{"offering_id":"o","count":1}]'::jsonb, 100, 'USD', $3, 'tester',
			CASE WHEN $3 = 'submitted' THEN NULL ELSE now() END)
		RETURNING id::text`, provider, sources, status).Scan(&id))
	return id
}

func (e *riExchangeIntegrationEnv) reserve(t *testing.T, account string, sources ...string) error {
	t.Helper()
	cap := e.capAbove(t, time.Now().UTC())
	r := guardCandidate(account, sources...)
	_, err := e.storeA.ReserveRIExchange(e.ctx, r, cap, cap)
	if err == nil {
		e.release(t, r.ID)
	}
	return err
}

// [fail-first] A direct row that is submitted or unknown refuses an AUTO
// reservation of its RI, names only the caller's RI, and never the row.
func TestPostgresStoreDB_ReserveRIExchange_RefusesInFlightDirectExecution(t *testing.T) {
	for _, status := range []string{"unknown", "submitted"} {
		t.Run(status, func(t *testing.T) {
			env := newRIExchangeIntegrationEnv(t)
			blockerID := env.directRow(t, "aws", status, "ri-g1829-a", "ri-g1829-shared")

			account := testAccountID(830000000001)
			err := env.reserve(t, account, "ri-g1829-shared", "ri-g1829-c")
			require.ErrorIs(t, err, ErrRIExchangeSourceInFlight)
			assert.Contains(t, err.Error(), "ri-g1829-shared")
			assert.Contains(t, err.Error(), "verify")
			assert.NotContains(t, err.Error(), "ri-g1829-a", "only the shared RI is named")
			assert.NotContains(t, err.Error(), blockerID, "the blocking row's id is never exposed (#1428)")
			assert.Zero(t, env.rowCount(t, account), "a refused reservation writes no row")
		})
	}
}

// [fail-first] An approval of a RI held by a direct row is refused too.
func TestPostgresStoreDB_ReserveApprovedRIExchange_RefusesInFlightDirectExecution(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	cap := env.capAbove(t, time.Now().UTC())
	env.directRow(t, "aws", "unknown", "ri-g1829-approve")
	blockerID := env.directRow(t, "aws", "submitted", "ri-g1829-other")
	row := env.processingWith(t, testAccountID(830000000011), "ri-g1829-approve")
	defer env.release(t, row.ID)

	_, err := env.storeA.ReserveApprovedRIExchange(env.ctx, row.ID, cap, cap)
	require.ErrorIs(t, err, ErrRIExchangeSourceInFlight)
	assert.Contains(t, err.Error(), "ri-g1829-approve")
	assert.NotContains(t, err.Error(), "ri-g1829-other", "only the shared RI is named")
	assert.NotContains(t, err.Error(), blockerID, "the blocking row's id is never exposed (#1428)")
}

// [fail-first] The approval path normalises too: upper case and padded ids
// match a stored lower-case id.
func TestPostgresStoreDB_ReserveApprovedRIExchange_DirectGuardNormalisesIDs(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	cap := env.capAbove(t, time.Now().UTC())
	env.directRow(t, "aws", "unknown", "ri-g1829-abc")
	for i, ri := range []string{"RI-G1829-ABC", " RI-G1829-ABC "} {
		row := env.processingWith(t, testAccountID(830000000051+int64(i)), ri)
		_, err := env.storeA.ReserveApprovedRIExchange(env.ctx, row.ID, cap, cap)
		require.ErrorIs(t, err, ErrRIExchangeSourceInFlight, "%q", ri)
		env.release(t, row.ID)
	}
}

// [guard] A held RI does not block a different one, on either path.
func TestPostgresStoreDB_DirectGuard_DisjointRIStillReserves(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	cap := env.capAbove(t, time.Now().UTC())
	env.directRow(t, "aws", "unknown", "ri-g1829-x")

	require.NoError(t, env.reserve(t, testAccountID(830000000061), "ri-g1829-y"))
	row := env.processingWith(t, testAccountID(830000000062), "ri-g1829-y")
	defer env.release(t, row.ID)
	_, err := env.storeA.ReserveApprovedRIExchange(env.ctx, row.ID, cap, cap)
	require.NoError(t, err)
}

// [guard] Settled rows and Azure rows do not hold an AWS RI.
func TestPostgresStoreDB_ReserveRIExchange_SettledOrAzureDirectRowsDoNotBlock(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	env.directRow(t, "aws", "completed", "ri-g1829-done")
	env.directRow(t, "aws", "failed", "ri-g1829-failed")
	env.directRow(t, "azure", "unknown", "ri-g1829-azure")
	env.directRow(t, "azure", "submitted", "ri-g1829-azure2")

	for i, ri := range []string{"ri-g1829-done", "ri-g1829-failed", "ri-g1829-azure", "ri-g1829-azure2"} {
		require.NoError(t, env.reserve(t, testAccountID(830000000020+int64(i)), ri), ri)
	}
}

// [fail-first] The guard normalises both sides: spelling and surrounding
// spaces cannot slip an RI past it.
func TestPostgresStoreDB_ReserveRIExchange_DirectGuardNormalisesIDs(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	env.directRow(t, "aws", "unknown", "ri-g1829-case")
	env.directRow(t, "aws", "unknown", " RI-G1829-SPACE ")

	err := env.reserve(t, testAccountID(830000000031), "RI-G1829-CASE")
	require.ErrorIs(t, err, ErrRIExchangeSourceInFlight, "stored lower case, reserved upper case")
	err = env.reserve(t, testAccountID(830000000032), "ri-g1829-space")
	require.ErrorIs(t, err, ErrRIExchangeSourceInFlight, "stored padded upper case, reserved lower case")
	err = env.reserve(t, testAccountID(830000000033), "  Ri-G1829-Case  ")
	require.ErrorIs(t, err, ErrRIExchangeSourceInFlight, "reserved padded mixed case")

	env.directRow(t, "aws", "unknown", "ri-g1829-tab\t")
	err = env.reserve(t, testAccountID(830000000034), "ri-g1829-tab")
	require.ErrorIs(t, err, ErrRIExchangeSourceInFlight, "stored with a trailing tab")

	// PostgreSQL E-strings have no \v escape: a trim set spelled with it would
	// strip the letter v, so an id that ends in v must still match itself.
	env.directRow(t, "aws", "unknown", "ri-g1829-endsv")
	err = env.reserve(t, testAccountID(830000000035), "ri-g1829-endsv")
	require.ErrorIs(t, err, ErrRIExchangeSourceInFlight, "an id ending in v must not be letter-stripped by the trim set")
}

// [fail-first] A lookup error refuses the reservation instead of letting it
// through.
func TestPostgresStoreDB_ReserveRIExchange_DirectGuardFailsClosedOnLookupError(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	// Break only the column the guard reads: the daily-spend query runs first
	// and reads other columns of the same table, so dropping it would fail there.
	_, err := env.poolA.Exec(env.ctx, "ALTER TABLE ri_exchange_direct_executions RENAME COLUMN source_ids TO source_ids_broken")
	require.NoError(t, err)

	account := testAccountID(830000000041)
	err = env.reserve(t, account, "ri-g1829-x")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRIExchangeSourceInFlight)
	assert.Contains(t, err.Error(), "failed to check RI exchange source RIs held by a direct execute", "the guard itself must refuse, not a later statement on the aborted transaction")
	assert.Zero(t, env.rowCount(t, account), "a failed lookup writes no row")
}
