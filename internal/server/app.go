// Package server provides a cloud-agnostic server implementation for CUDly.
// It supports both AWS Lambda and standard HTTP server modes.
package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/analytics"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/archera"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/commitmentopts"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/oidc"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/scheduler"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/secrets"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/server/scheduledauth"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Application holds all components of the CUDly server.
type Application struct {
	// scheduledTasks tracks detached HTTP-triggered scheduled tasks so server
	// shutdown can cancel and wait for them (#784).
	scheduledTasks scheduledTaskTracker
	// migrationFinishedAt is when the most recent migration attempt ended; see
	// migrationErr. Guarded by migrationMu.
	migrationFinishedAt time.Time
	secretResolver      secrets.Resolver
	Purchase            PurchaseManagerInterface
	Email               email.SenderInterface // Multi-cloud email sender (AWS SES, GCP SendGrid, Azure ACS)
	// lifeCtx outlives any request; Close cancels it via lifeCancel. Guarded by lifeMu.
	lifeCtx     context.Context
	RateLimiter api.RateLimiterInterface // Distributed rate limiter (DB-backed for multi-instance)
	Analytics   AnalyticsStoreInterface  // Analytics store for savings data
	// AnalyticsCollector aggregates savings into snapshots on a schedule.
	// Nil until reinitializeAfterConnect wires it; the collect task no-ops
	// when nil so test builds without a DB stay quiet.
	AnalyticsCollector AnalyticsCollectorInterface
	// State from the most recent migration attempt. Surfaced by /health so
	// ops can see failures. Protected by its OWN dedicated mutex -- NOT
	// dbMu, because ensureDB holds dbMu for its full duration. If /health
	// reached into dbMu it would block behind a long-running ensureDB,
	// which defeats the point of non-fatal migrations.
	migrationErr error
	// marketplacePoller polls RI Marketplace listings during the
	// finalize_revocations sweep (issue #27). Nil means the API handler; only
	// tests set it.
	marketplacePoller marketplaceListingPoller
	TaskLocker        TaskLocker // Advisory lock for scheduled tasks (defaults to DB)
	signer            oidc.Signer
	Scheduler         SchedulerInterface
	dbErr             error
	Config            config.StoreInterface
	DB                *database.Connection // PostgreSQL database connection
	scheduledAuth     *scheduledauth.Validator
	lifeCancel        context.CancelFunc
	// LadderAccountResolver resolves the Lambda's own AWS account ID and region
	// for the single-account ladder gate (Q1). It MUST fail loud when the
	// account cannot be determined: the account ID gates which configs run, so a
	// transient STS failure must abort the whole ladder_run rather than silently
	// skip every config as multi-account. Nil in production -> the default
	// STS-backed resolver (defaultLadderAccountResolver); tests inject a stub.
	LadderAccountResolver func(ctx context.Context) (accountID, region string, err error)
	archera               *archera.Provider
	// dbConfig is lazy-initialization state for PostgreSQL (Lambda ENI
	// readiness), like dbConnected and dbErr. Guarded by dbMu.
	dbConfig *database.Config
	// LadderCapabilityFactory constructs a LadderCapability for the given region
	// and accountID. It is called once per ladder_run task invocation.
	// Defaults to awsladder.NewFromAWSConfig in production; tests replace it
	// with a fake factory that returns a hermetic LadderCapability.
	LadderCapabilityFactory func(ctx context.Context, region, accountID string) (pkgladder.LadderCapability, error)
	// rlCleanupStop stops the previous rate-limit cleanup worker when a retried
	// reinitializeAfterConnect starts a new one, so at most one worker runs.
	// Guarded by lifeMu.
	rlCleanupStop     context.CancelFunc
	runMigrationsFunc func(ctx context.Context, pool *pgxpool.Pool, migrationsPath, adminEmail, adminPassword string) error
	API               *api.Handler
	Auth              *auth.Service
	// encKeySource is the env var name that resolved the credential encryption
	// key (e.g. "CREDENTIAL_ENCRYPTION_KEY_SECRET_NAME"). Set during
	// reinitializeAfterConnect. /health reports only a generic degraded state
	// from it, never the source itself (#15).
	encKeySource string
	Version      string
	// Static file serving directory (from STATIC_DIR env var)
	staticDir string
	appConfig ApplicationConfig
	// initRetryDelay is the pause between background database-initialization
	// attempts (see startBackgroundInit). Zero means backgroundInitRetryDelay.
	// Only tests set it.
	initRetryDelay    time.Duration
	migrationsTimeout time.Duration
	dbMu              sync.Mutex // guards dbConfig, dbConnected and dbErr
	lifeMu            sync.Mutex // guards lifeCtx, lifeCancel and rlCleanupStop
	migrationMu       sync.Mutex // guards migrationErr and migrationFinishedAt
	dbConnected       bool
}

