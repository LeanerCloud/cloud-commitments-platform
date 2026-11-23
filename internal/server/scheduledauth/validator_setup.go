package scheduledauth

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/httpclient"
)

// New builds a Validator from a parsed Config. The caller is responsible
// for invoking NewFromEnv (or a similar config loader) to populate
// Config from environment variables.
//
// In oidc mode, the underlying *oidc.RemoteKeySet performs JWKS retrieval
// lazily on the first request and refreshes on unknown kid (per
// https://openid.net/specs/openid-connect-core-1_0.html#RotateSigKeys).
// Single-flight is built in. There is no fixed TTL — refresh-on-
// unknown-kid is sufficient because providers always sign new tokens
// with the new kid before retiring the old key.
//
// To pre-warm the cache and surface JWKS endpoint misconfiguration at
// startup, callers should invoke (*Validator).Warmup. Fail-open for serving
// by design (a Lambda cold-start init budget cannot host a retry loop; a bad
// JWKS only affects /api/scheduled/*). The outcome is exposed via JWKSHealth
// and shown as the scheduled_auth_jwks check on /health; the deploy gate
// fails a persistent degraded state. Healthy is terminal. platform#67, A+.
func New(cfg Config) (*Validator, error) {
	if cfg.Skew <= 0 {
		cfg.Skew = DefaultClockSkew
	}

	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	v := &Validator{
		mode: cfg.Mode,
		skew: cfg.Skew,
		now:  cfg.Now,
	}

	switch cfg.Mode {
	case ModeOIDC:
		return configureOIDC(v, cfg)
	case ModeBearer:
		return configureBearer(v, cfg)
	case ModeDisabled:
		// No setup; every request passes (with a per-request WARN log).
		logging.Warnf("scheduledauth: WARN — auth mode is disabled; /api/scheduled/* is unauthenticated. " +
			"Set SCHEDULED_TASK_AUTH_MODE=oidc (GCP) or =bearer (Azure) in production.")
		return v, nil
	default:
		return nil, fmt.Errorf("%w: unknown mode %q (expected oidc, bearer, or disabled)", ErrConfigInvalid, cfg.Mode)
	}
}

// configureOIDC validates OIDC config, builds set-lookups, and wires the
// go-oidc RemoteKeySet + IDTokenVerifier. Split out of New to keep the
// per-mode setup below the cyclomatic-complexity gate.
func configureOIDC(v *Validator, cfg Config) (*Validator, error) {
	if cfg.Issuer == "" {
		cfg.Issuer = GoogleIssuer
	}
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = GoogleJWKSURL
	}
	if err := validateHTTPSURL(cfg.JWKSURL, "SCHEDULED_TASK_OIDC_JWKS_URL"); err != nil {
		return nil, err
	}
	if len(cfg.Audiences) == 0 {
		return nil, fmt.Errorf("%w: oidc mode requires SCHEDULED_TASK_OIDC_AUDIENCE", ErrConfigInvalid)
	}
	// Subject pinning is REQUIRED in oidc mode. Any GCP service account
	// in the same org can mint OIDC tokens with arbitrary `aud` values
	// — we MUST also pin the issuer SA unique_id via `sub`. Google uses
	// the service-account unique_id, not the email address, as the `sub`
	// claim for Cloud Scheduler-signed ID tokens. That is what
	// SCHEDULED_TASK_OIDC_SUBJECTS must contain.
	if len(cfg.Subjects) == 0 {
		return nil, fmt.Errorf("%w: oidc mode requires SCHEDULED_TASK_OIDC_SUBJECTS (defense in depth)", ErrConfigInvalid)
	}

	auds, err := cleanSet(cfg.Audiences, "SCHEDULED_TASK_OIDC_AUDIENCE")
	if err != nil {
		return nil, err
	}
	subs, err := cleanSet(cfg.Subjects, "SCHEDULED_TASK_OIDC_SUBJECTS")
	if err != nil {
		return nil, err
	}

	v.audiences = auds
	v.subjects = subs
	v.jwksURL = cfg.JWKSURL
	v.issuer = cfg.Issuer
	// All JWKS traffic (warmup probe and go-oidc key fetches) goes
	// through the hardened shared client: IMDS/metadata endpoints are
	// blocked and dial/TLS/overall timeouts apply. The JWKS URL is
	// operator-supplied (SCHEDULED_TASK_OIDC_JWKS_URL), so a
	// misconfigured or compromised value must not be able to reach
	// internal/metadata endpoints via the default transport.
	v.httpClient = httpclient.New()
	// The kid guard wraps the hardened transport to observe JWKS
	// responses; it does not replace it.
	v.kids = newKidGuard(v.httpClient, func() time.Time { return v.now() }, []jose.SignatureAlgorithm{jose.RS256})
	v.verifier = v.newVerifier()
	return v, nil
}

// newVerifier builds a fresh go-oidc RemoteKeySet (empty cache, no
// inflight fetch) behind the negative kid guard, and the IDTokenVerifier
// on top of it. Used at startup and by the rotation-recovery rebuild.
func (v *Validator) newVerifier() *oidc.IDTokenVerifier {
	ks := oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), v.httpClient), v.jwksURL)
	return oidc.NewVerifier(v.issuer, &guardedKeySet{inner: ks, g: v.kids}, &oidc.Config{
		// Pin to RS256. Google's tokens are RS256; rejecting anything
		// else closes the alg=none / alg=HS256 confusion family.
		SupportedSigningAlgs: []string{string(oidc.RS256)},
		// We validate audience manually (multiple values supported)
		// and expiry manually (skew tolerance). Issuer is checked by
		// go-oidc.
		SkipClientIDCheck: true,
		SkipExpiryCheck:   true,
	})
}

func configureBearer(v *Validator, cfg Config) (*Validator, error) {
	if cfg.Bearer == "" {
		return nil, fmt.Errorf("%w: bearer mode requires SCHEDULED_TASK_SECRET", ErrConfigInvalid)
	}
	v.bearer = []byte("Bearer " + cfg.Bearer)
	return v, nil
}

// cleanSet trims and deduplicates input, rejecting blank entries that
// would silently match a missing claim.
func cleanSet(in []string, label string) (map[string]struct{}, error) {
	out := make(map[string]struct{}, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, fmt.Errorf("%w: %s contains empty value", ErrConfigInvalid, label)
		}
		out[s] = struct{}{}
	}
	return out, nil
}

// validateHTTPSURL requires an absolute https URL. Plain http is allowed only
// for loopback hosts, which a network attacker cannot sit in front of.
func validateHTTPSURL(raw, label string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %s must be an absolute URL: %w", ErrConfigInvalid, label, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%w: %s must be an absolute URL", ErrConfigInvalid, label)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && isLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%w: %s must use https", ErrConfigInvalid, label)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
