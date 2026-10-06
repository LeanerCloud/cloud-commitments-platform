package server

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// TestRunMigrationsBounded_RealRunnerHonorsTimeout wires the production
// migrations.RunMigrations (not a context-obeying stub) to a database host that
// never answers. runMigrationsBoundedWith waits on the runner after the
// timeout, so the call returns only if the runner itself honors cancellation.
func TestRunMigrationsBounded_RealRunnerHonorsTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()

	cfg, err := pgxpool.ParseConfig("postgres://u:p@" + ln.Addr().String() + "/db?sslmode=disable")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "000001_x.up.sql"), []byte("SELECT 1;"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "000001_x.down.sql"), []byte("SELECT 1;"), 0o600))

	done := make(chan error, 1)
	go func() {
		done <- runMigrationsBoundedWith(pool, dir, "", "", 300*time.Millisecond, migrations.RunMigrations)
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		require.True(t, errors.Is(err, context.DeadlineExceeded), "want deadline error, got %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("runMigrationsBoundedWith still blocked on the runner 5s after its 300ms timeout")
	}
}
