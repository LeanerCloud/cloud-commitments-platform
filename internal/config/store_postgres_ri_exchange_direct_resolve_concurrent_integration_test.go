//go:build integration

package config

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real-PostgreSQL coverage for #1836: the resolve against concurrent resolves
// and against a concurrent Finalize, in both orders.

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs routes the package logger to a locked buffer (Finalize runs in a
// goroutine). Not parallel: the logger is process-global.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	out := &lockedBuffer{}
	prev := logging.SetOutput(out)
	t.Cleanup(func() { logging.SetOutput(prev) })
	return out
}

// [fail-first] Two concurrent resolves of one row: exactly one wins, and the
// stored resolution is the winner's.
func TestPostgresStoreDB_ResolveRIExchangeDirectExecution_ConcurrentOneWins(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	id := env.directAged(t, "unknown", "2 hours", "1 hour")
	checked := time.Now().UTC().Add(-time.Minute)
	reqs := []RIExchangeDirectResolve{directResolveReq(id, "failed", "unknown", checked), directResolveReq(id, "completed", "unknown", checked)}
	stores := []*PostgresStore{env.storeA, env.storeB}

	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range reqs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = stores[i].ResolveRIExchangeDirectExecution(env.ctx, reqs[i])
		}(i)
	}
	close(start)
	wg.Wait()

	winner := -1
	for i, err := range errs {
		if err == nil {
			require.Equal(t, -1, winner, "two resolves both succeeded")
			winner = i
			continue
		}
		require.ErrorIs(t, err, ErrRIExchangeResolveConflict)
	}
	require.NotEqual(t, -1, winner, "one resolve must win")
	var got string
	require.NoError(t, env.poolA.QueryRow(env.ctx, `SELECT resolved_status FROM ri_exchange_direct_executions WHERE id = $1`, id).Scan(&got))
	assert.Equal(t, reqs[winner].Outcome, got)
}

// holdRow opens a transaction that has written the row and not committed.
func (e *riExchangeIntegrationEnv) holdRow(t *testing.T, id, setSQL string) (commit func(), pid int) {
	t.Helper()
	tx, err := e.poolA.Begin(e.ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(e.ctx) })
	tag, err := tx.Exec(e.ctx, `UPDATE ri_exchange_direct_executions SET `+setSQL+` WHERE id = $1`, id)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())
	return func() { require.NoError(t, tx.Commit(e.ctx)) }, testhelpers.BackendPID(e.ctx, t, tx)
}

// [fail-first] Finalize wins the row lock first: the resolve waits, then sees
// the new status and conflicts.
func TestPostgresStoreDB_ResolveRIExchangeDirectExecution_FinalizeFirst(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	id := env.directAged(t, "submitted", "1 hour", "0")
	commit, pid := env.holdRow(t, id, `status = 'unknown', completed_at = now()`)

	done := make(chan error, 1)
	go func() {
		_, err := env.resolve(t, directResolveReq(id, "failed", "submitted", time.Now().UTC().Add(-time.Minute)))
		done <- err
	}()
	if err, answered := testhelpers.AwaitBlockedBy(env.ctx, t, env.poolB, pid, done); answered {
		t.Fatalf("resolve answered (%v) while a finalize held the row", err)
	}
	commit()
	err := <-done
	require.ErrorIs(t, err, ErrRIExchangeResolveConflict)
	assert.Contains(t, err.Error(), "unknown")
}

// [fail-first] Resolve wins first: Finalize waits on the row lock, then still
// stores the true outcome beside the resolution, and logs when they differ.
func TestPostgresStoreDB_ResolveRIExchangeDirectExecution_ResolveFirstThenFinalize(t *testing.T) {
	for _, tc := range []struct {
		name, resolved, finalStatus string
		wantLog                     bool
	}{
		{"contradiction is logged", "failed", RIExchangeDirectStatusCompleted, true},
		{"agreement is not", "completed", RIExchangeDirectStatusCompleted, false},
		{"unknown does not contradict", "failed", RIExchangeDirectStatusUnknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			env := newRIExchangeIntegrationEnv(t)
			id := env.directAged(t, "submitted", "1 hour", "0")
			commit, pid := env.holdRow(t, id,
				`resolved_status = '`+tc.resolved+`', resolved_by = 'op', resolved_at = now(), resolution_note = 'n'`)

			done := make(chan error, 1)
			go func() {
				done <- env.storeB.FinalizeRIExchangeDirectExecution(env.ctx, id, RIExchangeDirectOutcome{Status: tc.finalStatus})
			}()
			if err, answered := testhelpers.AwaitBlockedBy(env.ctx, t, env.poolA, pid, done); answered {
				t.Fatalf("finalize answered (%v) while the resolution was uncommitted", err)
			}
			commit()
			require.NoError(t, <-done)

			var status, rs string
			require.NoError(t, env.poolA.QueryRow(env.ctx,
				`SELECT status, resolved_status FROM ri_exchange_direct_executions WHERE id = $1`, id).Scan(&status, &rs))
			assert.Equal(t, tc.finalStatus, status, "the provider outcome is stored")
			assert.Equal(t, tc.resolved, rs, "beside the operator's resolution")
			assert.Equal(t, tc.wantLog, strings.Contains(logs.String(), "contradicts operator resolution"), logs.String())
		})
	}
}
