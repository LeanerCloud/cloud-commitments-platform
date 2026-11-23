// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/accounts"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/oidc"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/schedexpr"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Handler processes HTTP requests.
type Handler struct {
	awsCfgErr error // error from loading the base config, if any
	credStore credentials.CredentialStore
	purchase  PurchaseManagerInterface
	scheduler SchedulerInterface
	auth      AuthServiceInterface
	// collectionSchedule is HandlerConfig.RecommendationSchedule parsed once
	// at construction. Nil when the schedule is unset or unparsable, which
	// makes next_collection_at null.
	collectionSchedule schedexpr.Schedule
	// commitmentOpts discovers which AWS (term, payment) combinations
	// each service actually sells and validates saves against that data.
	// Nil is valid: the endpoint returns unavailable and save-side
	// validation no-ops, deferring to the frontend's hardcoded rules.
	commitmentOpts CommitmentOptsInterface
	// lambdaInvoker is the async-invoke client used by postRefreshRecommendations
	// and triggerColdStartCollect. In production it is constructed lazily from the
	// cached awsCfg. Tests inject a stub to avoid live Lambda calls.
	lambdaInvoker      LambdaInvokerInterface
	rateLimiter        RateLimiterInterface
	emailNotifier      email.SenderInterface // Optional: purchase approval emails
	config             config.StoreInterface
	analyticsClient    AnalyticsClientInterface        // Optional: analytics client (Postgres-backed in prod)
	analyticsCollector AnalyticsCollectorInterface     // Optional: snapshot collector
	analyticsSnapshots AnalyticsSnapshotStoreInterface // Optional: savings-snapshot time-series store
	signer             oidc.Signer                     // Optional: OIDC issuer signer (backed by cloud KMS)
	insurance          InsuranceProvider               // Optional: Archera comparison (nil = off)
	// Optional resolver for the running AWS account number, injected by
	// the listConvertibleRIs tests so the account-scoping branch can run
	// without live STS credentials. When nil (production default), the
	// handler calls h.resolveAWSAccountID. Returns the raw AWS account
	// number (e.g. "123456789012"), matching the account_id chip value.
	riInstancesAccountResolver func(context.Context) (string, error)
	// executeExchangeFn is the RI exchange execution function injected by tests.
	// When nil (the production default), awsExchangeExecutor returns
	// exchange.ExecuteExchange directly. Tests inject a stub so the handler
	// can be exercised end-to-end without live AWS credentials or a real RI.
	executeExchangeFn func(ctx context.Context, req exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error)
	// Optional Azure revoke client factory injected by tests. When nil, the
	// revoke handlers use the production Azure credential and client
	// constructors.
	azureRevokeFactory *azureRevokeClientFactory
	// exchangeQuoteFn is the RI exchange quote function injected by tests.
	// When nil (the production default), awsExchangeQuoter returns
	// exchange.GetExchangeQuote, which builds its own EC2/STS clients from the
	// ambient AWS SDK chain. Tests inject a stub so getExchangeQuote never
	// resolves credentials, probes IMDS or calls EC2 (#855).
	exchangeQuoteFn func(ctx context.Context, req exchange.ExchangeQuoteRequest) (*exchange.ExchangeQuoteSummary, error)
	// now is the wall clock for the start_date guard in
	// validatePurchaseScheduling. Nil (the production default) means
	// time.Now; tests inject a fixed instant so date-boundary cases are
	// deterministic. Read it through h.clock().
	now func() time.Time
	// loadAWSCfg loads the base AWS config for getBaseAWSConfig. Nil (the
	// production default) means loadDefaultAWSConfig in provider_wiring.go;
	// tests inject a stub to observe the context the load runs under.
	loadAWSCfg func(context.Context) (aws.Config, error)
	// Optional org-discovery factory used by tests to avoid live AWS
	// Organizations API calls. When nil (production default), the handler
	// falls back to accounts.DiscoverOrgAccounts which dials Organizations
	// via the credentials resolved for the org-root account.
	discoverOrgFn func(context.Context, aws.Config) (*accounts.OrgDiscoveryResult, error)
	sourceID      *sourceIdentity // cached source cloud identity
	// Optional account-resolver injection point used by the reshape
	// handler integration test. When nil (the production default), the
	// handler calls h.resolveAWSCloudAccountID which in turn invokes
	// sts.GetCallerIdentity — fine in Lambda but fails on dev machines
	// without AWS credentials. Tests set this to a fixed-result fake so
	// the integration suite runs hermetically.
	reshapeAccountResolver func(context.Context) (string, error)
	// Postgres-backed TTL cache for Cost Explorer
	// GetReservationUtilization. Dashboard + RI Exchange page hits
	// read from the shared cache table so Lambda containers don't each
	// fan out to a paid CE API call on every page load. See
	// ri_utilization_cache.go for the rationale; in-memory was ruled
	// out because Lambda's short container lifetime means each cold
	// start would bypass the cache entirely.
	riUtilizationCache *riUtilizationCache
	// Optional AWS-client injection points used by the reshape handler
	// integration test. When nil (the production default), the
	// handler falls back to the direct AWS SDK constructors
	// `awsprovider.NewEC2ClientDirect` and
	// `awsprovider.NewRecommendationsClientDirect`. Tests set these
	// to stubs that satisfy the narrow interfaces declared in
	// `handler_ri_exchange_clients.go` (reshapeEC2Client / reshapeRecsClient)
	// so the test can exercise the handler end-to-end without live
	// AWS credentials. Prod behavior is unchanged because both
	// fields stay nil.
	reshapeEC2Factory  func(aws.Config) reshapeEC2Client
	reshapeRecsFactory func(aws.Config) reshapeRecsClient
	// Optional target-offerings EC2 client factory injected by tests. When nil
	// (the production default), listTargetOfferings uses awsprovider.NewEC2ClientDirect.
	targetOfferingsEC2Factory func(aws.Config) targetOfferingsEC2Client
	// Optional Azure exchange client factory injected by tests. When nil
	// (the production default), buildAzureExchangeClient resolves the
	// registered CloudAccount's per-subscription credentials and constructs
	// a real armreservations-backed client. The azureExchangeClient
	// interface covers listing exchangeable reservations plus pricing
	// (CalculateExchange) and committing (ExecuteExchange) an exchange.
	azureExchangeFactory func(subscriptionID string) azureExchangeClient
	// Optional marketplace EC2 client factory injected by tests. When nil
	// (the production default), buildMarketplaceEC2Client uses
	// awsprovider.NewEC2ClientDirect.
	marketplaceEC2Factory func(aws.Config) marketplaceEC2Client
	// Optional listing-finder injected by tests. When nil, the marketplace
	// reconcile uses the AWS SDK directly (see awsMarketplaceListingFinder).
	marketplaceFinderFactory func(aws.Config) marketplaceListingFinder
	awsCfg                   aws.Config // cached base AWS config (no region override)
	// encryptionKeySource is the env var name that resolved the credential
	// encryption key. Empty when no credStore is configured. Read only by the
	// /health credential_store check, which keeps it out of the public body
	// and logs the dev-key case server-side (#15).
	encryptionKeySource string
	issuerURL           string // Canonical OIDC issuer URL (falls back to dashboardURL / request domain)
	corsAllowedOrigin   string // CORS allowed origin
	apiKey              string // Cached API key
	dashboardURL        string // Base URL for approval/cancel links
	secretsARN          string
	// executionTimeoutOverride, when positive, replaces riExchangeExecutionTimeout
	// for the direct executes. Tests only.
	executionTimeoutOverride time.Duration
	sourceIdentityOnce       sync.Once // guards one-time source identity resolution
	riUtilizationCacheOnce   sync.Once
	awsCfgOnce               sync.Once // guards one-time loading of the base AWS config; load it through getBaseAWSConfig
	isLambda                 bool      // Lambda mode from HandlerConfig.IsLambda; gates background goroutines
	// fireSweepOff is HandlerConfig.FireSweepOff.
	fireSweepOff bool
}

