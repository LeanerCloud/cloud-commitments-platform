package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/server/scheduledauth"
)

const (
	jwksModeFail  = 0
	jwksModeOK    = 1
	jwksModeBlock = 2
)

type jwksStub struct {
	*httptest.Server
	hits    atomic.Int64
	mode    atomic.Int32
	release chan struct{}
	once    sync.Once
}

func newJWKSStub(t *testing.T, mode int32) *jwksStub {
	t.Helper()
	s := &jwksStub{release: make(chan struct{})}
	s.mode.Store(mode)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		switch s.mode.Load() {
		case jwksModeFail:
			http.Error(w, "boom", http.StatusInternalServerError)
		case jwksModeBlock:
			select {
			case <-s.release:
			case <-r.Context().Done():
				return
			}
			fallthrough
		default:
			_, _ = w.Write([]byte(`{"keys":[]}`))
		}
	}))
	t.Cleanup(s.Close)
	t.Cleanup(func() { s.once.Do(func() { close(s.release) }) })
	return s
}

type healthClock struct{ ns atomic.Int64 }

func (c *healthClock) Now() time.Time { return time.Unix(0, c.ns.Load()).UTC() }

// oidcHealthApp wires an Application with a Warmup'd OIDC validator against
// stub; the clock lets tests age the stored probe result without sleeping.
func oidcHealthApp(t *testing.T, stub *jwksStub, warm bool) (*Application, *healthClock) {
	t.Helper()
	clock := &healthClock{}
	clock.ns.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	v, err := scheduledauth.New(scheduledauth.Config{
		Mode:      scheduledauth.ModeOIDC,
		Issuer:    "https://accounts.example.com",
		JWKSURL:   stub.URL + "/jwks",
		Audiences: []string{"https://api.example.com"},
		Subjects:  []string{"123456789012345678901"},
		Now:       clock.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if warm {
		v.Warmup(context.Background())
	}
	return &Application{
		Version:       "test",
		Config:        &mockConfigStoreForHealth{},
		Auth:          createHealthyAuthService(),
		scheduledAuth: v,
	}, clock
}

func getHealth(ctx context.Context, t *testing.T, h func(http.ResponseWriter, *http.Request), target string) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	res := httptest.NewRecorder()
	h(res, req)
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", res.Body.String(), err)
	}
	return res.Code, body, res.Body.String()
}

func jwksCheck(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	check, ok := body["checks"].(map[string]any)[checkScheduledAuthJWKS].(map[string]any)
	if !ok {
		t.Fatalf("missing %s check in %v", checkScheduledAuthJWKS, body)
	}
	return check
}

func TestHealthScheduledAuthJWKS_DisabledWhenNilAndBearer(t *testing.T) {
	t.Parallel()
	app := &Application{Version: "test", Config: &mockConfigStoreForHealth{}, Auth: createHealthyAuthService()}
	_, body, _ := getHealth(context.Background(), t, app.handleHealthCheck, "/health")
	if got := jwksCheck(t, body)["status"]; got != "disabled" {
		t.Fatalf("nil validator: want disabled, got %v", got)
	}
	app.scheduledAuth, _ = scheduledauth.New(scheduledauth.Config{Mode: scheduledauth.ModeBearer, Bearer: "x"})
	_, body, _ = getHealth(context.Background(), t, app.handleHealthCheck, "/health")
	if got := jwksCheck(t, body)["status"]; got != "disabled" {
		t.Fatalf("bearer validator: want disabled, got %v", got)
	}
}

func TestHealthScheduledAuthJWKS_Healthy(t *testing.T) {
	t.Parallel()
	app, _ := oidcHealthApp(t, newJWKSStub(t, jwksModeOK), true)
	_, body, _ := getHealth(context.Background(), t, app.handleHealthCheck, "/health")
	if got := jwksCheck(t, body)["status"]; got != "healthy" {
		t.Fatalf("want healthy, got %v", got)
	}
}

func TestHealthScheduledAuthJWKS_DegradedJSON(t *testing.T) {
	t.Parallel()
	stub := newJWKSStub(t, jwksModeFail)
	app, _ := oidcHealthApp(t, stub, true)
	code, body, raw := getHealth(context.Background(), t, app.handleHealthCheck, "/health")
	want := map[string]any{
		"status":  "degraded",
		"message": "Scheduled-task JWKS probe failed",
		"reason":  scheduledauth.JWKSReasonHTTPError,
	}
	got := jwksCheck(t, body)
	if code != http.StatusOK || len(got) != len(want) {
		t.Fatalf("code=%d check=%v", code, got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Fatalf("check[%s]=%v want %v", k, got[k], w)
		}
	}
	if strings.Contains(raw, strings.TrimPrefix(stub.URL, "http://")) {
		t.Fatalf("body leaks the JWKS host: %s", raw)
	}
}

