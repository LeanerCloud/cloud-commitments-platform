//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/require"
)

func TestReadinessProcess(t *testing.T) {
	if os.Getenv("READINESS_TEST_PROCESS") != "1" {
		return
	}
	config, err := database.LoadFromEnv()
	require.NoError(t, err)
	app, err := NewApplicationFromDeps(context.Background(), ApplicationConfig{
		DashboardURL: "https://readiness.example.test", DefaultTerm: 3, DefaultCoverage: 80,
	}, ExternalDeps{DBConfig: config, EmailSender: &noopEmailSender{}, SecretResolver: &mockSecretResolver{getResult: "local-fixture-password"}})
	require.NoError(t, err)
	defer app.Close()
	port, err := strconv.Atoi(os.Getenv("READINESS_TEST_PORT"))
	require.NoError(t, err)
	require.NoError(t, StartHTTPServer(app, port))
}

func TestHTTPProcessesInitializeWithoutAPITraffic(t *testing.T) {
	pg := readinessPostgres(t)
	ctx := context.Background()
	require.NoError(t, migrations.RunMigrations(ctx, pg.DB.Pool(), "../database/postgres/migrations", "", ""))
	config := *pg.Config
	config.AutoMigrate = true
	config.MigrationsPath = "../database/postgres/migrations"
	first, _, _ := readinessProcess(t, &config)
	second, _, _ := readinessProcess(t, &config)
	for _, url := range []string{first, second} {
		// Health itself never initializes; this fails on parent code before testing the new route.
		require.Eventually(t, func() bool {
			status, health := readinessResponse(url + "/health")
			return status == http.StatusOK && health.Status == "healthy"
		}, 20*time.Second, 25*time.Millisecond, "instance did not initialize without API traffic")
		status, health := readinessResponse(url + "/ready")
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, "healthy", health.Status)
		require.Equal(t, "healthy", health.Checks["migrations"].Status)
	}
}

func TestHTTPReadinessRejectsBlockedAndFailedRealMigrations(t *testing.T) {
	for _, scenario := range []string{"blocked recovery", "blocked shutdown", "second signal", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			failure := scenario == "failed"
			pg := readinessPostgres(t)
			ctx := context.Background()
			migrationPath := t.TempDir()
			statement := "SELECT pg_advisory_lock(488488); SELECT pg_advisory_unlock(488488);"
			if failure {
				statement = "THIS IS NOT VALID SQL;"
			}
			require.NoError(t, os.WriteFile(filepath.Join(migrationPath, "000001_readiness.up.sql"), []byte(statement), 0o600))
			config := *pg.Config
			config.AutoMigrate = true
			config.MigrationsPath = migrationPath
			lock, err := pg.DB.Pool().Acquire(ctx)
			require.NoError(t, err)
			defer lock.Release()
			_, err = lock.Exec(ctx, "SELECT pg_advisory_lock(488488)")
			require.NoError(t, err)
			defer func() { _, _ = lock.Exec(ctx, "SELECT pg_advisory_unlock(488488)") }()
			url, stop, process := readinessProcess(t, &config)
			if !failure {
				require.Eventually(t, func() bool {
					var blocked bool
					queryErr := pg.DB.Pool().QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname = $1 AND wait_event_type = 'Lock' AND query LIKE '%pg_advisory_lock(488488)%')", config.Database).Scan(&blocked)
					return queryErr == nil && blocked
				}, 20*time.Second, 25*time.Millisecond, "real migration did not reach the held lock")
				requests := make(chan error, 2)
				for _, path := range []string{"/api/auth/check-admin", "/api/scheduled/cleanup"} {
					go func() {
						requestCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
						defer cancel()
						request, requestErr := http.NewRequestWithContext(requestCtx, http.MethodPost, url+path, nil)
						if requestErr == nil {
							var response *http.Response
							response, requestErr = http.DefaultClient.Do(request)
							if response != nil {
								_ = response.Body.Close()
							}
						}
						requests <- requestErr
					}()
				}
				for range 2 {
					require.ErrorIs(t, <-requests, context.DeadlineExceeded)
				}
			}
			want := "pending"
			if failure {
				want = "failed"
			}
			require.Eventually(t, func() bool {
				status, health := readinessResponse(url + "/ready")
				if failure && (health.Checks["config_store"].Status != "healthy" || health.Checks["auth_store"].Status != "healthy") {
					return false
				}
				return status == http.StatusServiceUnavailable && health.Checks["migrations"].Status == want
			}, 20*time.Second, 25*time.Millisecond)
			status, _ := readinessResponse(url + "/health")
			require.Equal(t, http.StatusOK, status)
			if scenario == "second signal" {
				require.NoError(t, process.Signal(syscall.SIGTERM))
				require.Eventually(t, func() bool {
					status, _ := readinessResponse(url + "/health")
					return status == 0
				}, time.Second, 10*time.Millisecond, "first signal did not close the listener")
				stopErr := stop()
				require.ErrorContains(t, stopErr, "signal: terminated")
				return
			}
			if scenario == "blocked shutdown" {
				stopped := make(chan error, 1)
				go func() { stopped <- stop() }()
				select {
				case stopErr := <-stopped:
					t.Fatalf("startup returned before blocked migration was released: %v", stopErr)
				case <-time.After(100 * time.Millisecond):
				}
				_, err = lock.Exec(ctx, "SELECT pg_advisory_unlock(488488)")
				require.NoError(t, err)
				require.NoError(t, <-stopped)
				return
			}
			if !failure {
				_, err = lock.Exec(ctx, "SELECT pg_advisory_unlock(488488)")
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					status, health := readinessResponse(url + "/ready")
					return status == http.StatusOK && health.Status == "healthy"
				}, 20*time.Second, 25*time.Millisecond)
			}
			require.NoError(t, stop())
		})
	}
}

