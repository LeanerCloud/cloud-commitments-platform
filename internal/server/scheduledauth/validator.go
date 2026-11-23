package scheduledauth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Mode is the authentication mode used for scheduled-task requests.
type Mode string

const (
	// ModeOIDC verifies a Google-issued OIDC ID token (RS256 only) on
	// the Authorization header. Subject pinning is required.
	ModeOIDC Mode = "oidc"

	// ModeBearer compares the Authorization header to a shared secret
	// using crypto/subtle.ConstantTimeCompare. Used on Azure where Logic
	// Apps fetch the secret from Key Vault at runtime.
	ModeBearer Mode = "bearer"

	// ModeDisabled disables auth entirely — every request is allowed
	// through. Intended for local development only. The validator logs
	// a WARN at startup and on every request to make this loud.
	ModeDisabled Mode = "disabled"
)

// DefaultClockSkew is the tolerance applied to exp/nbf checks. Matches
// the leeway most OIDC providers (Microsoft, Auth0) recommend.
const DefaultClockSkew = 60 * time.Second

// GoogleIssuer is the canonical OIDC issuer URL for Google-signed ID
// tokens (Cloud Scheduler, Cloud Functions, etc.).
const GoogleIssuer = "https://accounts.google.com"

// GoogleJWKSURL is Google's JWKS endpoint. Used as the default when the
// caller does not override it via SCHEDULED_TASK_OIDC_JWKS_URL.
const GoogleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

// Config holds the parsed configuration for a Validator. NewValidator
// applies defaults and rejects invalid combinations.
type Config struct {
	Now       func() time.Time // injectable clock; nil means time.Now
	Mode      Mode
	Issuer    string
	JWKSURL   string
	Bearer    string
	Audiences []string
	Subjects  []string
	Skew      time.Duration
}

// Validator authenticates inbound requests against the configured mode.
type Validator struct {
	lastRebuild  time.Time // last verifier rebuild; rate-limits attacker-driven JWKS refetches
	audiences    map[string]struct{}
	now          func() time.Time                // pluggable for tests
	verifier     *oidc.IDTokenVerifier           // nil unless mode == ModeOIDC; backed by go-oidc's single-flight RemoteKeySet
	kids         *kidGuard                       // nil unless mode == ModeOIDC; negative kid cache shared across rebuilds (issue #75)
	jwksInflight chan struct{}                   // non-nil while a probe goroutine runs; closed on completion
	jwksResult   atomic.Pointer[jwksProbeResult] // nil until the first probe finished
	subjects     map[string]struct{}
	httpClient   *http.Client // hardened client for all JWKS fetches; nil unless mode == ModeOIDC
	jwksURL      string       // remembered for Warmup and rotation rebuild; empty unless mode == ModeOIDC
	issuer       string       // OIDC issuer URL; empty unless mode == ModeOIDC; needed to rebuild verifier
	mode         Mode
	bearer       []byte
	skew         time.Duration
	jwksWait     time.Duration // test override of jwksRefreshWait; 0 means the default
	verMu        sync.RWMutex  // guards verifier and lastRebuild during rotation-recovery rebuilds
	jwksMu       sync.Mutex    // guards jwksInflight and the re-probe trigger decision
}

// claims captures the timestamp claims we need beyond what go-oidc's
// IDToken exposes directly. Issuer/Subject/Audience are read off the
// IDToken value returned by Verify; we only re-parse the payload to
// pick up exp/iat/nbf as int64 timestamps so we can apply our own
// skew-tolerant checks (go-oidc's built-in expiry check has no skew).
type claims struct {
	ExpiresAt int64 `json:"exp"`
	IssuedAt  int64 `json:"iat"`
	NotBefore int64 `json:"nbf"`
}

// Mode returns the validator's auth mode. Useful for /health.
func (v *Validator) Mode() Mode {
	return v.mode
}