// getRIUtilizationCache returns the Postgres-backed TTL cache for Cost
// Explorer results, lazy-initialized on first call so tests that never
// exercise the RI Exchange paths don't need to wire it up. The Lambda
// mode comes from h.isLambda (HandlerConfig.IsLambda, i.e. the
// Application's captured value, issue #110) so SWR is gated off on
// Lambda where background goroutines freeze between invocations.
func (h *Handler) getRIUtilizationCache() *riUtilizationCache {
	h.riUtilizationCacheOnce.Do(func() {
		h.riUtilizationCache = newRIUtilizationCache(h.config, h.isLambda)
	})
	return h.riUtilizationCache
}

// IsLambda reports the Lambda mode this handler was constructed with
// (HandlerConfig.IsLambda). It never re-reads the environment.
func (h *Handler) IsLambda() bool {
	return h.isLambda
}

// NewHandler creates a new API handler.
func NewHandler(cfg HandlerConfig) *Handler {
	corsOrigin := cfg.CORSAllowedOrigin
	if corsOrigin == "" {
		// Security: CORS must be explicitly configured
		// For local development, use CORS_ALLOWED_ORIGIN=http://localhost:3000
		// For production, use CORS_ALLOWED_ORIGIN=https://your-cloudfront-domain.com
		logging.Errorf("SECURITY WARNING: CORS_ALLOWED_ORIGIN not set. CORS will be disabled (no Access-Control-Allow-Origin header). Set this to your dashboard URL.")
		// Leave corsOrigin empty - the response will not include Access-Control-Allow-Origin header
		// This effectively disables CORS for browser-based clients
	}

	warnIfCookieCannotWork(corsOrigin)

	h := &Handler{
		config:              cfg.ConfigStore,
		credStore:           cfg.CredentialStore,
		purchase:            cfg.PurchaseManager,
		scheduler:           cfg.Scheduler,
		auth:                cfg.AuthService,
		secretsARN:          cfg.APIKeySecretARN,
		corsAllowedOrigin:   corsOrigin,
		rateLimiter:         cfg.RateLimiter,
		emailNotifier:       cfg.EmailNotifier,
		dashboardURL:        cfg.DashboardURL,
		analyticsClient:     cfg.AnalyticsClient,
		analyticsCollector:  cfg.AnalyticsCollector,
		analyticsSnapshots:  cfg.AnalyticsSnapshots,
		signer:              cfg.OIDCSigner,
		issuerURL:           cfg.OIDCIssuerURL,
		insurance:           cfg.Insurance,
		commitmentOpts:      cfg.CommitmentOpts,
		encryptionKeySource: cfg.EncryptionKeySource,
		isLambda:            cfg.IsLambda,
		collectionSchedule:  parseCollectionSchedule(cfg.RecommendationSchedule),
		fireSweepOff:        cfg.FireSweepOff,
	}

	// Pre-load API key (with a 5s timeout to avoid stalling cold-start indefinitely)
	if cfg.APIKeySecretARN != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if key, err := h.loadAPIKey(ctx); err == nil {
			h.apiKey = key
		} else {
			logging.Warnf("Failed to pre-load API key from Secrets Manager: %v", err)
		}
	}

	// The OIDC signer is constructed once at app startup (see
	// internal/server/app.go) and passed in via cfg.OIDCSigner. Leave
	// it nil to disable the /.well-known/* endpoints.

	// Startup assertion: every key string used in checkRateLimit call sites must
	// have a dedicated entry in getDefaultRateLimits(). A missing key falls
	// silently through to api_general (300/min), as happened in issue #1016.
	// This panics at init time so the gap is caught in tests and on cold start,
	// not under production load.
	assertRateLimitKeysKnown(
		"login",
		"forgot_password",
		"forgot_password_ip",
		"setup_admin",
		"reset_password",
		"change_password",
		"register",
		"approve_cancel_public",
		"mfa",
		"login_mfa",
	)

	return h
}

