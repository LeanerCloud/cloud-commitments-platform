package scheduledauth

import (
	"context"
	"log"
	"net/http"
	"time"
)

// Stable, URL-free reason codes for a failed JWKS probe. They are safe to
// expose on the unauthenticated /health endpoint.
const (
	JWKSReasonFetchFailed    = "jwks_fetch_failed"
	JWKSReasonHTTPError      = "jwks_http_error"
	JWKSReasonInvalidPayload = "jwks_invalid_payload"
)

// jwksReprobeInterval caps outbound re-probes per instance while degraded.
const jwksReprobeInterval = 10 * time.Second

// jwksRefreshWait bounds how long a refreshing JWKSHealth call waits for the
// probe. A var so tests can shorten it.
var jwksRefreshWait = 2 * time.Second

// jwksProbeResult is immutable and swapped atomically.
type jwksProbeResult struct {
	ok     bool
	reason string
	at     time.Time
}

// JWKSHealth is the outcome of the JWKS endpoint probe (platform#67).
type JWKSHealth struct {
	Enabled bool
	Pending bool // Warmup has not finished a probe yet
	OK      bool
	Reason  string // a JWKSReason* code when !OK
}

// probeJWKS fetches and validates the JWKS document. It returns "" on
// success, else a JWKSReason* code. Details go to the server log only.
// The shared hardened client also feeds the kid guard observer; a successful
// re-probe may advance its latest fetch, which is fail-closed and only
// happens while degraded.
func (v *Validator) probeJWKS(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, http.NoBody)
	if err != nil {
		log.Printf("scheduledauth: WARN — JWKS warmup request build failed: %v", err)
		return JWKSReasonFetchFailed
	}
	resp, err := v.httpClient.Do(req) // #nosec G704 -- jwksURL is operator-supplied config (SCHEDULED_TASK_OIDC_JWKS_URL), not request-tainted user input; v.httpClient is the hardened client from pkg/httpclient that blocks IMDS/link-local endpoints and enforces dial/TLS/overall timeouts
	if err != nil {
		log.Printf("scheduledauth: WARN — JWKS warmup fetch failed for %s: %v "+
			"(validator will retry on first request)", v.jwksURL, err)
		return JWKSReasonFetchFailed
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		log.Printf("scheduledauth: WARN — JWKS warmup got %d from %s "+
			"(validator will retry on first request)", resp.StatusCode, v.jwksURL)
		return JWKSReasonHTTPError
	}
	if err := validateJWKSBody(resp.Body); err != nil {
		log.Printf("scheduledauth: WARN — JWKS warmup got invalid JWKS payload from %s: %v "+
			"(validator will retry on first request)", v.jwksURL, err)
		return JWKSReasonInvalidPayload
	}
	return ""
}

func (v *Validator) storeJWKSResult(reason string) {
	v.jwksResult.Store(&jwksProbeResult{ok: reason == "", reason: reason, at: v.now()})
}

func snapshotJWKS(r *jwksProbeResult) JWKSHealth {
	if r == nil {
		return JWKSHealth{Enabled: true, Pending: true}
	}
	return JWKSHealth{Enabled: true, OK: r.ok, Reason: r.reason}
}

// JWKSHealth reports the last JWKS probe outcome. With refresh=false it is a
// pure atomic read: it never probes or waits, so liveness, readiness and
// uptime probes stay cheap. With refresh=true and a degraded state it starts
// at most one probe (at most one per jwksReprobeInterval) and waits up to
// jwksRefreshWait for it, because Cloud Run throttles CPU between requests
// and a background retry would starve. Healthy is terminal.
func (v *Validator) JWKSHealth(ctx context.Context, refresh bool) JWKSHealth {
	if v.mode != ModeOIDC || v.jwksURL == "" {
		return JWKSHealth{}
	}
	if !refresh {
		return snapshotJWKS(v.jwksResult.Load())
	}

	done := v.startRefreshLocked()
	if done != nil {
		v.waitForProbe(ctx, done)
	}
	return snapshotJWKS(v.jwksResult.Load())
}

// startRefreshLocked decides under jwksMu whether a probe is needed, starts at
// most one, and returns the in-flight channel to wait on (nil: nothing to wait
// for). The result is loaded after taking the lock, so the age check is a
// double-check.
func (v *Validator) startRefreshLocked() chan struct{} {
	v.jwksMu.Lock()
	defer v.jwksMu.Unlock()
	r := v.jwksResult.Load()
	if r == nil || r.ok {
		return nil
	}
	if v.jwksInflight == nil && v.now().Sub(r.at) >= jwksReprobeInterval {
		v.jwksInflight = make(chan struct{})
		go v.runProbe(v.jwksInflight)
	}
	return v.jwksInflight
}

func (v *Validator) waitForProbe(ctx context.Context, done chan struct{}) {
	wait := v.jwksWait
	if wait == 0 {
		wait = jwksRefreshWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-ctx.Done():
	}
}

// runProbe outlives the caller's bounded wait (5s cap) so a slow success
// still lands for the next read.
func (v *Validator) runProbe(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), warmupTimeout)
	defer cancel()
	reason := v.probeJWKS(ctx)
	v.storeJWKSResult(reason)
	if reason == "" {
		log.Printf("scheduledauth: JWKS probe recovered")
	}
	v.jwksMu.Lock()
	v.jwksInflight = nil
	close(done)
	v.jwksMu.Unlock()
}