// warmupTimeout is the fallback deadline for the JWKS warmup probe
// when the caller passes a context without one. The hardened client
// has its own overall timeout, but startup should fail the probe much
// faster than that when the JWKS endpoint is misconfigured or
// unreachable.
const warmupTimeout = 5 * time.Second

// Warmup performs a best-effort sanity check on the JWKS endpoint at
// startup. Fail-open for serving by design (a Lambda cold-start init budget
// cannot host a retry loop; a bad JWKS only affects /api/scheduled/*). The
// outcome is exposed via JWKSHealth and shown as the scheduled_auth_jwks
// check on /health; the deploy gate fails a persistent degraded state.
// Healthy is terminal. platform#67, option A+.
//
// If ctx has no deadline, a 5s fallback is applied — startup must
// always make forward progress, even if the caller forgot to bound the
// warmup. Callers that DO want to wait longer are respected.
//
// Implementation note: go-oidc's RemoteKeySet does not expose a public
// "fetch now" method — it only fetches on demand from VerifySignature
// (which requires a parseable JWT). We issue an independent HTTP GET
// to the JWKS URL to keep the warmup probe lightweight and reusable.
func (v *Validator) Warmup(ctx context.Context) {
	if v.mode != ModeOIDC || v.jwksURL == "" {
		return
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, warmupTimeout)
		defer cancel()
	}
	v.storeJWKSResult(v.probeJWKS(ctx))
}

func validateJWKSBody(r io.Reader) error {
	var doc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return err
	}
	if doc.Keys == nil {
		return errors.New(`missing "keys" array`)
	}
	return nil
}

// Middleware wraps next with auth enforcement. On failure, writes a 401
// with no body (avoiding any implementation-detail leakage) and logs the
// reason. Successful requests pass through unmodified.
func (v *Validator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v.mode == ModeDisabled {
			logging.Warnf("scheduledauth: WARN — request to %s allowed without auth (mode=disabled)", r.URL.Path) // #nosec G706 -- URL path logged only when auth is disabled (operator-set mode); informational audit log
			next.ServeHTTP(w, r)
			return
		}

		authz := r.Header.Get("Authorization")
		if err := v.Validate(r.Context(), authz); err != nil {
			logging.Infof("scheduledauth: rejected %s %s: %q", r.Method, r.URL.Path, err.Error()) // #nosec G706 -- HTTP method and path logged for auth audit; Go net/http normalizes paths before handler dispatch; %q quotes the error string to prevent log injection from any token-derived values
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Validate is the unit-testable core. It returns nil iff the
// Authorization header satisfies the configured mode's requirements.
func (v *Validator) Validate(ctx context.Context, authz string) error {
	switch v.mode {
	case ModeDisabled:
		return nil
	case ModeBearer:
		return v.validateBearer(authz)
	case ModeOIDC:
		return v.validateOIDC(ctx, authz)
	default:
		// Should be impossible — New rejects unknown modes.
		return fmt.Errorf("%w: misconfigured validator", ErrUnauthorized)
	}
}

func (v *Validator) validateBearer(authz string) error {
	if authz == "" {
		return fmt.Errorf("%w: missing Authorization header", ErrUnauthorized)
	}
	if subtle.ConstantTimeCompare([]byte(authz), v.bearer) != 1 {
		return fmt.Errorf("%w: bearer secret mismatch", ErrUnauthorized)
	}
	return nil
}

func (v *Validator) validateOIDC(ctx context.Context, authz string) error {
	rawToken, err := extractBearerToken(authz)
	if err != nil {
		return err
	}

	// Verify signature, issuer, and algorithm via go-oidc. SkipClientIDCheck
	// and SkipExpiryCheck are enabled because we apply our own multi-aud
	// and skew-tolerant expiry checks below.
	//
	// verifyWithRotationRetry handles the go-oidc stale-inflight race: during
	// key rotation, go-oidc's RemoteKeySet may cache a completed but
	// not-yet-cleaned-up inflight request. A second goroutine joining that
	// inflight receives pre-rotation keys, causing a spurious signature
	// failure. The retry detects this class of error and rebuilds the
	// RemoteKeySet from scratch (no stale inflight), then retries once.
	idToken, err := v.verifyWithRotationRetry(ctx, rawToken)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnauthorized, err)
	}

	// Re-parse the payload to access iat / nbf — IDToken exposes Expiry
	// and IssuedAt but go-oidc's expiry check has no skew tolerance, and
	// nbf is not exposed as a typed field.
	var c claims
	if err := idToken.Claims(&c); err != nil {
		return fmt.Errorf("%w: malformed claims: %w", ErrUnauthorized, err)
	}

	if err := v.checkTimestamps(c); err != nil {
		return err
	}

	// Audience: at least one of the token's aud values must be in our
	// allow-list. The token typically has exactly one audience but the
	// spec permits multiple.
	if !audienceMatches(idToken.Audience, v.audiences) {
		return fmt.Errorf("%w: audience %q not in allowlist", ErrUnauthorized, idToken.Audience)
	}

	// Subject pinning: required defense-in-depth — any GCP SA in the
	// org could mint a token with our `aud` value, but only the
	// scheduler SA has our `sub`.
	if _, ok := v.subjects[idToken.Subject]; !ok {
		return fmt.Errorf("%w: subject %q not in allowlist", ErrUnauthorized, idToken.Subject)
	}

	logging.Infof("scheduledauth: oidc token accepted (sub=%q, aud=%q)", idToken.Subject, idToken.Audience) // #nosec G706 -- both sub and aud are quoted (%q) to prevent log injection; sub is additionally validated against the allowlist
	return nil
}

