//go:build integration

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real-PostgreSQL coverage for #1050: a source RI that another exchange still
// holds in processing (its outcome may be unknown at AWS) is not reserved again.

func (e *riExchangeIntegrationEnv) processingWith(t *testing.T, account string, sources ...string) *RIExchangeRecord {
	t.Helper()
	r := e.processing(t, "manual", account, 10, time.Now().UTC())
	_, err := e.poolA.Exec(e.ctx, "UPDATE ri_exchange_history SET source_ri_ids = $2 WHERE id = $1", r.ID, sources)
	require.NoError(t, err)
	r.SourceRIIDs = sources
	return r
}

func guardCandidate(account string, sources ...string) *RIExchangeRecord {
	return &RIExchangeRecord{AccountID: account, Region: "us-east-1", SourceRIIDs: sources,
		SourceInstanceType: "m5.large", SourceCount: 1, TargetOfferingID: "offering-test", TargetInstanceType: "m6i.large",
		TargetCount: 1, PaymentDue: riExchangeUSD(usdMicros(10)), Status: "processing", Mode: "auto"}
}

func (e *riExchangeIntegrationEnv) rowCount(t *testing.T, account string) int {
	t.Helper()
	var n int
	require.NoError(t, e.poolA.QueryRow(e.ctx, "SELECT count(*) FROM ri_exchange_history WHERE account_id = $1", account).Scan(&n))
	return n
}

// [fail-first] An AUTO reservation whose source RIs overlap a processing row is
// refused, writes nothing, and names only the shared RI, never the other row.
func TestPostgresStoreDB_ReserveRIExchange_RefusesInFlightSourceRI(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	cap := env.capAbove(t, time.Now().UTC())
	blocker := env.processingWith(t, testAccountID(820000000001), "ri-g1050-a", "ri-g1050-shared")
	defer env.release(t, blocker.ID)

	account := testAccountID(820000000002)
	_, err := env.storeA.ReserveRIExchange(env.ctx, guardCandidate(account, "ri-g1050-shared", "ri-g1050-c"), cap, cap)
	require.ErrorIs(t, err, ErrRIExchangeSourceInFlight)
	assert.Contains(t, err.Error(), "ri-g1050-shared")
	assert.NotContains(t, err.Error(), "ri-g1050-a", "only the shared RI is named")
	assert.NotContains(t, err.Error(), blocker.ID, "the blocking exchange's id is never exposed (#1428)")
	assert.Zero(t, env.rowCount(t, account), "a refused reservation writes no row")
}

// [guard] Disjoint source RIs, and RIs held only by settled rows, still reserve.
func TestPostgresStoreDB_ReserveRIExchange_SettledOrDisjointSourcesStillReserve(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	cap := env.capAbove(t, time.Now().UTC())
	live := env.processingWith(t, testAccountID(820000000011), "ri-g1050-live")
	failed := env.processingWith(t, testAccountID(820000000012), "ri-g1050-failed")
	require.NoError(t, env.storeA.FailRIExchange(env.ctx, failed.ID, "test"))
	done := env.processingWith(t, testAccountID(820000000013), "ri-g1050-done")
	require.NoError(t, env.storeA.CompleteRIExchange(env.ctx, done.ID, "exchange-test"))
	defer env.release(t, live.ID)

	for i, sources := range [][]string{{"ri-g1050-other"}, {"ri-g1050-failed"}, {"ri-g1050-done"}} {
		r := guardCandidate(testAccountID(820000000020+int64(i)), sources...)
		_, err := env.storeA.ReserveRIExchange(env.ctx, r, cap, cap)
		require.NoError(t, err, "sources %v", sources)
		env.release(t, r.ID)
	}
}

// [guard] An approval's own processing row never blocks its reservation.
// [fail-first] A second approval of a source RI that another processing row
// holds is refused.
func TestPostgresStoreDB_ReserveApprovedRIExchange_SourceGuard(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	// A small per-exchange cap keeps the first reservation from taking the whole
	// daily headroom, so the second is refused by the guard, not by the cap.
	cap, perExchange := env.capAbove(t, time.Now().UTC()), riExchangeUSD(usdMicros(20))
	first := env.processingWith(t, testAccountID(820000000031), "ri-g1050-approve")
	_, err := env.storeA.ReserveApprovedRIExchange(env.ctx, first.ID, cap, perExchange)
	require.NoError(t, err, "the row's own source RIs do not block it")

	second := env.processingWith(t, testAccountID(820000000032), "ri-g1050-approve")
	defer env.release(t, first.ID, second.ID)
	_, err = env.storeA.ReserveApprovedRIExchange(env.ctx, second.ID, cap, perExchange)
	require.ErrorIs(t, err, ErrRIExchangeSourceInFlight)
	assert.Contains(t, err.Error(), "ri-g1050-approve")
	assert.NotContains(t, err.Error(), first.ID)
}
