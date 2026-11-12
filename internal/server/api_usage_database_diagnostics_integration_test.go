//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/tracelog"
	"github.com/stretchr/testify/require"
)

type usageDiagnosticOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *usageDiagnosticOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *usageDiagnosticOutput) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type usageDiagnosticWrite struct {
	ctx     context.Context
	key     string
	delta   int64
	release chan struct{}
	result  chan error
}

type usageDiagnosticStore struct {
	*auth.PostgresStore
	writes chan usageDiagnosticWrite
}

func (s *usageDiagnosticStore) RecordAPIKeyUsage(ctx context.Context, key string, delta int64) error {
	w := usageDiagnosticWrite{ctx, key, delta, make(chan struct{}), make(chan error, 1)}
	select {
	case s.writes <- w:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-w.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	err := s.PostgresStore.RecordAPIKeyUsage(ctx, key, delta)
	select {
	case w.result <- err:
	case <-ctx.Done():
	}
	return err
}

type usageDiagnosticAdapter struct {
	*authServiceAdapter
	bookings atomic.Int64
}

func (a *usageDiagnosticAdapter) RecordAPIKeyUsageAsync(key string) {
	a.bookings.Add(1)
	a.authServiceAdapter.RecordAPIKeyUsageAsync(key)
}

type usageDiagnosticTrace struct {
	ctx  context.Context
	msg  string
	data map[string]any
	safe bool
}

func TestAPIDatabaseDiagnosticsUsageHandler(t *testing.T) {
	if os.Getenv("CC_DU_HELPER") != "1" {
		runUsageDiagnosticParent(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute+45*time.Second)
	defer cancel()
	db := usageDiagnosticChildConnection(t, ctx)
	childPools := []*pgxpool.Pool{db.Pool()}
	poolsClosed := false
	t.Cleanup(func() {
		if !poolsClosed {
			poolsClosed = true
			closeUsageDiagnosticChildPools(t, ctx, childPools)
		}
	})
	var output usageDiagnosticOutput
	oldOutput, oldLevel := logging.SetOutput(io.MultiWriter(os.Stderr, &output)), logging.GetLevel()
	logging.SetLevelValue(logging.LevelDebug)
	t.Cleanup(func() { logging.SetOutput(oldOutput); logging.SetLevelValue(oldLevel) })
	t.Setenv("DB_LOG_BIND_PARAMETERS", "true")
	config := db.Pool().Config().Copy()
	original := config.ConnConfig.Tracer.(*tracelog.TraceLog).Logger
	observations := make(chan usageDiagnosticTrace, 512)
	var overflow atomic.Bool
	config.ConnConfig.Tracer = &tracelog.TraceLog{LogLevel: tracelog.LogLevelDebug, Logger: tracelog.LoggerFunc(func(ctx context.Context, level tracelog.LogLevel, msg string, data map[string]any) {
		original.Log(ctx, level, msg, data)
		select {
		case observations <- usageDiagnosticTrace{ctx, msg, data, database.WithSafeDiagnostics(ctx) == ctx}:
		default:
			overflow.Store(true)
		}
	})}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	childPools = append(childPools, pool)
	store := &usageDiagnosticStore{PostgresStore: auth.NewPostgresStore(pool), writes: make(chan usageDiagnosticWrite, 8)}
	permissions := []auth.Permission{{Action: auth.ActionView, Resource: auth.ResourceAPIKeys}}
	group := &auth.Group{ID: uuid.NewString(), Name: "diagnostics", Permissions: permissions, AllowedAccounts: []string{}}
	require.NoError(t, store.CreateGroup(ctx, group))
	user := &auth.User{ID: uuid.NewString(), Email: "diagnostics@example.com", PasswordHash: "synthetic-hash", Salt: "synthetic-salt", Active: true, GroupIDs: []string{group.ID}, PasswordHistory: []string{}}
	require.NoError(t, store.CreateUser(ctx, user))
	storedUser, err := store.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	key := &auth.UserAPIKey{ID: uuid.NewString(), UserID: user.ID, Name: "diagnostics", KeyPrefix: "synthetic", KeyHash: usageBookingKeyHash(), IsActive: true, Permissions: permissions, PasswordVersion: storedUser.PasswordVersion}
	require.NoError(t, store.CreateAPIKey(ctx, key))
	service := auth.NewService(auth.ServiceConfig{Store: store, SessionDuration: time.Hour, CSRFKey: auth.TestCSRFKey()})
	adapter := &usageDiagnosticAdapter{authServiceAdapter: newAuthServiceAdapter(service)}
	handler := api.NewHandler(api.HandlerConfig{AuthService: adapter, CORSAllowedOrigin: "https://dashboard.example.com"})
	harness := &usageBookingHarness{handler: handler}
	request := func(method, path string, status int, booked int64) usageDiagnosticWrite {
		parent, stop := context.WithCancel(ctx)
		defer stop()
		response, err := handler.HandleRequest(parent, harness.request(method, path))
		require.NoError(t, err)
		require.Equal(t, status, response.StatusCode, response.Body)
		require.Equal(t, booked, adapter.bookings.Load(), "booking is synchronous before handler returns")
		if status == 200 {
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(response.Body), &fields))
			require.NotNil(t, fields)
			for _, field := range []string{"total_active", "total_requests_window", "total_requests_lifetime", "lifetime_partial", "top_keys"} {
				value, present := fields[field]
				require.True(t, present, "required public field: %s", field)
				require.NotEqual(t, "null", strings.TrimSpace(string(value)), "nonnull public field: %s", field)
			}
			var stats auth.APIKeysUsageStatsResponse
			require.NoError(t, json.Unmarshal([]byte(response.Body), &stats))
			require.Equal(t, 1, stats.TotalActive)
			require.Equal(t, booked-1, stats.TotalRequestsWindow)
			require.Equal(t, booked-1, stats.TotalRequestsLifetime)
			require.False(t, stats.LifetimePartial)
			if booked == 1 {
				require.NotNil(t, stats.TopKeys)
				require.Empty(t, stats.TopKeys)
			} else {
				require.Equal(t, []auth.APIKeysUsageStatsTopKey{{ID: key.ID, Name: key.Name, KeyPrefix: key.KeyPrefix, RequestCountWindow: 1}}, stats.TopKeys)
			}
			for _, private := range []string{usageBookingRawKey, key.KeyHash, user.PasswordHash, user.Salt, "password", "key_hash", "credentials"} {
				require.NotContains(t, response.Body, private)
			}
		}
		operation, end := context.WithTimeout(ctx, 10*time.Second)
		defer end()
		select {
		case w := <-store.writes:
			stop()
			require.ErrorIs(t, parent.Err(), context.Canceled)
			require.NoError(t, w.ctx.Err(), "request cancellation must not cancel usage")
			deadline, ok := w.ctx.Deadline()
			require.True(t, ok)
			require.WithinDuration(t, time.Now().Add(5*time.Second), deadline, time.Second)
			require.Equal(t, key.ID, w.key)
			require.EqualValues(t, 1, w.delta)
			return w
		case <-operation.Done():
			t.Fatal("usage-write-observation-timeout")
			return usageDiagnosticWrite{}
		}
	}
	finish := func(w usageDiagnosticWrite, failed bool) error {
		close(w.release)
		select {
		case err := <-w.result:
			if failed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			return err
		case <-w.ctx.Done():
			t.Fatal("usage-write-result-timeout")
			return w.ctx.Err()
		}
	}
	checkCount := func(want int64) {
		stored, err := store.GetAPIKeyByID(ctx, key.ID)
		require.NoError(t, err)
		require.Equal(t, want, stored.RequestCountTotal)
		require.Equal(t, want, stored.RequestCountWindow)
		require.NotNil(t, stored.LastUsedAt)
		require.NotNil(t, stored.RequestCountWindowStart)
		require.WithinDuration(t, time.Now(), *stored.LastUsedAt, time.Minute)
		require.WithinDuration(t, *stored.LastUsedAt, *stored.RequestCountWindowStart, time.Minute)
	}
	finish(request("GET", "/api/api-keys/usage-stats", 200, 1), false)
	checkCount(1)
	_, err = pool.Exec(ctx, `CREATE FUNCTION du_usage_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'usage-marker' USING DETAIL='usage-detail'; END $$; CREATE TRIGGER du_usage_fail BEFORE UPDATE ON api_keys FOR EACH ROW EXECUTE FUNCTION du_usage_fail()`)
	require.NoError(t, err)
	failed := finish(request("GET", "/api/api-keys/usage-stats", 200, 2), true)
	var failedPG *pgconn.PgError
	require.ErrorAs(t, failed, &failedPG)
	require.Contains(t, failed.Error(), "failed to record API key usage:")
	require.Equal(t, "P0001", failedPG.Code)
	require.Equal(t, "usage-marker", failedPG.Message)
	require.Equal(t, "usage-detail", failedPG.Detail)
	checkCount(1)
	// This synchronous unmarked control deliberately retains verbose output and errors.
	synchronous := make(chan error, 1)
	directCtx, stopDirect := context.WithTimeout(ctx, 5*time.Second)
	defer stopDirect()
	go func() {
		err := service.RecordUsage(directCtx, key.ID, 1)
		select {
		case synchronous <- err:
		case <-directCtx.Done():
		}
	}()
	var direct usageDiagnosticWrite
	select {
	case direct = <-store.writes:
	case <-directCtx.Done():
		t.Fatal("direct-write-observation-timeout")
	}
	directErr := finish(direct, true)
	select {
	case returned := <-synchronous:
		require.Same(t, directErr, returned)
	case <-directCtx.Done():
		t.Fatal("direct-result-timeout")
	}
	_, err = pool.Exec(ctx, `DROP TRIGGER du_usage_fail ON api_keys`)
	require.NoError(t, err)
	finish(request("DELETE", "/api/purchases/planned/11111111-1111-1111-1111-111111111111", 403, 3), false)
	checkCount(2)
	poolsClosed = true
	require.True(t, closeUsageDiagnosticChildPools(t, ctx, childPools), "child-pool-close-pending")
	require.False(t, overflow.Load(), "native-trace-observation-overflow")
	var traces []usageDiagnosticTrace
	draining := true
	for draining {
		select {
		case trace := <-observations:
			traces = append(traces, trace)
		case <-ctx.Done():
			t.Fatal("native-trace-drain-timeout")
		default:
			draining = false
		}
	}
	var safeRecords []string
	for _, line := range strings.Split(output.text(), "\n") {
		if strings.Contains(line, "Database event=") {
			safeRecords = append(safeRecords, line)
		}
	}
	safeCallbacks, usageCallbacks, foreground, directCallbacks := 0, 0, 0, 0
	var tracedFailure *pgconn.PgError
	for _, trace := range traces {
		if trace.msg == "Release" {
			require.False(t, trace.safe)
			require.Len(t, trace.data, 1)
			_, ok := trace.data["pid"].(uint32)
			require.True(t, ok)
		}
		if trace.safe {
			safeCallbacks++
			elapsed, ok := trace.data["time"].(time.Duration)
			require.True(t, ok)
			require.GreaterOrEqual(t, elapsed, time.Duration(0))
			class, state := "ok", ""
			if trace.data["err"] != nil {
				class, state = "database", " sqlstate=P0001"
				require.True(t, errors.As(trace.data["err"].(error), &tracedFailure))
				require.Same(t, failedPG, tracedFailure)
			}
			require.Contains(t, output.text(), fmt.Sprintf("Database event=%s class=%s%s duration=%s", trace.msg, class, state, elapsed))
		}
		if trace.msg != "Query" {
			continue
		}
		sql, _ := trace.data["sql"].(string)
		if strings.Contains(sql, "UPDATE api_keys") && strings.Contains(sql, "request_count_total") {
			if trace.safe {
				usageCallbacks++
			} else {
				directCallbacks++
				var directPG *pgconn.PgError
				require.ErrorAs(t, directErr, &directPG)
				require.Same(t, directPG, trace.data["err"])
				require.Contains(t, output.text(), directPG.Error())
			}
		} else if strings.Contains(sql, "FROM api_keys") {
			require.False(t, trace.safe, "foreground auth remains unmarked in D-U")
			foreground++
		}
	}
	require.Equal(t, 3, usageCallbacks, "success, failure and later denial each issue one actual UPDATE")
	require.Equal(t, 1, directCallbacks)
	require.Positive(t, foreground)
	require.NotNil(t, tracedFailure)
	require.Positive(t, safeCallbacks)
	require.Len(t, safeRecords, safeCallbacks, "every actual marked callback must emit a coded record")
	for _, line := range safeRecords {
		for _, raw := range []string{"usage-marker", "usage-detail", usageBookingRawKey, key.ID, key.KeyHash, "UPDATE", "args:", "sql:", "err:", "pid:"} {
			require.NotContains(t, line, raw)
		}
	}
	require.Contains(t, output.text(), "usage-marker", "application warning/unmarked control is retained privately")
	t.Log("DU_HELPER_COMPLETE")
}
