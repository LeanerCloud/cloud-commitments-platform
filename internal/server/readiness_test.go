package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/stretchr/testify/require"
)

type startupSecretResolver struct {
	mockSecretResolver
	entered chan struct{}
	exited  chan struct{}
}

func (resolver *startupSecretResolver) GetSecret(ctx context.Context, _ string) (string, error) {
	close(resolver.entered)
	defer close(resolver.exited)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestHTTPStartupJoinsCancelableInitialization(t *testing.T) {
	for _, bindFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("bind failure=%t", bindFailure), func(t *testing.T) {
			resolver := &startupSecretResolver{entered: make(chan struct{}), exited: make(chan struct{})}
			app := &Application{dbConfig: &database.Config{PasswordSecret: "fixture"}, secretResolver: resolver}
			port := 0
			if bindFailure {
				listener, err := net.Listen("tcp", ":0")
				require.NoError(t, err)
				defer listener.Close()
				port = listener.Addr().(*net.TCPAddr).Port
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- startHTTPServer(ctx, app, port) }()
			select {
			case <-resolver.entered:
			case <-time.After(time.Second):
				t.Fatal("startup did not initialize independently of requests")
			}
			if !bindFailure {
				cancel()
			}
			select {
			case err := <-done:
				if bindFailure {
					require.ErrorContains(t, err, "bind")
				} else {
					require.NoError(t, err)
				}
			case <-time.After(time.Second):
				t.Fatal("startup did not join canceled secret resolution")
			}
			select {
			case <-resolver.exited:
			default:
				t.Fatal("startup returned before initializer exited")
			}
		})
	}
}

func TestReadinessRequiresCompletedInitializationAndHealthyMigrations(t *testing.T) {
	for _, state := range []string{"pending", "failed", "migration pending", "migration failed", "healthy"} {
		t.Run(state, func(t *testing.T) {
			app := &Application{
				Config: &mockConfigStoreForHealth{}, Auth: createHealthyAuthService(),
				dbConfig: &database.Config{AutoMigrate: true}, dbConnected: true,
				httpInit: &httpInitialization{done: make(chan struct{})},
			}
			if state != "pending" {
				if state == "failed" {
					app.httpInit.err = errors.New("initialization failed")
				}
				close(app.httpInit.done)
			}
			switch state {
			case "migration failed":
				app.recordMigrationResult(errors.New("invalid migration"))
			case "healthy":
				app.recordMigrationResult(nil)
			}
			handler := CreateHTTPServer(app, 0).Handler
			for _, path := range []string{"/health", "/ready"} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				want := http.StatusOK
				if path == "/ready" && state != "healthy" {
					want = http.StatusServiceUnavailable
				}
				require.Equal(t, want, response.Code)
				var health HealthStatus
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &health))
				if state == "healthy" {
					require.Equal(t, "healthy", health.Status)
				} else {
					require.Equal(t, "degraded", health.Status)
				}
			}
		})
	}
}

func TestHTTPRequestsCannotInitializeAheadOfStartup(t *testing.T) {
	for _, path := range []string{"/api/auth/check-admin", "/api/scheduled/cleanup"} {
		t.Run(path, func(t *testing.T) {
			app := &Application{
				dbConfig: &database.Config{}, dbErr: errors.New("lazy initialization must not run"),
				httpInit: &httpInitialization{done: make(chan struct{})},
			}
			ctx, cancel := context.WithCancel(context.Background())
			request := httptest.NewRequestWithContext(ctx, http.MethodPost, path, nil)
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				CreateHTTPServer(app, 0).Handler.ServeHTTP(response, request)
				close(done)
			}()
			select {
			case <-done:
				t.Fatal("request bypassed the startup barrier")
			case <-time.After(20 * time.Millisecond):
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("canceled request remained blocked")
			}
			require.Equal(t, http.StatusServiceUnavailable, response.Code)
			select {
			case <-app.httpInit.done:
				t.Fatal("request cancellation completed startup")
			default:
			}
		})
	}
}