// minRebuildInterval rate-limits verifier rebuilds. Without it, every
// bad-signature token would trigger a keyset rebuild plus an outbound JWKS
// GET on the retry, letting an unauthenticated attacker amplify garbage
// tokens into requests against the provider's JWKS endpoint. 10s bounds
// attacker-driven fetches to ~6/min while a genuine key rotation still
// recovers within one interval (providers publish the new key well before
// signing with it, so a >10s outage window here is not a realistic
// rotation pattern). go-oidc's own refresh-on-unknown-kid would still run
// on every request regardless of this limit; the negative kid cache in
// kidguard.go bounds that to one fetch per kid per negativeKidTTL.
const minRebuildInterval = 10 * time.Second

// verifyWithRotationRetry calls Verify on the current verifier. If the call
// fails with a signature error (unknown kid after key rotation, or stale
// inflight keyset) it rebuilds the RemoteKeySet and IDTokenVerifier from
// scratch to bypass go-oidc's cached inflight, then retries exactly once.
//
// Fail-closed constraints:
//   - Retry only on signature errors (error contains "failed to verify signature").
//     aud/iss/exp/nbf/sub failures are returned immediately without retry.
//   - At most one rebuild per rotation event: if another goroutine has already
//     rebuilt the verifier (pointer changed under the write lock), we skip the
//     rebuild and retry with the new verifier directly.
//   - Rebuilds are rate-limited to one per minRebuildInterval; within the
//     window the ORIGINAL signature error is returned without a retry
//     (fail closed), preventing bad-token spam from amplifying into
//     unbounded JWKS fetches.
//   - No sleep; the retry itself re-fetching fresh keys is the synchronization.
//   - Rebuild is logged once per rotation (not per failing request).
func (v *Validator) verifyWithRotationRetry(ctx context.Context, rawToken string) (*oidc.IDToken, error) {
	v.verMu.RLock()
	ver := v.verifier
	v.verMu.RUnlock()

	idToken, err := ver.Verify(ctx, rawToken)
	if err == nil || !isSignatureError(err) {
		return idToken, err
	}

	// Signature failure - possibly a stale inflight keyset from go-oidc's
	// single-flight cache. Rebuild the RemoteKeySet (starts empty, no cached
	// inflight) so the retry always performs a fresh JWKS fetch. The check
	// v.verifier == ver prevents duplicate rebuilds when multiple goroutines
	// hit the same rotation at once: only the first write-lock holder rebuilds;
	// the rest find the pointer already updated and reuse the new verifier.
	v.verMu.Lock()
	if v.verifier == ver {
		if !v.lastRebuild.IsZero() && v.now().Sub(v.lastRebuild) < minRebuildInterval {
			// Rebuild budget exhausted: fail closed with the original
			// signature error. No retry - a retry against the same verifier
			// would still trigger go-oidc's internal refetch and reopen the
			// amplification vector this limit exists to close.
			v.verMu.Unlock()
			return nil, err
		}
		logging.Warnf("scheduledauth: oidc signature verification failed; rebuilding key set for rotation retry")
		v.verifier = v.newVerifier()
		v.lastRebuild = v.now()
	}
	newVer := v.verifier
	v.verMu.Unlock()

	return newVer.Verify(ctx, rawToken)
}