func readinessPostgres(t *testing.T) *testhelpers.PostgresContainer {
	t.Helper()
	for _, key := range []string{
		"CREDENTIAL_ENCRYPTION_KEY_SECRET_ARN", "CREDENTIAL_ENCRYPTION_KEY_SECRET_NAME",
		"CREDENTIAL_ENCRYPTION_KEY_SECRET_ID", "CREDENTIAL_ENCRYPTION_ALLOW_DEV_KEY",
		"CUDLY_SIGNING_KEY_ID", "CUDLY_SIGNING_KEY_VAULT_URL", "CUDLY_SIGNING_KEY_NAME",
		"CUDLY_SIGNING_KEY_RESOURCE", "AWS_PROFILE", "AWS_LAMBDA_RUNTIME_API", "ADMIN_EMAIL", "API_KEY_SECRET_ARN",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("CREDENTIAL_ENCRYPTION_KEY", strings.Repeat("12", 32))
	t.Setenv("CUDLY_ISSUER_URL", "https://readiness.example.test")
	t.Setenv("CUDLY_SOURCE_CLOUD", "aws")
	t.Setenv("SCHEDULED_TASK_AUTH_MODE", "disabled")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_ACCESS_KEY_ID", "local-readiness-test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "local-readiness-test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("ADMIN_PASSWORD_SECRET", "local-readiness-test")
	pg, err := testhelpers.SetupPostgresContainer(context.Background(), t)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Cleanup(context.Background())) })
	return pg
}

func readinessProcess(t *testing.T, config *database.Config) (string, func() error, *os.Process) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	binary, err := os.Executable()
	require.NoError(t, err)
	command := exec.Command(binary, "-test.run=^TestReadinessProcess$", "-test.v")
	// Make premature exit observable without the race detector's process-exit delay.
	command.Env = append(os.Environ(),
		"GORACE=atexit_sleep_ms=0",
		"READINESS_TEST_PROCESS=1", fmt.Sprintf("READINESS_TEST_PORT=%d", port),
		"DB_HOST="+config.Host, fmt.Sprintf("DB_PORT=%d", config.Port), "DB_NAME="+config.Database,
		"DB_USER="+config.User, "DB_PASSWORD="+config.Password, "DB_PASSWORD_SECRET=", "DB_SSL_MODE=disable",
		fmt.Sprintf("DB_AUTO_MIGRATE=%t", config.AutoMigrate), "DB_MIGRATIONS_PATH="+config.MigrationsPath,
	)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	require.NoError(t, command.Start())
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	var once sync.Once
	stop := func() error {
		var stopErr error
		once.Do(func() {
			_ = command.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-done:
				if err != nil {
					stopErr = fmt.Errorf("readiness child: %w: %s", err, output.String())
				}
			case <-time.After(10 * time.Second):
				_ = command.Process.Kill()
				<-done
				stopErr = fmt.Errorf("readiness child did not exit: %s", output.String())
			}
		})
		return stopErr
	}
	t.Cleanup(func() { require.NoError(t, stop()) })
	return fmt.Sprintf("http://127.0.0.1:%d", port), stop, command.Process
}

func readinessResponse(url string) (int, HealthStatus) {
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(url)
	if err != nil {
		return 0, HealthStatus{}
	}
	defer response.Body.Close()
	var health HealthStatus
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		return response.StatusCode, HealthStatus{}
	}
	return response.StatusCode, health
}
