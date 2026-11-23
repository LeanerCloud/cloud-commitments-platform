package server

import (
	"context"
	"fmt"
	"os"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	awsladder "github.com/LeanerCloud/cloud-commitments-go/providers/aws/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
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