// errorResponseHeaders returns base plus any headers a client error carries
// (for example Retry-After). base is never mutated.
func errorResponseHeaders(err error, base map[string]string) map[string]string {
	ce, ok := IsClientError(err)
	if !ok || len(ce.headers) == 0 {
		return base
	}
	merged := make(map[string]string, len(base)+len(ce.headers))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range ce.headers {
		merged[k] = v
	}
	return merged
}

// handleRequestError converts an error to status code and response.
func (h *Handler) handleRequestError(err error) (int, any) { //nolint:gocritic // unnamedResult: stable internal API, adding names would require body renaming in impl
	if IsNotFoundError(err) {
		return 404, map[string]string{"error": "Not found"}
	}
	if ce, ok := IsClientError(err); ok {
		// If the error carries structured details (e.g. retry soft-block
		// surfaces ops_hint / retry_attempt_n / threshold), flatten them
		// into the JSON body next to "error" so the frontend can branch
		// on machine-readable hints without substring-matching the
		// human message.
		if details := ce.Details(); len(details) > 0 {
			body := make(map[string]any, len(details)+1)
			body["error"] = ce.message
			for k, v := range details {
				if k == "error" {
					continue // never let a detail key shadow the message
				}
				body[k] = v
			}
			return ce.code, body
		}
		return ce.code, map[string]string{"error": ce.message}
	}

	logging.Errorf("API error: %v", err)
	return 500, map[string]string{"error": "Internal server error"}
}

