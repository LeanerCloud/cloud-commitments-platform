//go:build integration

package database

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/tracelog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

type poolLifetimeKey struct{}

type poolLifetimeConstructor struct {
	role string
	ctx  context.Context
}

type poolLifetimeOutcome struct {
	role string
	err  error
}

type poolLifetimeRelease struct {
	callerErr, activeErr error
	closed, busy         bool
	txStatus             byte
}

type poolLifetimeTrace struct {
	*tracelog.TraceLog
	caller                             context.Context
	cancelCaller                       context.CancelFunc
	active                             context.Context
	mode                               string
	before                             chan poolLifetimeConstructor
	outcomes                           chan poolLifetimeOutcome
	releases                           chan poolLifetimeRelease
	fillStarted, foregroundStarted     chan struct{}
	fillConnected, releaseFill         chan struct{}
	fillStartOnce, foregroundStartOnce sync.Once
	fillConnectedOnce                  sync.Once
	releaseCount                       atomic.Int32
}

func (h *poolLifetimeTrace) TraceAcquireStart(ctx context.Context, pool *pgxpool.Pool, data pgxpool.TraceAcquireStartData) context.Context {
	h.active = ctx
	return context.WithValue(h.TraceLog.TraceAcquireStart(ctx, pool, data), poolLifetimeKey{}, "foreground")
}

func (h *poolLifetimeTrace) TraceAcquireEnd(ctx context.Context, pool *pgxpool.Pool, data pgxpool.TraceAcquireEndData) {
	h.TraceLog.TraceAcquireEnd(ctx, pool, data)
}

func (h *poolLifetimeTrace) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	h.TraceLog.TraceConnectEnd(ctx, data)
	role, _ := ctx.Value(poolLifetimeKey{}).(string)
	h.outcomes <- poolLifetimeOutcome{role: role, err: data.Err}
}

func (h *poolLifetimeTrace) TraceRelease(_ *pgxpool.Pool, data pgxpool.TraceReleaseData) {
	if h.releaseCount.Add(1) != 1 {
		return
	}
	h.releases <- poolLifetimeRelease{
		callerErr: h.caller.Err(), activeErr: h.active.Err(),
		closed: data.Conn.IsClosed(), busy: data.Conn.PgConn().IsBusy(),
		txStatus: data.Conn.PgConn().TxStatus(),
	}
	switch h.mode {
	case "late-caller":
		h.cancelCaller()
	case "late-deadline":
		<-h.active.Done()
	}
}

