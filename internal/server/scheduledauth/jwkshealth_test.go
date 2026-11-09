package scheduledauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is installed once before any probe; only the atomic advances.
type fakeClock struct{ ns atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.ns.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *fakeClock) Now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *fakeClock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

const (
	stepOK      = "ok"
	step404     = "404"
	step500     = "500"
	stepEmpty   = "empty"   // 200 {} (no keys array)
	stepGarbage = "garbage" // 200 not json
	stepBlock   = "block"   // waits for release, then answers ok
)

type seqJWKS struct {
	*httptest.Server
	hits    atomic.Int64
	release chan struct{}
	once    sync.Once
}

func (s *seqJWKS) Release() { s.once.Do(func() { close(s.release) }) }

func sequencedJWKS(t *testing.T, steps ...string) *seqJWKS {
	t.Helper()
	s := &seqJWKS{release: make(chan struct{})}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(s.hits.Add(1)) - 1
		step := steps[len(steps)-1]
		if n < len(steps) {
			step = steps[n]
		}
		switch step {
		case step404:
			http.NotFound(w, r)
		case step500:
			http.Error(w, "boom", http.StatusInternalServerError)
		case stepEmpty, stepGarbage:
			_, _ = w.Write([]byte(map[string]string{stepEmpty: `{}`, stepGarbage: `not json`}[step]))
		default:
			if step == stepBlock {
				select {
				case <-s.release:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = w.Write([]byte(`{"keys":[]}`))
		}
	}))
	t.Cleanup(s.Close)
	t.Cleanup(s.Release) // runs before Close so blocked handlers drain
	return s
}

func waitIdle(t *testing.T, v *Validator) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v.jwksMu.Lock()
		idle := v.jwksInflight == nil
		v.jwksMu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("probe still in flight after 3s")
}

func clockedValidator(t *testing.T, url string) (*Validator, *fakeClock) {
	t.Helper()
	v := newOIDCValidator(t, url)
	c := newFakeClock()
	v.now = c.Now
	return v, c
}

func TestJWKSHealth_NotOIDC_Disabled(t *testing.T) {
	v, err := New(Config{Mode: ModeBearer, Bearer: "x"})
	if err != nil || v.JWKSHealth(context.Background(), true).Enabled {
		t.Fatalf("bearer mode must report disabled (err=%v)", err)
	}
}

func TestJWKSHealth_BeforeWarmup_Pending(t *testing.T) {
	got := newOIDCValidator(t, "http://127.0.0.1:1/x").JWKSHealth(context.Background(), false)
	if !got.Enabled || !got.Pending || got.OK {
		t.Fatalf("expected enabled+pending, got %+v", got)
	}
}

func TestWarmup_RecordsDegradedReasons(t *testing.T) {
	cases := []struct {
		name string
		step string // empty means closed port
		want string
	}{
		{"closed port", "", JWKSReasonFetchFailed},
		{"404", step404, JWKSReasonHTTPError},
		{"500", step500, JWKSReasonHTTPError},
		{"no keys array", stepEmpty, JWKSReasonInvalidPayload},
		{"not json", stepGarbage, JWKSReasonInvalidPayload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := "http://127.0.0.1:1/closed"
			if tc.step != "" {
				url = sequencedJWKS(t, tc.step).URL
			}
			v, _ := clockedValidator(t, url)
			v.Warmup(context.Background())
			got := v.JWKSHealth(context.Background(), false)
			if got.OK || got.Reason != tc.want {
				t.Fatalf("want degraded %q, got %+v", tc.want, got)
			}
			if strings.Contains(got.Reason, "://") || strings.Contains(got.Reason, "127.0.0.1") {
				t.Fatalf("reason leaks the URL: %q", got.Reason)
			}
		})
	}
}

func TestJWKSHealth_NoRefreshNeverProbesOrWaits(t *testing.T) {
	srv := sequencedJWKS(t, step500, stepBlock)
	v, clock := clockedValidator(t, srv.URL)
	v.Warmup(context.Background())
	clock.Advance(time.Hour)

	start := time.Now()
	got := v.JWKSHealth(context.Background(), false)
	if el := time.Since(start); el > 100*time.Millisecond {
		t.Fatalf("plain read must not wait: %v", el)
	}
	if got.OK || got.Reason != JWKSReasonHTTPError {
		t.Fatalf("expected stored degraded state, got %+v", got)
	}
	if hits := srv.hits.Load(); hits != 1 {
		t.Fatalf("plain read probed: hits=%d", hits)
	}
	waitIdle(t, v) // fails if a probe goroutine was started
}

func TestWarmup_RecordsHealthy_NeverReprobed(t *testing.T) {
	srv := sequencedJWKS(t, stepOK)
	v, clock := clockedValidator(t, srv.URL)
	v.Warmup(context.Background())
	if got := v.JWKSHealth(context.Background(), false); !got.Enabled || got.Pending || !got.OK || got.Reason != "" {
		t.Fatalf("expected healthy, got %+v", got)
	}
	clock.Advance(time.Hour)
	if got := v.JWKSHealth(context.Background(), true); !got.OK || srv.hits.Load() != 1 {
		t.Fatalf("healthy must be terminal: %+v hits=%d", got, srv.hits.Load())
	}
}