// rawResponse allows handlers to return pre-formatted, non-JSON content
// (e.g. HTML, YAML). buildResponse will use the body and contentType directly
// instead of JSON-marshaling.
//
// csp, when non-empty, overrides the default restrictive Content-Security-
// Policy header set by setSecurityHeaders. Used by serveDocsUI to relax the
// CSP for the Swagger UI page (the default `default-src 'none'` blocks the
// CDN-hosted swagger-ui assets and the inline bootstrap script, leaving the
// page blank — closes issue #329).
type rawResponse struct {
	contentType string
	body        string
	csp         string
}

// buildResponse creates a Lambda Function URL response. It never returns an
// error: all failure modes (marshal failure, non-nil err arg) are converted
// into a 500 response body so callers can use the single-return form.
func (h *Handler) buildResponse(statusCode int, headers map[string]string, body any, err error) *events.LambdaFunctionURLResponse {
	if err != nil {
		return &events.LambdaFunctionURLResponse{
			StatusCode: 500,
			Headers:    headers,
			Body:       `{"error": "internal server error"}`,
		}
	}

	if cr, ok := body.(*cookieResponse); ok {
		resp := h.buildResponse(statusCode, headers, cr.body, nil)
		resp.Cookies = cr.cookies
		return resp
	}

	// Handle raw (non-JSON) responses
	if raw, ok := body.(*rawResponse); ok {
		headers["Content-Type"] = raw.contentType
		if raw.csp != "" {
			headers["Content-Security-Policy"] = raw.csp
		}
		return &events.LambdaFunctionURLResponse{
			StatusCode: statusCode,
			Headers:    headers,
			Body:       raw.body,
		}
	}

	var bodyBytes []byte
	if body != nil {
		var marshalErr error
		bodyBytes, marshalErr = json.Marshal(body)
		if marshalErr != nil {
			logging.Errorf("Failed to marshal response: %v", marshalErr)
			return &events.LambdaFunctionURLResponse{
				StatusCode: 500,
				Headers:    headers,
				Body:       `{"error": "internal server error"}`,
			}
		}
	} else {
		// Nil-body success paths (e.g. DELETE /accounts/:id returning
		// (nil, nil)) previously emitted an empty string, which made the
		// frontend's `response.json()` throw SyntaxError on an otherwise-
		// successful request. Emit an explicit empty JSON object instead
		// so every 2xx response parses cleanly.
		bodyBytes = []byte("{}")
	}

	return &events.LambdaFunctionURLResponse{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       string(bodyBytes),
	}
}

