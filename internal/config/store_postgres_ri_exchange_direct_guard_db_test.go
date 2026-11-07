//go:build integration

package config

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real-PostgreSQL coverage for #1829: the direct Save runs under the shared
// advisory lock and refuses an AWS source RI that another exchange holds.

func awsDirectSave(sources ...string) *RIExchangeDirectExecution {
	rec := newDirectExec("aws")
	rec.SourceIDs = sources
	return rec
}

func (e *riExchangeIntegrationEnv) directRows(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, e.poolA.QueryRow(e.ctx, "SELECT count(*) FROM ri_exchange_direct_executions").Scan(&n))
	return n
}

func TestRIExchangeDirectSave_GuardRefusesHeldRIs(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	history := env.processingWith(t, testAccountID(840000000001), "ri-g1829-hist")
	defer env.release(t, history.ID)

	err := env.storeA.SaveRIExchangeDirectExecution(env.ctx, awsDirectSave("ri-g1829-hist", "ri-g1829-free"))
	require.ErrorIs(t, err, ErrRIExchangeSourceInFlight)
	assert.Zero(t, env.directRows(t), "a refused save inserts no audit row")

	held := awsDirectSave("ri-g1829-held")
	require.NoError(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, held), "a submitted row")
	require.ErrorIs(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, awsDirectSave("ri-g1829-held")), ErrRIExchangeSourceInFlight)
	require.NoError(t, env.storeA.FinalizeRIExchangeDirectExecution(env.ctx, held.ID, RIExchangeDirectOutcome{Status: RIExchangeDirectStatusUnknown, Error: "lost"}))
	require.ErrorIs(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, awsDirectSave("ri-g1829-held")), ErrRIExchangeSourceInFlight, "an unknown row")
}

func TestRIExchangeDirectSave_GuardLeavesOthersAlone(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	history := env.processingWith(t, testAccountID(840000000011), "ri-g1829-hist")
	defer env.release(t, history.ID)
	env.directRow(t, "aws", "unknown", "ri-g1829-aws")

	azure := newDirectExec("azure")
	azure.SourceIDs = []string{"ri-g1829-hist", "ri-g1829-aws"}
	require.NoError(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, azure), "Azure is not guarded")
	require.NoError(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, awsDirectSave("ri-g1829-disjoint")))

	env.directRow(t, "aws", "completed", "ri-g1829-done")
	require.NoError(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, awsDirectSave("ri-g1829-done")), "a settled row holds nothing")
}

func TestRIExchangeDirectSave_GuardNormalisesIDs(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	history := env.processingWith(t, testAccountID(840000000021), "ri-g1829-abc")
	defer env.release(t, history.ID)
	for _, ri := range []string{"RI-G1829-ABC", " RI-G1829-ABC "} {
		require.ErrorIs(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, awsDirectSave(ri)), ErrRIExchangeSourceInFlight, "history row, save %q", ri)
	}

	env.directRow(t, "aws", "unknown", " RI-G1829-PAD ")
	env.directRow(t, "aws", "unknown", "ri-g1829-low")
	require.ErrorIs(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, awsDirectSave("ri-g1829-pad")), ErrRIExchangeSourceInFlight, "stored padded upper case")
	require.ErrorIs(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, awsDirectSave("RI-G1829-LOW")), ErrRIExchangeSourceInFlight, "stored lower case")
}

func TestRIExchangeDirectSave_LockSerialisesAndRunsAtReadCommitted(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	holder, err := env.poolA.Begin(env.ctx)
	require.NoError(t, err)
	var iso string
	require.NoError(t, holder.QueryRow(env.ctx, "SHOW transaction_isolation").Scan(&iso))
	assert.Equal(t, "read committed", iso, "the lock design assumes each statement sees the latest commits")
	_, err = holder.Exec(env.ctx, "SELECT pg_advisory_xact_lock($1)", riExchangeDailySpendLockKey)
	require.NoError(t, err)
	pid := testhelpers.BackendPID(env.ctx, t, holder)

	rec := awsDirectSave("ri-g1829-lock")
	done := make(chan error, 1)
	go func() { done <- env.storeB.SaveRIExchangeDirectExecution(env.ctx, rec) }()
	if got, answered := testhelpers.AwaitBlockedBy(env.ctx, t, env.poolB, pid, done); answered {
		_ = holder.Rollback(env.ctx)
		t.Fatalf("the save answered (%v) while the lock was held; it must wait for it", got)
	}
	var releasedAt time.Time
	require.NoError(t, holder.QueryRow(env.ctx, "SELECT clock_timestamp()").Scan(&releasedAt))
	require.NoError(t, holder.Commit(env.ctx))
	require.NoError(t, <-done)

	var createdAt time.Time
	require.NoError(t, env.poolA.QueryRow(env.ctx, "SELECT created_at FROM ri_exchange_direct_executions WHERE id = $1", rec.ID).Scan(&createdAt))
	assert.False(t, createdAt.Before(releasedAt), "created_at %s predates the lock release %s: the lock wait aged the row", createdAt, releasedAt)
}

func TestRIExchangeDirectSave_RaceExactlyOneWins(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	const parties = 6
	var wg sync.WaitGroup
	errs := make([]error, parties)
	start := make(chan struct{})
	for i := 0; i < parties; i++ {
		rec := awsDirectSave("ri-g1829-race", fmt.Sprintf("ri-g1829-own-%d", i))
		rec.Targets = json.RawMessage(fmt.Sprintf(`[{"offering_id":"o%d","count":%d}]`, i, i+1))
		rec.ID = uuid.NewString()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = env.storeA.SaveRIExchangeDirectExecution(env.ctx, rec)
		}(i)
	}
	close(start)
	wg.Wait()

	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		assert.ErrorIs(t, err, ErrRIExchangeSourceInFlight)
	}
	assert.Equal(t, 1, wins, "exactly one save may take the RI")
	assert.Equal(t, 1, env.directRows(t))
}