// isSignatureError reports whether err is a go-oidc signature verification
// failure (unknown kid, key mismatch, or stale inflight result). It does NOT
// match aud/iss/exp/nbf/sub claim failures, which must never trigger a retry.
// The sentinel string "failed to verify signature" is the exact prefix emitted
// by (*oidc.IDTokenVerifier).Verify when (*RemoteKeySet).VerifySignature
// returns an error (see go-oidc/v3 verify.go).
func isSignatureError(err error) bool {
	return strings.Contains(err.Error(), "failed to verify signature")
}

// extractBearerToken pulls the JWT out of an `Authorization: Bearer <jwt>`
// header. Returns ErrUnauthorized for any shape that isn't a non-empty
// bearer token.
func extractBearerToken(authz string) (string, error) {
	if authz == "" {
		return "", fmt.Errorf("%w: missing Authorization header", ErrUnauthorized)
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(authz, prefix) {
		return "", fmt.Errorf("%w: Authorization is not a Bearer token", ErrUnauthorized)
	}
	raw := strings.TrimSpace(authz[len(prefix):])
	if raw == "" {
		return "", fmt.Errorf("%w: empty bearer token", ErrUnauthorized)
	}
	return raw, nil
}

// checkTimestamps applies skew-tolerant exp/nbf/iat checks. exp is
// required (Cloud Scheduler always sets it); nbf and iat are optional
// per RFC 7519. The iat-in-future check defends against horizontally-
// spread clock-skew attacks.
func (v *Validator) checkTimestamps(c claims) error {
	now := v.now()

	if c.ExpiresAt == 0 {
		return fmt.Errorf("%w: missing exp claim", ErrUnauthorized)
	}
	exp := time.Unix(c.ExpiresAt, 0)
	if now.After(exp.Add(v.skew)) {
		return fmt.Errorf("%w: token expired at %s (now=%s, skew=%s)",
			ErrUnauthorized, exp.Format(time.RFC3339), now.Format(time.RFC3339), v.skew)
	}

	if c.NotBefore != 0 {
		nbf := time.Unix(c.NotBefore, 0)
		if now.Add(v.skew).Before(nbf) {
			return fmt.Errorf("%w: token not yet valid (nbf=%s, now=%s)",
				ErrUnauthorized, nbf.Format(time.RFC3339), now.Format(time.RFC3339))
		}
	}

	if c.IssuedAt != 0 {
		iat := time.Unix(c.IssuedAt, 0)
		if iat.After(now.Add(v.skew)) {
			return fmt.Errorf("%w: token iat in the future (iat=%s, now=%s)",
				ErrUnauthorized, iat.Format(time.RFC3339), now.Format(time.RFC3339))
		}
	}
	return nil
}

func audienceMatches(tokenAud []string, allowed map[string]struct{}) bool {
	for _, a := range tokenAud {
		if _, ok := allowed[a]; ok {
			return true
		}
	}
	return false
}

// IsUnauthorized reports whether err originated from a Validate failure.
// Useful for callers that need to distinguish auth failures from config
// errors.
func IsUnauthorized(err error) bool {
	return errors.Is(err, ErrUnauthorized)
}
