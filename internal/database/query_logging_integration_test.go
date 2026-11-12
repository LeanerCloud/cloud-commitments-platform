//go:build integration

package database_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/tracelog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type queryLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func TestAPIDatabaseDiagnosticsUsageNativeQuery(t *testing.T) {
	overall, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	overallDeadline, _ := overall.Deadline()
	ctx, stopProductive := context.WithDeadline(overall, overallDeadline.Add(-60*time.Second))
	defer stopProductive()
	setup, setupCancel := context.WithTimeout(ctx, 120*time.Second)
	container := testhelpers.RequirePostgresContainer(setup, t)
	setupCancel()
	pools := []*pgxpool.Pool{container.DB.Pool()}
	const application = "du-native-query"
	t.Cleanup(func() {
		result := beginQueryDiagnosticsCleanup(container, pools, application, overallDeadline, nil)
		result.wait()
		t.Logf("query cleanup: reconcile=%v termination_complete=%v termination_error=%v close_complete=%d/%d", result.reconcile, result.terminated, result.termination, result.closed, len(result.closes))
		require.True(t, result.clean(), "incomplete fixture cleanup is inconclusive")
	})
	// The dependency health probe uses Background; these deadlines bound only ctx-aware phases.
	require.NoError(t, ctx.Err(), "productive reserve missed during dependency setup; inconclusive")
	var output queryLogBuffer
	oldOutput, oldLevel := logging.SetOutput(io.MultiWriter(os.Stderr, &output)), logging.GetLevel()
	logging.SetLevelValue(logging.LevelDebug)
	t.Cleanup(func() { logging.SetOutput(oldOutput); logging.SetLevelValue(oldLevel) })
	t.Setenv("DB_LOG_BIND_PARAMETERS", "true")
	require.NoError(t, ctx.Err(), "no new statement after productive deadline")
	_, err := container.DB.Exec(ctx, `CREATE FUNCTION du_query_fail(text) RETURNS void LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'query-marker' USING DETAIL='query-detail'; END $$`)
	require.NoError(t, err)
	config := container.DB.Pool().Config().Copy()
	original := config.ConnConfig.Tracer.(*tracelog.TraceLog).Logger
	type observation struct {
		ctx  context.Context
		data map[string]any
	}
	queries := make(chan observation, 16)
	releases := make(chan map[string]any, 16)
	var overflow atomic.Bool
	config.ConnConfig.RuntimeParams["application_name"] = application
	config.ConnConfig.Tracer = &tracelog.TraceLog{LogLevel: tracelog.LogLevelDebug, Logger: tracelog.LoggerFunc(func(ctx context.Context, level tracelog.LogLevel, msg string, data map[string]any) {
		original.Log(ctx, level, msg, data)
		if msg == "Query" {
			select {
			case queries <- observation{ctx, data}:
			default:
				overflow.Store(true)
			}
		}
		if msg == "Release" {
			select {
			case releases <- data:
			default:
				overflow.Store(true)
			}
		}
	})}
	require.NoError(t, ctx.Err(), "no new pool after productive deadline")
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	pools = append(pools, pool)
	const sql = `SELECT du_query_fail($1)`
	for _, safe := range []bool{true, false} {
		require.NoError(t, ctx.Err(), "no new query after productive deadline")
		queryCtx := ctx
		if safe {
			queryCtx = database.WithSafeDiagnostics(ctx)
		}
		output.take()
		_, err := pool.Exec(queryCtx, sql, "query-bound-secret")
		var returned *pgconn.PgError
		require.ErrorAs(t, err, &returned)
		operation, stop := context.WithTimeout(ctx, 10*time.Second)
		var observed observation
		select {
		case observed = <-queries:
		case <-operation.Done():
			t.Fatal("query-observation-timeout")
		}
		var traced *pgconn.PgError
		require.ErrorAs(t, observed.data["err"].(error), &traced)
		require.Same(t, returned, traced)
		require.Equal(t, "P0001", returned.Code)
		require.Equal(t, "query-marker", returned.Message)
		require.Equal(t, "query-detail", returned.Detail)
		elapsed, ok := observed.data["time"].(time.Duration)
		require.True(t, ok)
		require.GreaterOrEqual(t, elapsed, time.Duration(0))
		logged := output.take()
		require.NotEmpty(t, logged)
		if safe {
			require.Contains(t, logged, fmt.Sprintf("Database event=Query class=database sqlstate=P0001 duration=%s", elapsed))
			for _, raw := range []string{"query-marker", "query-detail", "query-bound-secret", sql, "err:", "args:", "sql:"} {
				require.NotContains(t, logged, raw)
			}
		} else {
			require.Contains(t, logged, sql)
			require.Contains(t, logged, "query-bound-secret")
			require.Contains(t, logged, returned.Error())
		}
		var release map[string]any
		select {
		case release = <-releases:
		case <-operation.Done():
			t.Fatal("release-observation-timeout")
		}
		stop()
		require.False(t, overflow.Load(), "query-observation-overflow")
		require.Len(t, release, 1)
		_, ok = release["pid"].(uint32)
		require.True(t, ok, "contextless Release contains only numeric backend PID")
	}
}