// NewApplicationFromDeps creates an Application from pre-built configuration and dependencies.
// This is the testable constructor - all external I/O is done before calling this.
func NewApplicationFromDeps(ctx context.Context, cfg ApplicationConfig, deps ExternalDeps) (*Application, error) {
	if deps.DBConfig == nil {
		return nil, fmt.Errorf("database configuration required: DBConfig must be provided")
	}

	// Validate money-moving env defaults at the startup boundary -- consistent with the
	// fail-fast posture used for scheduledauth mode and ADMIN_PASSWORD_SECRET (issue #1026).
	if err := validateAppConfigEnvDefaults(cfg); err != nil {
		return nil, err
	}

	// Wire up the /api/scheduled/* auth validator. initScheduledAuth loads
	// the mode config, resolves the bearer secret with fail-fast semantics
	// in bearer mode (04-M4), builds the validator, and warms up JWKS.
	scheduledAuth, err := initScheduledAuth(ctx, &cfg, deps.SecretResolver)
	if err != nil {
		return nil, err
	}

	// Construct the OIDC issuer signer once per deployment. Nil means
	// the deployment has not opted into the federated flow yet — all
	// OIDC-dependent paths (handler_oidc.go, purchase manager Azure
	// federated credential) fall back to their legacy behaviors.
	signer, signerErr := oidc.NewSignerFromEnv(ctx)
	if signerErr != nil {
		logging.Errorf("oidc signer init failed (federated flow disabled): %v", signerErr)
		signer = nil
	}

	// Prime the issuer URL cache. Priority order:
	//  1. CUDLY_ISSUER_URL / DASHBOARD_URL via resolveOIDCIssuerURL
	//  2. AWS Lambda self-lookup via lambda:GetFunctionUrlConfig
	//
	// The handler_oidc.go path is still a backstop (populates the
	// cache from the first inbound request's DomainName), but doing
	// it here means scheduled-task cold starts don't race the first
	// inbound HTTP request.
	if issuer := resolveOIDCIssuerURL(cfg); issuer != "" {
		if err := oidc.SetIssuerURL(issuer); err != nil {
			logging.Warnf("WARN: oidc issuer URL invalid, ignoring: %v", err)
		}
	} else if cfg.IsLambda {
		primeCtx, primeCancel := context.WithTimeout(ctx, 5*time.Second)
		if err := oidc.PrimeIssuerURLFromLambda(primeCtx); err != nil {
			logging.Warnf("oidc issuer cache not primed from lambda: %v (falling back to request-driven populate)", err)
		}
		primeCancel()
	}

	// Initialize purchase manager
	purchaseManager := purchase.NewManager(purchase.ManagerConfig{
		ConfigStore:            deps.ConfigStore,
		EmailSender:            deps.EmailSender,
		STSClient:              deps.STSClient,
		NotificationDaysBefore: cfg.NotificationDaysBefore,
		MaxDueRowAge:           cfg.ScheduledPurchaseMaxOverdue,
		OIDCSigner:             signer,
		OIDCIssuerURL:          resolveOIDCIssuerURL(cfg),
	})

	// Initialize scheduler
	sched := scheduler.NewScheduler(scheduler.SchedulerConfig{
		ConfigStore:     deps.ConfigStore,
		PurchaseManager: purchaseManager,
		EmailSender:     deps.EmailSender,
		STSClient:       deps.STSClient,
		// cfg.IsLambda, never a live runtime.IsLambda(): the Application has
		// exactly one answer to "am I on Lambda", shared with the post-DB
		// scheduler in ensureDB and the API handler (issue #110).
		IsLambda: cfg.IsLambda,
	})
	purchaseManager.SetRecommendationSource(sched)

	// Auth store will be initialized lazily after DB connection
	var authStore auth.StoreInterface
	logging.Infof("PostgreSQL auth store will be initialized on first request")

	// Initialize auth service
	authService := auth.NewService(auth.ServiceConfig{
		Store:           authStore,
		EmailSender:     deps.EmailSender,
		SessionDuration: 24 * time.Hour,
		DashboardURL:    cfg.DashboardURL,
		BcryptCost:      cfg.authBcryptCost,
	})

	// Sensitive requests pass ensureDB, which replaces this temporary limiter
	// with shared database counters before dispatch on every runtime.
	rateLimiter := api.RateLimiterInterface(api.NewInMemoryRateLimiter())
	logging.Infof("Initialized temporary in-memory rate limiter until database connection")

	// Archera comparison: default off, key resolved lazily on first explicit request.
	archeraProvider := archera.NewProvider(archera.SettingsFromEnv(), deps.SecretResolver, nil)

	// Initialize API handler
	apiHandler := api.NewHandler(api.HandlerConfig{
		ConfigStore:       deps.ConfigStore,
		PurchaseManager:   purchaseManager,
		Scheduler:         sched,
		AuthService:       newAuthServiceAdapter(authService),
		APIKeySecretARN:   cfg.APIKeySecretARN,
		EnableDashboard:   cfg.EnableDashboard,
		DashboardBucket:   cfg.DashboardBucket,
		CORSAllowedOrigin: cfg.CORSAllowedOrigin,
		RateLimiter:       rateLimiter,
		EmailNotifier:     deps.EmailSender,
		DashboardURL:      cfg.DashboardURL,
		OIDCSigner:        signer,
		OIDCIssuerURL:     resolveOIDCIssuerURL(cfg),
		Insurance:         archeraProvider,
		IsLambda:          cfg.IsLambda,

		RecommendationSchedule: cfg.RecommendationSchedule,
		FireSweepOff:           cfg.FireSweepOff,
	})

	logging.Infof("CUDly Server initialization complete")

	return &Application{
		Config:            deps.ConfigStore,
		API:               apiHandler,
		Scheduler:         sched,
		Purchase:          purchaseManager,
		Email:             deps.EmailSender,
		Auth:              authService,
		RateLimiter:       rateLimiter,
		Version:           cfg.Version,
		DB:                nil, // Will be initialized lazily on first request
		staticDir:         staticDirFromEnv(),
		dbConfig:          deps.DBConfig,
		secretResolver:    deps.SecretResolver,
		archera:           archeraProvider,
		appConfig:         cfg,
		signer:            signer,
		scheduledAuth:     scheduledAuth,
		migrationsTimeout: resolveMigrationsTimeout(),
		runMigrationsFunc: migrations.RunMigrations,
	}, nil
}

