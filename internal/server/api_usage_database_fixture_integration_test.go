//go:build integration

package server

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func runUsageDiagnosticParent(t *testing.T) {
	t.Helper()
	overall, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	deadline, _ := overall.Deadline()
	productive, stopProductive := context.WithDeadline(overall, deadline.Add(-60*time.Second))
	defer stopProductive()
	setup, stop := context.WithTimeout(productive, 120*time.Second)
	defer stop()
	fixture := testhelpers.RequirePostgresContainer(setup, t)
	application := "du-child-" + uuid.NewString()
	t.Cleanup(func() {
		result := cleanupUsageDiagnosticFixture(fixture, application, deadline, nil)
		t.Logf("parent cleanup: reconcile=%v termination_complete=%v termination_error=%v close_complete=%v", result.reconcile, result.terminated, result.termination, result.closed)
		require.True(t, result.clean(), "pending/failed fixture cleanup is inconclusive")
	})
	// Dependency Background probes and native Close are not universally bounded by these contexts.
	require.NoError(t, productive.Err(), "productive reserve missed during dependency setup; inconclusive")
	_, source, _, _ := runtime.Caller(0)
	require.NoError(t, migrations.RunMigrations(setup, fixture.DB.Pool(), filepath.Join(filepath.Dir(source), "..", "database", "postgres", "migrations"), "", ""))
	stop()
	requireUsageDiagnosticSettings(t, fixture.Config.Host, fixture.Config.Port, fixture.Config.Database, fixture.Config.User, fixture.Config.Password)
	owned := t.TempDir()
	stdout, err := os.Create(filepath.Join(owned, "stdout.log"))
	require.NoError(t, err)
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(owned, "stderr.log"))
	require.NoError(t, err)
	defer stderr.Close()
	for _, name := range []string{"HOME", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "AZURE_CLIENT_SECRET", "DOCKER_HOST"} {
		t.Setenv(name, "synthetic-parent-poison")
	}
	childDeadline := usageDiagnosticDeadline(deadline.Add(-60*time.Second), 5*time.Minute)
	require.True(t, time.Now().Before(childDeadline), "do not launch without cleanup reserve")
	child, stopChild := context.WithDeadline(context.Background(), childDeadline)
	defer stopChild()
	executable, err := os.Executable()
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(executable))
	cmd := exec.CommandContext(child, executable, "-test.run=^TestAPIDatabaseDiagnosticsUsageHandler$", "-test.v", "-test.timeout=5m")
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = []string{
		"CC_DU_HELPER=1", "CC_DU_DB_HOST=" + fixture.Config.Host,
		"CC_DU_DB_PORT=" + strconv.Itoa(fixture.Config.Port), "CC_DU_DB_NAME=cudly_test",
		"CC_DU_DB_USER=cudly_test", "CC_DU_DB_PASSWORD=test_password", "CC_DU_DB_SSLMODE=disable",
		"PGAPPNAME=" + application, "TMPDIR=" + owned,
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	stdoutBytes, outErr := os.ReadFile(stdout.Name())
	stderrBytes, errErr := os.ReadFile(stderr.Name())
	t.Logf("Complete private stdout through child exit:\n%s", stdoutBytes)
	t.Logf("Complete private stderr through child exit:\n%s", stderrBytes)
	require.NoError(t, outErr)
	require.NoError(t, errErr)
	require.NoError(t, child.Err(), "timed helper exit is inconclusive")
	require.NoError(t, runErr, "abnormal/WaitDelay helper exit is inconclusive")
	require.Contains(t, string(stdoutBytes), "DU_HELPER_COMPLETE")
	require.NotContains(t, string(stdoutBytes), "--- SKIP")
}

func requireUsageDiagnosticSettings(t *testing.T, host string, port int, name, user, password string) {
	t.Helper()
	ip := net.ParseIP(host)
	require.True(t, host == "localhost" || (ip != nil && ip.IsLoopback()), "fixture must be loopback")
	require.Greater(t, port, 0)
	require.LessOrEqual(t, port, 65535)
	require.Equal(t, "cudly_test", name)
	require.Equal(t, "cudly_test", user)
	require.Equal(t, "test_password", password)
}