type queryDiagnosticsCleanup struct {
	reconcile, termination error
	terminated             bool
	closes                 []chan struct{}
	closed                 int
	deadline               time.Time
}

func beginQueryDiagnosticsCleanup(fixture *testhelpers.PostgresContainer, pools []*pgxpool.Pool, application string, overallDeadline time.Time, observer *queryCloseObserver) queryDiagnosticsCleanup {
	result := queryDiagnosticsCleanup{deadline: queryDiagnosticsDeadline(overallDeadline, 60*time.Second)}
	reconcile, cancel := context.WithDeadline(context.Background(), queryDiagnosticsDeadline(result.deadline, 10*time.Second))
	var backends int
	var reconciled bool
	result.reconcile = fixture.DB.QueryRow(reconcile, `SELECT count(*), COALESCE(bool_and(pg_terminate_backend(pid)),true) FROM pg_stat_activity WHERE application_name=$1 AND pid<>pg_backend_pid()`, application).Scan(&backends, &reconciled)
	if result.reconcile == nil && !reconciled {
		result.reconcile = fmt.Errorf("owned query backend termination incomplete")
	}
	cancel()
	for _, pool := range pools {
		done := make(chan struct{})
		result.closes = append(result.closes, done)
		go func() {
			observer.publish(pool)
			pool.Close()
			close(done)
		}()
	}
	termination, stop := context.WithDeadline(context.Background(), queryDiagnosticsDeadline(result.deadline, 30*time.Second))
	defer stop()
	terminated := make(chan error, 1)
	go func() { terminated <- fixture.Container.Terminate(termination) }()
	select {
	case result.termination = <-terminated:
		result.terminated = true
	case <-termination.Done():
		result.termination = termination.Err()
	}
	return result
}

func (r *queryDiagnosticsCleanup) wait() {
	ctx, cancel := context.WithDeadline(context.Background(), queryDiagnosticsDeadline(r.deadline, 15*time.Second))
	defer cancel()
	for _, done := range r.closes {
		select {
		case <-done:
			r.closed++
		case <-ctx.Done():
			return
		}
	}
}

func (r queryDiagnosticsCleanup) clean() bool {
	return r.reconcile == nil && r.terminated && r.termination == nil && r.closed == len(r.closes)
}

func queryDiagnosticsDeadline(overall time.Time, budget time.Duration) time.Time {
	deadline := time.Now().Add(budget)
	if overall.Before(deadline) {
		return overall
	}
	return deadline
}

func TestAPIDatabaseDiagnosticsUsageHeldLeaseCleanup(t *testing.T) {
	runQueryDiagnosticsHeldLease(t)
}

func (b *queryLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *queryLogBuffer) take() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := b.buf.String()
	b.buf.Reset()
	return text
}

func TestQueryLogging_RedactsBoundSecretsOnFailure(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RequirePostgresContainer(ctx, t)
	var output queryLogBuffer
	previousOutput := logging.SetOutput(&output)
	previousLevel := logging.GetLevel()
	logging.SetLevelValue(logging.LevelDebug)
	t.Cleanup(func() {
		assert.NoError(t, container.Cleanup(context.Background()))
		logging.SetOutput(previousOutput)
		logging.SetLevelValue(previousLevel)
	})

	_, err := container.DB.Exec(ctx, `CREATE TABLE query_logging_secrets (
		session_token text, password_hash text, approval_token text,
		accepted integer CHECK (accepted > 0))`)
	require.NoError(t, err)
	secrets := []string{strings.Repeat("a", 64), strings.Repeat("b", 60), strings.Repeat("c", 64)}
	for _, optIn := range []string{"", "true"} {
		t.Run("bind_parameters="+optIn, func(t *testing.T) {
			t.Setenv("DB_LOG_BIND_PARAMETERS", optIn)
			output.take()
			_, err := container.DB.Exec(ctx,
				`INSERT INTO query_logging_secrets VALUES ($1, $2, $3, $4)`,
				secrets[0], secrets[1], secrets[2], 0)
			var pgErr *pgconn.PgError
			require.ErrorAs(t, err, &pgErr)
			require.Equal(t, "23514", pgErr.Code)
			for _, secret := range secrets {
				require.NotContains(t, pgErr.Error(), secret)
			}
			logged := output.take()
			assert.Contains(t, logged, "Query")
			assert.Contains(t, logged, pgErr.Error())
			for _, secret := range secrets {
				if optIn == "true" {
					assert.Contains(t, logged, secret)
				} else {
					assert.NotContains(t, logged, secret)
				}
			}
			if optIn == "true" {
				assert.Contains(t, logged, "args:")
			} else {
				assert.NotContains(t, logged, "args:")
			}
		})
	}
}
