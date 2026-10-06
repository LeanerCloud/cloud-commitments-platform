package migrations

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// stalledServer accepts TCP connections and never answers, like a database
// host that is reachable but wedged (or a proxy that swallowed the startup
// packet). It returns the pool config pointing at it.
func stalledServer(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var held []net.Conn
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			held = append(held, c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-accepted
		for _, c := range held {
			_ = c.Close()
		}
	})

	cfg, err := pgxpool.ParseConfig("postgres://u:p@" + ln.Addr().String() + "/db?sslmode=disable")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func writeMigration(t *testing.T, sql string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "000001_noop.up.sql"), []byte(sql), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "000001_noop.down.sql"), []byte("SELECT 1;"), 0o600))
	return dir
}

// TestRunMigrations_ReturnsWhenDeadlineExpiresDuringConnectionSetup drives the
// real RunMigrations against a server that never completes the startup
// handshake. golang-migrate's pgx driver pings with context.Background, so
// without the cancellation wiring the call blocks until the process dies.
func TestRunMigrations_ReturnsWhenDeadlineExpiresDuringConnectionSetup(t *testing.T) {
	pool := stalledServer(t)
	dir := writeMigration(t, "SELECT 1;")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- RunMigrations(ctx, pool, dir, "", "") }()

	select {
	case err := <-done:
		require.Error(t, err)
		require.True(t, errors.Is(err, context.DeadlineExceeded), "want deadline error, got %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("RunMigrations did not return within 5s of its 300ms deadline")
	}
}

func TestRunMigrations_ReturnsWhenContextCanceledDuringConnectionSetup(t *testing.T) {
	pool := stalledServer(t)
	dir := writeMigration(t, "SELECT 1;")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunMigrations(ctx, pool, dir, "", "") }()
	time.AfterFunc(200*time.Millisecond, cancel)

	select {
	case err := <-done:
		require.Error(t, err)
		require.True(t, errors.Is(err, context.Canceled), "want canceled error, got %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("RunMigrations did not return within 5s of cancellation")
	}
}

func TestWrapContextError_KeepsBothErrorChains(t *testing.T) {
	cause := errors.New("driver failure")

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	wrapped := wrapContextError(ctx, cause)
	require.ErrorIs(t, wrapped, context.DeadlineExceeded)
	require.ErrorIs(t, wrapped, cause)

	require.Same(t, cause, wrapContextError(context.Background(), cause), "live context must not wrap")
	require.NoError(t, wrapContextError(ctx, nil))
}