func TestHealthScheduledAuthJWKS_DegradedKeepsTopLevelHealthy(t *testing.T) {
	t.Parallel()
	app, _ := oidcHealthApp(t, newJWKSStub(t, jwksModeFail), true)
	_, body, _ := getHealth(context.Background(), t, app.handleHealthCheck, "/health")
	if body["status"] != "healthy" {
		t.Fatalf("top-level status must stay healthy (GCP uptime check), got %v", body["status"])
	}
}

func TestHealthScheduledAuthJWKS_PendingKeepsTopLevelHealthy(t *testing.T) {
	t.Parallel()
	app, _ := oidcHealthApp(t, newJWKSStub(t, jwksModeOK), false)
	_, body, _ := getHealth(context.Background(), t, app.handleHealthCheck, "/health")
	if jwksCheck(t, body)["status"] != "pending" || body["status"] != "healthy" {
		t.Fatalf("pending check must not flip status: %v", body)
	}
}

func TestHealthPlainNeverProbes(t *testing.T) {
	t.Parallel()
	stub := newJWKSStub(t, jwksModeFail)
	app, clock := oidcHealthApp(t, stub, true)
	stub.mode.Store(jwksModeBlock)
	clock.ns.Add(int64(time.Hour))

	start := time.Now()
	for _, target := range []string{"/health", "/health?jwks=other"} {
		_, body, _ := getHealth(context.Background(), t, app.handleHealthCheck, target)
		if jwksCheck(t, body)["status"] != "degraded" {
			t.Fatalf("%s must report the stored degraded state", target)
		}
	}
	getHealth(context.Background(), t, app.handleReadinessCheck, "/ready")
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("plain reads must not wait: %v", el)
	}
	if hits := stub.hits.Load(); hits != 1 {
		t.Fatalf("plain reads must not probe: hits=%d", hits)
	}
}

func TestHealthRefreshQueryProbesAndWaitsBounded(t *testing.T) {
	t.Parallel()
	stub := newJWKSStub(t, jwksModeFail)
	app, clock := oidcHealthApp(t, stub, true)
	stub.mode.Store(jwksModeOK)
	clock.ns.Add(int64(11 * time.Second))
	_, body, _ := getHealth(context.Background(), t, app.handleHealthCheck, "/health?jwks=refresh")
	if jwksCheck(t, body)["status"] != "healthy" || stub.hits.Load() != 2 {
		t.Fatalf("refresh should recover in the same response: %v hits=%d", body, stub.hits.Load())
	}

	blocked := newJWKSStub(t, jwksModeFail)
	app, clock = oidcHealthApp(t, blocked, true)
	blocked.mode.Store(jwksModeBlock)
	clock.ns.Add(int64(11 * time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, body, _ = getHealth(ctx, t, app.handleHealthCheck, "/health?jwks=refresh")
	if el := time.Since(start); el > time.Second || jwksCheck(t, body)["status"] != "degraded" {
		t.Fatalf("blocked refresh must return degraded within the bound: %v %v", el, body)
	}
}

func TestReadyIgnoresScheduledAuthJWKS(t *testing.T) {
	t.Parallel()
	degraded, _ := oidcHealthApp(t, newJWKSStub(t, jwksModeFail), true)
	disabled := &Application{Version: "test", Config: &mockConfigStoreForHealth{}, Auth: createHealthyAuthService()}
	for name, app := range map[string]*Application{"degraded": degraded, "disabled": disabled} {
		code, body, _ := getHealth(context.Background(), t, app.handleReadinessCheck, "/ready")
		if code != http.StatusOK || jwksCheck(t, body)["status"] != name {
			t.Fatalf("%s: /ready code=%d body=%v", name, code, body)
		}
	}
}

func TestReadyStill503WhenJWKSDegradedAndStoreDown(t *testing.T) {
	t.Parallel()
	app, _ := oidcHealthApp(t, newJWKSStub(t, jwksModeFail), true)
	// No dbConfig: migrations is "disabled", so config_store alone is unhealthy.
	app.Config = nil
	code, body, _ := getHealth(context.Background(), t, app.handleReadinessCheck, "/ready")
	if code != http.StatusServiceUnavailable || jwksCheck(t, body)["status"] != "degraded" {
		t.Fatalf("exemption must be scoped: code=%d body=%v", code, body)
	}
}