func usageDiagnosticChildConnection(t *testing.T, ctx context.Context) *database.Connection {
	t.Helper()
	for _, name := range []string{"HOME", "PATH", "DOCKER_HOST", "DOCKER_CONTEXT", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_PROFILE", "AWS_SESSION_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "AZURE_CLIENT_SECRET"} {
		require.Empty(t, os.Getenv(name), "parent environment must not enter child: %s", name)
	}
	port, err := strconv.Atoi(os.Getenv("CC_DU_DB_PORT"))
	require.NoError(t, err)
	host, name, user, password := os.Getenv("CC_DU_DB_HOST"), os.Getenv("CC_DU_DB_NAME"), os.Getenv("CC_DU_DB_USER"), os.Getenv("CC_DU_DB_PASSWORD")
	requireUsageDiagnosticSettings(t, host, port, name, user, password)
	require.Equal(t, "disable", os.Getenv("CC_DU_DB_SSLMODE"))
	application := os.Getenv("PGAPPNAME")
	require.Len(t, application, len("du-child-")+36)
	_, err = uuid.Parse(application[len("du-child-"):])
	require.NoError(t, err)
	require.Equal(t, "du-child-", application[:len("du-child-")])
	config := &database.Config{Host: host, Port: port, Database: name, User: user, Password: password, SSLMode: "disable", MaxConnections: 10, MinConnections: 1, ConnectTimeout: 5 * time.Second, HealthCheckPeriod: time.Minute, LogLevel: "debug"}
	db, err := database.NewConnection(ctx, config, nil)
	require.NoError(t, err)
	require.Equal(t, application, db.Pool().Config().ConnConfig.RuntimeParams["application_name"])
	return db
}

type usageDiagnosticCleanup struct {
	reconcile, termination error
	terminated, closed     bool
	closeDone              <-chan struct{}
}

func (r usageDiagnosticCleanup) clean() bool {
	return r.reconcile == nil && r.terminated && r.termination == nil && r.closed
}

func cleanupUsageDiagnosticFixture(fixture *testhelpers.PostgresContainer, application string, overallDeadline time.Time, observer *usageCloseObserver) usageDiagnosticCleanup {
	var result usageDiagnosticCleanup
	// Reserve the same sixty seconds, including the child's five-second WaitDelay.
	deadline := usageDiagnosticDeadline(overallDeadline, 55*time.Second)
	reconcile, cancel := context.WithDeadline(context.Background(), usageDiagnosticDeadline(deadline, 10*time.Second))
	var backends int
	var reconciled bool
	result.reconcile = fixture.DB.QueryRow(reconcile, `SELECT count(*), COALESCE(bool_and(pg_terminate_backend(pid)),true) FROM pg_stat_activity WHERE application_name=$1 AND pid<>pg_backend_pid()`, application).Scan(&backends, &reconciled)
	if result.reconcile == nil && !reconciled {
		result.reconcile = fmt.Errorf("owned child backend termination incomplete")
	}
	cancel()
	closed := make(chan struct{})
	result.closeDone = closed
	go func() {
		observer.publish(fixture.DB.Pool())
		fixture.DB.Close()
		close(closed)
	}()
	termination, stop := context.WithDeadline(context.Background(), usageDiagnosticDeadline(deadline, 30*time.Second))
	terminated := make(chan error, 1)
	go func() { terminated <- fixture.Container.Terminate(termination) }()
	select {
	case result.termination = <-terminated:
		result.terminated = true
	case <-termination.Done():
		result.termination = termination.Err()
	}
	stop()
	join, stopJoin := context.WithDeadline(context.Background(), usageDiagnosticDeadline(deadline, 15*time.Second))
	defer stopJoin()
	select {
	case <-closed:
		result.closed = true
	case <-join.Done():
	}
	return result
}

func usageDiagnosticDeadline(overall time.Time, budget time.Duration) time.Time {
	deadline := time.Now().Add(budget)
	if overall.Before(deadline) {
		return overall
	}
	return deadline
}

func closeUsageDiagnosticChildPools(t *testing.T, ctx context.Context, pools []*pgxpool.Pool) bool {
	t.Helper()
	closes := make([]chan struct{}, 0, len(pools))
	for _, pool := range pools {
		done := make(chan struct{})
		closes = append(closes, done)
		go func() { pool.Close(); close(done) }()
	}
	join, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for _, done := range closes {
		select {
		case <-done:
		case <-join.Done():
			t.Error("child-pool-close-pending")
			return false
		}
	}
	return true
}