func (h *poolLifetimeTrace) beforeConnect(ctx context.Context, cfg *pgx.ConnConfig) error {
	role, _ := ctx.Value(poolLifetimeKey{}).(string)
	h.before <- poolLifetimeConstructor{role: role, ctx: ctx}
	if role != "caller" && role != "foreground" {
		return fmt.Errorf("unknown constructor role %q", role)
	}
	dial := cfg.DialFunc
	cfg.DialFunc = func(ctx context.Context, network, address string) (conn net.Conn, err error) {
		if role == "caller" {
			h.fillStartOnce.Do(func() { close(h.fillStarted) })
			select {
			case <-h.releaseFill:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		} else {
			select {
			case <-h.fillStarted:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			h.foregroundStartOnce.Do(func() { close(h.foregroundStarted) })
			if h.mode == "blocked-caller" || h.mode == "blocked-deadline" {
				<-ctx.Done()
				return nil, ctx.Err()
			}
		}
		return dial(ctx, network, address)
	}
	return nil
}

func (h *poolLifetimeTrace) afterConnect(ctx context.Context, _ *pgx.Conn) error {
	role, _ := ctx.Value(poolLifetimeKey{}).(string)
	if role == "caller" {
		h.fillConnectedOnce.Do(func() { close(h.fillConnected) })
	}
	return nil
}

func poolLifetimeAwait(t *testing.T, ctx context.Context, done <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("%s did not finish: %v", label, ctx.Err())
	}
}

func TestConnectionPoolInitializationLifetime(t *testing.T) {
	startup, cancelStartup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStartup()
	container, err := postgres.Run(startup, "postgres:16-alpine",
		postgres.WithDatabase("pool_lifetime"),
		postgres.WithUsername("pool_lifetime"),
		postgres.WithPassword("test_password"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(30*time.Second)),
	)
	if container != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, container.Terminate(ctx))
		})
	}
	require.NoError(t, err)
	dsn, err := container.ConnectionString(startup, "sslmode=disable")
	require.NoError(t, err)
	originalAttempts, originalTimeout := maxConnectRetries, perAttemptConnectTimeout
	maxConnectRetries, perAttemptConnectTimeout = 1, 3*time.Second
	t.Cleanup(func() {
		maxConnectRetries, perAttemptConnectTimeout = originalAttempts, originalTimeout
	})

	for _, mode := range []string{"successful-opening", "blocked-caller", "blocked-deadline", "late-caller", "late-deadline", "close-held-fill"} {
		t.Run(mode, func(t *testing.T) {
			deadman, stop := context.WithTimeout(context.Background(), 20*time.Second)
			t.Cleanup(stop)
			caller, cancelCaller := context.WithCancel(context.WithValue(deadman, poolLifetimeKey{}, "caller"))
			t.Cleanup(cancelCaller)
			outer, cancelOuter := context.WithTimeout(caller, 10*time.Second)
			h := &poolLifetimeTrace{
				TraceLog: &tracelog.TraceLog{Logger: &stdLogger{}, LogLevel: tracelog.LogLevelError},
				caller:   caller, cancelCaller: cancelCaller, mode: mode,
				before: make(chan poolLifetimeConstructor, 4), outcomes: make(chan poolLifetimeOutcome, 4),
				releases:    make(chan poolLifetimeRelease, 1),
				fillStarted: make(chan struct{}), foregroundStarted: make(chan struct{}),
				fillConnected: make(chan struct{}), releaseFill: make(chan struct{}),
			}
			cfg, err := pgxpool.ParseConfig(dsn)
			require.NoError(t, err)
			cfg.MinConns, cfg.MaxConns = 1, 2
			cfg.HealthCheckPeriod = time.Hour
			cfg.ConnConfig.ConnectTimeout = 5 * time.Second
			cfg.ConnConfig.Tracer = h
			cfg.BeforeConnect, cfg.AfterConnect = h.beforeConnect, h.afterConnect
			opened, closePool, workerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			releaseFill := sync.OnceFunc(func() { close(h.releaseFill) })
			requestClose := sync.OnceFunc(func() { close(closePool) })
			var candidate *poolCandidate
			var openErr error
			t.Cleanup(func() {
				defer releaseFill()
				cancelCaller()
				cancelOuter()
				requestClose()
				join, cancelJoin := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancelJoin()
				poolLifetimeAwait(t, join, workerDone, "owned opening/Close worker cleanup")
			})
			go func() {
				defer close(workerDone)
				if mode == "late-caller" || mode == "late-deadline" {
					candidate, openErr = attemptOpenPool(caller, outer, cfg, perAttemptConnectTimeout)
				} else {
					candidate, openErr = createConnectionPoolWithRetry(caller, cfg, &Config{})
				}
				close(opened)
				<-closePool
				if candidate != nil {
					conn := &Connection{pool: candidate.pool, cancelInitialFill: candidate.cancelInitialFill}
					conn.Close()
					conn.Close()
					(&Connection{pool: candidate.pool}).Close()
				}
			}()
			poolLifetimeAwait(t, deadman, h.foregroundStarted, "foreground constructor after held initial fill")
			if mode == "blocked-caller" {
				cancelCaller()
			}
			poolLifetimeAwait(t, deadman, opened, "bounded opening")
			constructors := make(map[string]context.Context)
			for range 2 {
				select {
				case before := <-h.before:
					require.NotContains(t, constructors, before.role, "constructor roles must be unique")
					constructors[before.role] = before.ctx
				case <-deadman.Done():
					t.Fatal("missing constructor context")
				}
			}
			require.Contains(t, constructors, "caller")
			require.Contains(t, constructors, "foreground")
			fillCtx := constructors["caller"]
			if mode == "successful-opening" || mode == "close-held-fill" {
				require.NoError(t, openErr, "independent healthy real-PG helper control")
				require.NotNil(t, candidate)
				require.NoError(t, fillCtx.Err(), "initial fill survives successful opening teardown")
				_, hasDeadline := fillCtx.Deadline()
				require.False(t, hasDeadline, "owned initial fill has no opening deadline")
				require.Equal(t, "caller", fillCtx.Value(poolLifetimeKey{}))
				require.Equal(t, int32(1), candidate.pool.Config().MinConns)
				require.Equal(t, int32(2), candidate.pool.Config().MaxConns)
				require.Equal(t, 5*time.Second, candidate.pool.Config().ConnConfig.ConnectTimeout)
				if mode == "successful-opening" {
					cancelCaller()
					require.NoError(t, fillCtx.Err(), "caller cancellation after transfer does not own fill")
					releaseFill()
					poolLifetimeAwait(t, deadman, h.fillConnected, "real initial connection completion")
				} else {
					require.NoError(t, caller.Err(), "Close control retains a live caller")
				}
			} else {
				require.Nil(t, candidate, "canceled opening must not publish a candidate")
				if mode == "late-caller" {
					require.True(t, openErr == context.Canceled, "post-nil-Ping refusal returns direct caller sentinel")
					require.True(t, openErr == caller.Err())
				} else if mode == "late-deadline" {
					require.NoError(t, caller.Err())
					require.True(t, openErr == context.DeadlineExceeded, "post-nil-Ping refusal returns direct active sentinel")
					require.True(t, openErr == h.active.Err())
				} else if mode == "blocked-caller" {
					require.ErrorIs(t, openErr, context.Canceled)
				} else {
					require.NoError(t, caller.Err())
					require.ErrorIs(t, openErr, context.DeadlineExceeded)
				}
				require.ErrorIs(t, fillCtx.Err(), context.Canceled, "failed opening cancels held fill before Close returns")
			}
			if mode != "blocked-caller" && mode != "blocked-deadline" {
				require.Equal(t, int32(1), h.releaseCount.Load())
				entry := <-h.releases
				require.NoError(t, entry.callerErr, "caller live at Release entry before intentional refusal")
				require.NoError(t, entry.activeErr, "active Ping live at Release entry before intentional refusal")
				require.False(t, entry.closed)
				require.False(t, entry.busy)
				require.Equal(t, byte('I'), entry.txStatus)
			} else {
				require.Zero(t, h.releaseCount.Load(), "blocked acquisition never reaches Release")
			}
			requestClose()
			poolLifetimeAwait(t, deadman, workerDone, "actual Connection.Close while fill owned")
			if mode == "close-held-fill" {
				require.ErrorIs(t, fillCtx.Err(), context.Canceled, "Close cancels held initialization before waiting")
			}
			outcomes := make(map[string]error)
			for range 2 {
				select {
				case result := <-h.outcomes:
					require.NotContains(t, outcomes, result.role)
					outcomes[result.role] = result.err
				case <-deadman.Done():
					t.Fatal("missing terminal connection outcome after pool join")
				}
			}
			require.Contains(t, outcomes, "caller")
			require.Contains(t, outcomes, "foreground")
			if mode == "successful-opening" {
				require.NoError(t, outcomes["caller"])
			} else {
				require.ErrorIs(t, outcomes["caller"], context.Canceled)
			}
			if mode == "blocked-caller" {
				require.ErrorIs(t, outcomes["foreground"], context.Canceled)
			} else if mode == "blocked-deadline" {
				require.ErrorIs(t, outcomes["foreground"], context.Canceled)
			} else {
				require.NoError(t, outcomes["foreground"])
			}
			assert.Empty(t, h.before, "no unknown or extra constructor role")
			assert.Empty(t, h.outcomes, "no unknown or extra terminal connection outcome")
		})
	}
}