// loadAPIKey retrieves the API key from Secrets Manager.
//
// Uses a local AWS config load — does NOT share the request-path
// awsCfgOnce/awsCfgErr cache. Reason: NewHandler runs loadAPIKey at
// cold-start with a 5-second deadline. If that deadline fires, the
// shared sync.Once would seal awsCfgErr = "context deadline exceeded"
// for the entire Lambda lifetime, permanently breaking the request-path
// resolveAWSCallerIdentity call (and the multi-tenant scope filter that
// depends on it). Loading config locally keeps the cold-start timeout
// scoped to this one call.
func (h *Handler) loadAPIKey(ctx context.Context) (string, error) {
	if h.secretsARN == "" {
		return "", nil
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return "", err
	}

	client := secretsmanager.NewFromConfig(cfg)
	result, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &h.secretsARN,
	})
	if err != nil {
		return "", err
	}

	if result.SecretString == nil {
		return "", nil
	}

	return *result.SecretString, nil
}

// sourceIdentity holds the auto-detected identity of the cloud account
// where CUDly itself runs. Resolved once at first use, cached for process lifetime.
type sourceIdentity struct {
	Provider       string `json:"provider"`
	AccountID      string `json:"account_id,omitempty"`      // AWS account number
	SubscriptionID string `json:"subscription_id,omitempty"` // Azure subscription
	TenantID       string `json:"tenant_id,omitempty"`       // Azure tenant
	ClientID       string `json:"client_id,omitempty"`       // Azure app client ID
	ProjectID      string `json:"project_id,omitempty"`      // GCP project
	// Partition is the AWS partition name (`aws`, `aws-cn`, `aws-us-gov`).
	// Populated only when Provider == "aws" and STS GetCallerIdentity
	// returned a parseable ARN; left empty on any failure path so the
	// frontend can default to the standard `aws` partition (issue #130c).
	Partition string `json:"partition,omitempty"`
}

// ExternalID returns the canonical external identifier for the source cloud.
func (s *sourceIdentity) ExternalID() string {
	switch s.Provider {
	case "aws":
		return s.AccountID
	case "azure":
		return s.SubscriptionID
	case "gcp":
		return s.ProjectID
	}
	return ""
}

// resolveSourceIdentity returns the auto-detected identity of CUDly's host
// account. All resolution is best-effort — returns an empty struct on failure.
func (h *Handler) resolveSourceIdentity(ctx context.Context) *sourceIdentity {
	h.sourceIdentityOnce.Do(func() {
		cloud := sourceCloud()
		id := &sourceIdentity{Provider: cloud}
		switch cloud {
		case "aws":
			// resolveSourceIdentity is best-effort and is consumed by
			// populateSourceAccountID, which fails loud on an empty
			// AccountID for the AWS-source case. STS errors are already
			// logged WARN inside resolveAWSCallerIdentity; the consumer's
			// empty-string check is the security gate for federation rendering.
			// (Reshape uses resolveAWSAccountID directly which DOES
			// propagate the error for fail-closed multi-tenant safety.)
			var stsErr error
			id.AccountID, id.Partition, stsErr = h.resolveAWSCallerIdentity(ctx)
			if stsErr != nil {
				logging.Debugf("resolveSourceIdentity: best-effort STS failed (already logged): %v", stsErr)
			}
		case "azure":
			id.ClientID = os.Getenv("AZURE_CLIENT_ID")
			id.SubscriptionID = os.Getenv("AZURE_SUBSCRIPTION_ID")
			id.TenantID = os.Getenv("AZURE_TENANT_ID")
		case "gcp":
			id.ProjectID = os.Getenv("GCP_PROJECT_ID")
		}
		h.sourceID = id
	})
	return h.sourceID
}

// resolveSourceAccountID returns the AWS account ID where CUDly runs.
// Convenience wrapper for the federation IaC handler.
func (h *Handler) resolveSourceAccountID(ctx context.Context) string {
	return h.resolveSourceIdentity(ctx).AccountID
}