// reinitializeAfterConnect re-creates all stores and services that depend on the
// database connection. This is called after the lazy DB connect succeeds.
// Returns an error if any store or service initialization fails.
func (app *Application) reinitializeAfterConnect(ctx context.Context, dbConn *database.Connection) error {
	// Initialize config store with the connection
	pgStore := config.NewPostgresStore(dbConn)
	if pgStore == nil {
		return fmt.Errorf("failed to create PostgreSQL config store")
	}
	app.Config = pgStore
	// Wire per-recipient mute suppression + List-Unsubscribe now that the config
	// store (which implements email.MuteChecker via IsNotificationMuted) is live.
	// Moving this here from NewApplication is correct: mute lookups require DB,
	// so decorating before the store is available would panic on the first call.
	app.Email = decorateSenderWithMute(app.Email, pgStore, strings.TrimRight(app.appConfig.DashboardURL, "/"))

	// Initialize auth store with the connection
	authStore := auth.NewPostgresStore(dbConn)
	if authStore == nil {
		return fmt.Errorf("failed to create PostgreSQL auth store")
	}

	// Load the credential encryption key (deploy-provided, stable across
	// instances and cold-starts) up front: it seeds the CSRF key below and is
	// reused for the credential store further down. loadAndGuardEncryptionKey
	// memoizes via sync.Once, so the later credential-store call is free.
	encKey, encKeySource, err := loadAndGuardEncryptionKey(ctx, app.secretResolver)
	if err != nil {
		return err
	}

	// Update auth service with PostgreSQL auth store. The CSRF key is derived
	// from the encryption key so every instance uses the same stable key.
	authSvc, err := app.initAuthService(authStore, encKey)
	if err != nil {
		return err
	}
	app.Auth = authSvc

	dbRL := api.NewDBRateLimiter(dbConn.Pool())
	app.RateLimiter = dbRL
	logging.Infof("Initialized database-backed rate limiter with shared replica counters")

	// Initialize analytics store for savings data and materialized views, plus
	// the snapshot collector behind the scheduled analytics_collect task.
	app.Analytics = analytics.NewPostgresAnalyticsStore(dbConn)
	collector, err := newAnalyticsCollector(dbConn, app.Config)
	if err != nil {
		return fmt.Errorf("failed to create analytics collector: %w", err)
	}
	app.AnalyticsCollector = collector
	logging.Infof("Initialized PostgreSQL analytics store and snapshot collector")

	// Initialize credential store (AES-256-GCM encrypted credential blobs).
	// encKey/encKeySource were loaded above (memoized) to seed the CSRF key.
	credStore := credentials.NewCredentialStore(dbConn.Pool(), encKey)
	app.encKeySource = encKeySource
	logging.Infof("Initialized encrypted credential store")

	// Re-initialize purchase manager with multi-account deps now that credStore is available.
	// The initial manager (created before DB connect) lacks CredentialStore and AssumeRoleSTS,
	// so the multi-account fan-out guard (m.credStore != nil) would always be false without this.
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to load AWS config for cross-account STS: %w", err)
	}
	reinitPurchase := purchase.NewManager(postDBPurchaseManagerConfig(app, pgStore, credStore, awsCfg))
	app.Purchase = reinitPurchase
	logging.Infof("Re-initialized purchase manager with credential store and cross-account STS")

	// Build the commitment-options probe/cache service. It lazily probes
	// AWS reserved-offerings APIs through the first enabled AWS account and
	// persists the result so subsequent calls come from the DB. Failures
	// anywhere along the chain (no account connected, probe denied) collapse
	// to ErrNoData; the API handler and save-side validator both degrade
	// gracefully, so this is wired unconditionally. It is built before the scheduler,
	// which refreshes its Azure part after a collection run.
	commitmentOpts := commitmentopts.New(
		commitmentopts.NewPostgresStore(dbConn),
		app.Config,
		newCommitmentProbeConfigBuilder(credStore, app.Config, awsCfg, sts.NewFromConfig(awsCfg)),
		commitmentopts.DefaultProbers(),
	).WithAzure(
		newCommitmentProbeAzureCredential(credStore, app.signer, resolveOIDCIssuerURL(app.appConfig)),
		azureProbeCurrency,
		commitmentopts.DefaultAzureProbers(),
	)

	// Re-initialize scheduler with per-account credential resolution.
	reinitScheduler := scheduler.NewScheduler(scheduler.SchedulerConfig{
		ConfigStore:       app.Config,
		PurchaseManager:   app.Purchase,
		EmailSender:       app.Email,
		CredentialStore:   credStore,
		OIDCSigner:        app.signer,
		OIDCIssuerURL:     resolveOIDCIssuerURL(app.appConfig),
		AssumeRoleSTS:     sts.NewFromConfig(awsCfg),
		BastionSTSFactory: credentials.NewSTSClientFactory(awsCfg),
		AmbientAWSCreds:   awsCfg.Credentials,
		STSClient:         sts.NewFromConfig(awsCfg),
		AzureOptions:      commitmentOpts,
		IsLambda:          app.appConfig.IsLambda,
	})
	app.Scheduler = reinitScheduler
	reinitPurchase.SetRecommendationSource(reinitScheduler)

	// Update API handler with new config store, scheduler, and rate limiter.
	// AnalyticsClient is Postgres-backed (see api.NewPostgresAnalyticsClient) —
	// it aggregates purchase_history on demand so the History UI charts work
	// without requiring a separate S3/Athena deployment.
	app.API = api.NewHandler(api.HandlerConfig{
		ConfigStore:         app.Config,
		CredentialStore:     credStore,
		PurchaseManager:     app.Purchase,
		Scheduler:           app.Scheduler,
		AuthService:         newAuthServiceAdapter(app.Auth),
		APIKeySecretARN:     app.appConfig.APIKeySecretARN,
		EnableDashboard:     app.appConfig.EnableDashboard,
		DashboardBucket:     app.appConfig.DashboardBucket,
		CORSAllowedOrigin:   app.appConfig.CORSAllowedOrigin,
		RateLimiter:         app.RateLimiter,
		EmailNotifier:       app.Email,
		DashboardURL:        app.appConfig.DashboardURL,
		AnalyticsClient:     api.NewPostgresAnalyticsClient(dbConn),
		AnalyticsCollector:  app.AnalyticsCollector,
		AnalyticsSnapshots:  analytics.NewPostgresAnalyticsStore(dbConn),
		OIDCSigner:          app.signer,
		OIDCIssuerURL:       resolveOIDCIssuerURL(app.appConfig),
		CommitmentOpts:      commitmentOpts,
		EncryptionKeySource: app.encKeySource,
		Insurance:           app.archera,
		IsLambda:            app.appConfig.IsLambda,

		RecommendationSchedule: app.appConfig.RecommendationSchedule,
		FireSweepOff:           app.appConfig.FireSweepOff,
	})
	if app.API == nil {
		return fmt.Errorf("failed to create API handler")
	}

	// Periodic cleanup also evicts expired keys with no subsequent allowed
	// request. Started last: on any earlier error ensureDB closes the pool, and
	// a worker started before that would keep ticking against it.
	app.startRateLimiterCleanup(dbRL)

	return nil
}

// Helper functions for environment variable parsing
