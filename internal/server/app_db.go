package server

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	awsladder "github.com/LeanerCloud/cloud-commitments-go/providers/aws/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/secrets"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// NewApplication creates and initializes a new Application instance.
// version overrides the VERSION env var when non-empty, so cmd entrypoints
// can pass the ldflags-stamped value directly instead of round-tripping
// through os.Setenv / os.Getenv (04-N1). Pass "" to fall back to the env.
func NewApplication(ctx context.Context, version string) (*Application, error) {
	cfg, err := LoadApplicationConfig()
	if err != nil {
		return nil, err
	}
	if version != "" {
		cfg.Version = version
	}

	if err = cfg.Analytics.Validate(); err != nil {
		return nil, fmt.Errorf("invalid analytics configuration: %w", err)
	}

	logging.Infof("CUDly Server initializing, version: %s", cfg.Version)

	// Initialize configuration store (PostgreSQL). The store connects lazily on
	// first request, so ConfigStore is wired as nil here (see initConfigStore).
	dbConfig, secretResolver, err := initConfigStore(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize config store: %w", err)
	}

	// Initialize email sender (auto-detects cloud provider from SECRET_PROVIDER env var)
	emailSender, err := email.NewSenderFromEnvironment(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize email sender: %w", err)
	}
	// Initialize AWS config for STS client
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	stsClient := sts.NewFromConfig(awsCfg)

	deps := ExternalDeps{
		EmailSender:    emailSender,
		ConfigStore:    nil, // created lazily on first request (see initConfigStore)
		DBConfig:       dbConfig,
		SecretResolver: secretResolver,
		STSClient:      stsClient,
	}

	app, err := NewApplicationFromDeps(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	app.LadderCapabilityFactory = awsladder.NewFromAWSConfig
	return app, nil
}

// ensureDB ensures the database connection is established (lazy initialization).
// This is called on first request to ensure Lambda ENI is ready.
// Unlike sync.Once, transient failures allow retry on subsequent requests.
func (app *Application) ensureDB(ctx context.Context) error {
	// Only attempt lazy init if we have a dbConfig
	if app.dbConfig == nil {
		return nil // Not using PostgreSQL
	}

	app.dbMu.Lock()
	defer app.dbMu.Unlock()

	// Already connected successfully
	if app.dbConnected {
		return nil
	}

	// If a previous error was set (e.g. in tests), return it
	if app.dbErr != nil {
		return app.dbErr
	}

	logging.Infof("Establishing PostgreSQL connection (lazy initialization)...")

	// Connect to PostgreSQL
	dbConn, err := database.NewConnection(ctx, app.dbConfig, app.secretResolver)
	if err != nil {
		return fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	// Store the connection
	app.DB = dbConn
	logging.Infof("PostgreSQL connection established successfully")

	// Run migrations if AutoMigrate is enabled. Failures are non-fatal:
	// we log, surface via the migrations check in /health and /ready, and
	// proceed. The app stays up and ready (/ready only waits for the first
	// attempt to finish); handlers that need the missing schema error at
	// query time.
	// See specs/migration-resilience.md (or the plan) for the rationale.
	if app.dbConfig.AutoMigrate {
		logging.Infof("Running database migrations...")

		adminEmail := os.Getenv("ADMIN_EMAIL")
		adminPassword, err := resolveAdminPassword(ctx, app.secretResolver)
		if err != nil {
			// Same cleanup as the reinitializeAfterConnect failure path below:
			// the retry loop (and repeated lazy attempts) would otherwise leak
			// a pool per attempt while secret resolution keeps failing.
			dbConn.Close()
			app.DB = nil
			return err // secret-resolution failure is still fatal — env/config, not a migration runtime error
		}

		migErr := app.runMigrationsBounded(dbConn.Pool(), app.dbConfig.MigrationsPath, adminEmail, adminPassword)
		app.recordMigrationResult(migErr)

		if migErr != nil {
			logging.Errorf("⚠️ Migration failed — app continuing with existing schema: %v", migErr)
			// Intentionally do NOT return, do NOT close dbConn, do NOT clear app.DB.
		} else {
			logging.Infof("Database migrations completed successfully")
		}
	}

	// Re-initialize all stores and services with the live DB connection
	if err := app.reinitializeAfterConnect(ctx, dbConn); err != nil {
		dbConn.Close()
		app.DB = nil
		return fmt.Errorf("failed to reinitialize after DB connect: %w", err)
	}
	app.dbConnected = true

	return nil
}

// resolveAdminPassword returns the admin password from Secrets Manager.
// It requires ADMIN_PASSWORD_SECRET to be set to a valid secret ARN/name.
// If absent, it returns an error to prevent startup with no secret source.
func resolveAdminPassword(ctx context.Context, resolver secrets.Resolver) (string, error) {
	secret := os.Getenv("ADMIN_PASSWORD_SECRET")
	if secret == "" {
		return "", fmt.Errorf("ADMIN_PASSWORD_SECRET environment variable is required but not set; refusing to start without a Secrets Manager ARN")
	}
	if resolver == nil {
		return "", fmt.Errorf("secret resolver is not configured; cannot resolve ADMIN_PASSWORD_SECRET")
	}
	resolved, err := resolver.GetSecret(ctx, secret)
	if err != nil {
		return "", fmt.Errorf("failed to resolve admin password secret: %w", err)
	}
	return resolved, nil
}

// loadAndGuardEncryptionKey loads the credential-encryption key via the
// shared secrets.Resolver and refuses to return the all-zero dev key unless
// CREDENTIAL_ENCRYPTION_ALLOW_DEV_KEY=1 is explicitly set. Logs which env
// var resolved the key (name only — never the key value). The returned
// keySource is propagated to the API handler so /health can surface it.
func loadAndGuardEncryptionKey(ctx context.Context, resolver secrets.Resolver) (key []byte, keySource string, err error) {
	encKey, source, err := credentials.LoadKey(ctx, resolver)
	if err != nil {
		return nil, "", fmt.Errorf("failed to load credential encryption key: %w", err)
	}
	// Defense in depth: LoadKey already gates this, but a guard here protects
	// against a future regression where LoadKey is changed to silently fall
	// back again.
	if bytes.Equal(encKey, credentials.DevKey()) && os.Getenv(credentials.EnvAllowDev) != "1" {
		return nil, "", fmt.Errorf("credentials: refusing to start with all-zero dev key (set %s=1 for local dev only)", credentials.EnvAllowDev)
	}
	logging.Infof("credentials: loaded encryption key via %s", source)
	return encKey, source, nil
}

// buildAdminPasswordSyncCallback returns a callback that syncs the admin user's
// password to the secret manager when it changes. Returns nil if the required
// environment variables (ADMIN_PASSWORD_SECRET, ADMIN_EMAIL) are not set or
// the secret resolver is nil.
func buildAdminPasswordSyncCallback(store auth.StoreInterface, resolver secrets.Resolver) func(ctx context.Context, userID, newPassword string) {
	adminPasswordSecret := os.Getenv("ADMIN_PASSWORD_SECRET")
	adminEmail := os.Getenv("ADMIN_EMAIL")

	if adminPasswordSecret == "" || resolver == nil || adminEmail == "" {
		return nil
	}

	return func(ctx context.Context, userID, newPassword string) {
		user, err := store.GetUserByID(ctx, userID)
		if err != nil {
			// Cannot tell whether this was the admin, so the secret manager copy
			// may now be stale. Say so instead of skipping silently.
			logging.Errorf("Admin password sync skipped: could not load user %s: %v; if this user is the admin, secret %q may no longer match the database password", userID, err, adminPasswordSecret)
			return
		}
		if user == nil || user.Email != adminEmail {
			return
		}
		if err := resolver.PutSecret(ctx, adminPasswordSecret, newPassword); err != nil {
			// The password change is already committed to the database, so the
			// two copies have now diverged. ERROR, not WARN: anything recovering
			// from the secret manager copy gets the old password (#199). The
			// usual cause is a runtime identity without write access to this
			// one secret.
			logging.Errorf("Admin password sync FAILED: secret %q no longer matches the database password (check the runtime identity can write this secret): %v", adminPasswordSecret, err)
			return
		}
		logging.Infof("Admin password synced to secret manager")
	}
}

// Close gracefully shuts down the application.
func (app *Application) Close() error {
	logging.Infof("Shutting down CUDly Server...")

	app.lifeMu.Lock()
	if app.lifeCancel != nil {
		app.lifeCancel()
	}
	app.lifeMu.Unlock()

	// Close database connection if using PostgreSQL
	if app.DB != nil {
		logging.Infof("Closing database connection...")
		app.DB.Close()
		logging.Infof("Database connection closed successfully")
	}

	return nil
}

// initConfigStore initializes the configuration store using PostgreSQL
// Connection is deferred (lazy init) until first request to avoid Lambda ENI issues.
func initConfigStore(ctx context.Context) (*database.Config, secrets.Resolver, error) {
	// Require PostgreSQL configuration
	if os.Getenv("DB_HOST") == "" {
		return nil, nil, fmt.Errorf("database configuration required: DB_HOST must be set")
	}

	logging.Infof("Preparing PostgreSQL configuration store (lazy initialization)...")

	// Initialize secret resolver
	secretResolver, err := secrets.NewResolver(ctx, secrets.LoadConfigFromEnv())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create secret resolver: %w", err)
	}

	// Load database config from environment
	dbConfig, err := database.LoadFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load database config: %w", err)
	}

	logging.Infof("PostgreSQL config loaded (will connect on first request): %s:%d", dbConfig.Host, dbConfig.Port)

	// The config store itself is created lazily on first request (not here), so
	// callers wire a nil ConfigStore into ExternalDeps. Deferring the connection
	// avoids connecting during Lambda init when the ENI isn't ready.
	return dbConfig, secretResolver, nil
}