// resolveAWSAccountID returns the AWS account ID where CUDly runs by calling
// STS GetCallerIdentity. Convenience wrapper around resolveAWSCallerIdentity
// for callers that only need the account ID (and the security-paths-aware
// error propagation).
//
// Return shape distinguishes three cases:
//
//   - ("", nil)        — AWS SDK config could not load (deployment runs
//     without AWS context: e.g. Azure / GCP host).
//     This is the legitimate "no AWS account configured"
//     signal; callers may treat it as such.
//   - ("", err)        — AWS SDK config loaded but STS GetCallerIdentity
//     failed (transient API error, missing IAM permission,
//     token expiry). Callers in security-sensitive paths
//     (e.g. multi-tenant scope filters) MUST fail closed
//     on this — treating it like the legitimate empty
//     case can leak data across tenants.
//   - (accountID, nil) — success.
//
// WARNING: callers in user-facing flows must check the result and fail loud
// rather than rendering an empty account ID into a bundle — a bundle with
// an empty source_account_id produces a broken trust policy that silently
// fails at apply time.
func (h *Handler) resolveAWSAccountID(ctx context.Context) (string, error) {
	id, _, err := h.resolveAWSCallerIdentity(ctx)
	return id, err
}

// resolveAWSCallerIdentity returns (accountID, partition) parsed from STS
// GetCallerIdentity. The partition is taken from the returned ARN's second
// segment (e.g., `arn:aws-us-gov:iam::...` → "aws-us-gov") and is used by
// the trust-policy snippet renderer to emit the correct ARN prefix in
// AWS China and GovCloud deployments (issue #130c).
//
// Return shape:
//
//   - ("", "", nil)               — host is non-AWS (sourceCloud() != "aws")
//     AND AWS SDK config could not load. This is the legitimate
//     "no AWS context" path for Azure/GCP-hosted deployments.
//   - ("", "", err)               — host is AWS but the SDK config could
//     not load, OR config loaded but STS GetCallerIdentity failed.
//     Both are real failures; security-sensitive callers MUST fail
//     closed on this.
//   - (accountID, partition, nil) — success. partition may still be "" if the
//     ARN was malformed (frontend defaults to "aws").
//
// Non-AWS hosts (Azure/GCP) short-circuit before any AWS SDK work —
// no point burning a LoadDefaultConfig attempt + STS client on every
// reshape request from a host that genuinely has no AWS context. AWS
// hosts with a broken SDK config surface the load error so the
// multi-tenant scope filter in resolveAWSCloudAccountID fails closed
// instead of degrading into an unscoped read.
func (h *Handler) resolveAWSCallerIdentity(ctx context.Context) (string, string, error) { //nolint:gocritic // unnamedResult: internal helper; naming would conflict with body's accountID/partition locals
	if sourceCloud() != "aws" {
		// Azure/GCP host: short-circuit before any AWS SDK work.
		return "", "", nil
	}
	awsCfg, err := h.getBaseAWSConfig(ctx)
	if err != nil {
		// AWS host but SDK config broken: real failure. Surface the
		// error so security-sensitive callers fail closed.
		return "", "", fmt.Errorf("aws sdk config load: %w", err)
	}
	client := sts.NewFromConfig(awsCfg)
	identity, err := client.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		logging.Warnf("Failed to resolve source account ID via STS: %v", err)
		return "", "", fmt.Errorf("sts get-caller-identity: %w", err)
	}
	var accountID, partition string
	if identity.Account != nil {
		accountID = *identity.Account
	}
	if identity.Arn != nil {
		partition = parseArnPartition(*identity.Arn)
	}
	return accountID, partition, nil
}

// parseArnPartition extracts the partition segment from an AWS ARN.
// ARN format: arn:<partition>:<service>:<region>:<account>:<resource>.
// Returns "" for inputs that aren't recognizable ARNs so the caller can
// fall back to a default. Only the three known AWS partitions are
// accepted — anything else is treated as malformed to avoid forwarding
// attacker-controlled tokens into a JSON snippet the operator copy-
// pastes into IAM.
func parseArnPartition(arn string) string {
	return config.AWSPartitionFromARN(arn)
}
