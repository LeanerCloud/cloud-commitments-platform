package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/oidc"
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

func TestHTTPStartupFailureDoesNotRetryLazyInitialization(t *testing.T) {
	want := errors.New("startup failed")
	app := &Application{
		dbConfig: &database.Config{}, dbErr: errors.New("unexpected retry"),
		httpInit: &httpInitialization{done: make(chan struct{}), err: want},
	}
	close(app.httpInit.done)
	require.ErrorIs(t, app.ensureHTTPDB(context.Background()), want)
}

func TestHTTPStartupReturnsPermanentSecretFailure(t *testing.T) {
	want := errors.New("fixture secret is unavailable")
	app := &Application{
		dbConfig:       &database.Config{PasswordSecret: "fixture"},
		secretResolver: &mockSecretResolver{getErr: want},
	}
	require.ErrorIs(t, StartHTTPServer(app, 0), want)
	require.ErrorIs(t, app.ensureHTTPDB(context.Background()), want)
	response := httptest.NewRecorder()
	app.handleReadinessCheck(response, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
}

func TestConcurrentHealthyProbesDoNotBecomePending(t *testing.T) {
	app := &Application{
		Config: &mockConfigStoreForHealth{}, Auth: createHealthyAuthService(),
		httpInit: &httpInitialization{done: make(chan struct{})},
	}
	close(app.httpInit.done)
	handler := CreateHTTPServer(app, 0).Handler
	app.dbMu.Lock()
	defer app.dbMu.Unlock()
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			for _, path := range []string{"/health", "/ready"} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				if response.Code != http.StatusOK {
					t.Errorf("%s status = %d: %s", path, response.Code, response.Body.String())
				}
			}
		})
	}
	workers.Wait()
}

func TestLazyHealthDoesNotWaitForInitialization(t *testing.T) {
	app := &Application{dbConfig: &database.Config{}}
	app.dbMu.Lock()
	defer app.dbMu.Unlock()
	response := httptest.NewRecorder()
	app.handleHealthCheck(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"status":"degraded"`)
}

func TestPublicEndpointsUsePublishedAPI(t *testing.T) {
	signer, err := oidc.NewLocalSigner()
	require.NoError(t, err)
	t.Setenv("VERSION", "readiness-fixture")
	config := api.HandlerConfig{OIDCSigner: signer, OIDCIssuerURL: "https://published.example.test"}
	app := &Application{API: api.NewHandler(config)}
	handler := CreateHTTPServer(app, 0).Handler
	var workers sync.WaitGroup
	workers.Go(func() {
		for range 20 {
			app.apiMu.Lock()
			app.API = api.NewHandler(config)
			app.apiMu.Unlock()
		}
	})
	for range 20 {
		for _, path := range []string{"/version", "/oidc/.well-known/openid-configuration"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, "application/json", response.Header().Get("Content-Type"))
			require.Equal(t, http.StatusOK, response.Code)
			if path == "/version" {
				require.Contains(t, response.Body.String(), "readiness-fixture")
			} else {
				require.Contains(t, response.Body.String(), "https://published.example.test/oidc")
			}
		}
	}
	workers.Wait()
	config.OIDCIssuerURL = "https://reinitialized.example.test"
	app.apiMu.Lock()
	app.API = api.NewHandler(config)
	app.apiMu.Unlock()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/oidc/.well-known/openid-configuration", nil))
	require.Contains(t, response.Body.String(), "https://reinitialized.example.test/oidc")
}

type slowHealthStore struct {
	mockAuthStoreForHealth
	entered chan struct{}
	release chan struct{}
}

func (store *slowHealthStore) Ping(ctx context.Context) error {
	close(store.entered)
	select {
	case <-store.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSlowHealthPingDoesNotBlockAPIAdmission(t *testing.T) {
	store := &slowHealthStore{entered: make(chan struct{}), release: make(chan struct{})}
	app := &Application{
		Config: &mockConfigStoreForHealth{}, Auth: auth.NewService(auth.ServiceConfig{Store: store}),
		API:      api.NewHandler(api.HandlerConfig{}),
		dbConfig: &database.Config{}, dbConnected: true,
	}
	handler := CreateHTTPServer(app, 0).Handler
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	}()
	defer func() { close(store.release); <-done }()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("health did not enter the slow ping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/not-a-route", nil))
	require.NoError(t, ctx.Err(), "API admission waited for health ping")
	require.NotEqual(t, http.StatusServiceUnavailable, response.Code)
}
