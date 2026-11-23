package server

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/runtime"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/secrets"
)

// ApplicationConfig holds all env-based configuration for the application.
type ApplicationConfig struct {
	// RecommendationSchedule is RECOMMENDATION_SCHEDULE: the schedule
	// Terraform gave the deployment's recommendation-collection trigger
	// (EventBridge rate()/cron() or five-field cron), empty when scheduled
	// collection is off. Only read to report the next run (issue #756).
	RecommendationSchedule  string
	IssuerURL               string
	ScheduledTaskSecretName string
	Version                 string
	DefaultRampSchedule     string
	CORSAllowedOrigin       string
	DashboardURL            string
	DashboardBucket         string
	APIKeySecretARN         string
	ScheduledTaskSecret     string
	Analytics               AnalyticsConfig
	NotificationDaysBefore  int
	DefaultCoverage         float64
	DefaultTerm             int
	// TrustedProxyHops is the number of proxies that append to
	// X-Forwarded-For before the app (CUDLY_TRUSTED_PROXY_HOPS). 0 ignores
	// the header and uses the peer address.
	TrustedProxyHops int
	// ScheduledPurchaseMaxOverdue is SCHEDULED_PURCHASE_MAX_OVERDUE, the
	// fire and process sweeps' due-row age cap (issue #712). Zero means
	// purchase.DefaultMaxDueRowAge.
	ScheduledPurchaseMaxOverdue time.Duration
	// authBcryptCost is passed to auth.ServiceConfig.BcryptCost. Only tests
	// in this package set it; zero keeps the production bcrypt cost.
	authBcryptCost  int
	EnableDashboard bool
	IsLambda        bool
	// FireSweepOff is FIRE_SWEEP_SCHEDULED=false: Terraform scheduled no
	// fire_scheduled_purchases sweep, so delayed purchases cannot execute
	// (issue #1588). Unset means unknown and leaves saves unrestricted.
	FireSweepOff bool
}

// ExternalDeps holds pre-built external dependencies that require infrastructure.
type ExternalDeps struct {
	EmailSender    email.SenderInterface
	ConfigStore    config.StoreInterface
	DBConfig       *database.Config
	SecretResolver secrets.Resolver
	STSClient      purchase.STSClient
}

// LoadApplicationConfig reads all configuration from environment variables.
func LoadApplicationConfig() (ApplicationConfig, error) {
	version := os.Getenv("VERSION")
	if version == "" {
		version = "dev"
	}
	term := 3
	if raw := os.Getenv("DEFAULT_TERM"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return ApplicationConfig{}, fmt.Errorf("invalid DEFAULT_TERM: %w", err)
		}
		term = value
	}
	coverage := 80.0
	if raw := os.Getenv("DEFAULT_COVERAGE"); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return ApplicationConfig{}, fmt.Errorf("invalid DEFAULT_COVERAGE: %w", err)
		}
		coverage = value
	}
	maxOverdue, err := loadScheduledPurchaseMaxOverdue()
	if err != nil {
		return ApplicationConfig{}, err
	}
	analyticsConfig, err := LoadAnalyticsConfig()
	if err != nil {
		return ApplicationConfig{}, err
	}
	hops, err := parseTrustedProxyHops(os.Getenv("CUDLY_TRUSTED_PROXY_HOPS"))
	if err != nil {
		return ApplicationConfig{}, err
	}

	return ApplicationConfig{
		TrustedProxyHops:            hops,
		Version:                     version,
		NotificationDaysBefore:      getEnvInt("NOTIFICATION_DAYS_BEFORE", 3),
		ScheduledPurchaseMaxOverdue: maxOverdue,
		DefaultTerm:                 term,
		DefaultCoverage:             coverage,
		DefaultRampSchedule:         os.Getenv("DEFAULT_RAMP_SCHEDULE"),
		APIKeySecretARN:             os.Getenv("API_KEY_SECRET_ARN"),
		EnableDashboard:             os.Getenv("ENABLE_DASHBOARD") == "true",
		DashboardBucket:             os.Getenv("DASHBOARD_BUCKET"),
		DashboardURL:                os.Getenv("DASHBOARD_URL"),
		IssuerURL:                   os.Getenv("CUDLY_ISSUER_URL"),
		CORSAllowedOrigin:           os.Getenv("CORS_ALLOWED_ORIGIN"),
		// SCHEDULED_TASK_SECRET (plaintext) is the legacy dev-only path.
		// SCHEDULED_TASK_SECRET_NAME (secret-store name) is preferred in prod;
		// NewApplicationFromDeps resolves it via the SecretResolver at init.
		ScheduledTaskSecret:     os.Getenv("SCHEDULED_TASK_SECRET"),
		ScheduledTaskSecretName: os.Getenv("SCHEDULED_TASK_SECRET_NAME"),
		IsLambda:                runtime.IsLambda(),
		Analytics:               analyticsConfig,
		RecommendationSchedule:  os.Getenv("RECOMMENDATION_SCHEDULE"),
		FireSweepOff:            os.Getenv("FIRE_SWEEP_SCHEDULED") == "false",
	}, nil
}

// resolveOIDCIssuerURL picks the canonical OIDC issuer URL for the
// deployment. CUDLY_ISSUER_URL (set by the infra module to the
// Function URL / Container App URL / Cloud Run URL) wins; DashboardURL
// is the backstop.
func resolveOIDCIssuerURL(cfg ApplicationConfig) string {
	if cfg.IssuerURL != "" {
		return strings.TrimRight(cfg.IssuerURL, "/")
	}
	return strings.TrimRight(cfg.DashboardURL, "/")
}

// loadScheduledPurchaseMaxOverdue parses SCHEDULED_PURCHASE_MAX_OVERDUE, a Go
// duration such as "24h" or "36h": how long after its due time the fire and
// process sweeps may still buy a row (issue #712). Unset returns 0, which the
// purchase manager reads as purchase.DefaultMaxDueRowAge. A malformed or
// non-positive value fails startup rather than silently changing which overdue
// purchases get bought.
func loadScheduledPurchaseMaxOverdue() (time.Duration, error) {
	raw := os.Getenv("SCHEDULED_PURCHASE_MAX_OVERDUE")
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid SCHEDULED_PURCHASE_MAX_OVERDUE: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid SCHEDULED_PURCHASE_MAX_OVERDUE: %q must be a positive duration", raw)
	}
	return d, nil
}

// maxTrustedProxyHops bounds CUDLY_TRUSTED_PROXY_HOPS; no real chain is deeper.
const maxTrustedProxyHops = 8

// parseTrustedProxyHops parses CUDLY_TRUSTED_PROXY_HOPS. Unset means 0 (the
// X-Forwarded-For header is ignored); anything but an integer in 0..8 is a
// startup error.
func parseTrustedProxyHops(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	hops, err := strconv.Atoi(raw)
	if err != nil || hops < 0 || hops > maxTrustedProxyHops {
		return 0, fmt.Errorf("invalid CUDLY_TRUSTED_PROXY_HOPS %q: want an integer 0-%d", raw, maxTrustedProxyHops)
	}
	return hops, nil
}

// validateAppConfigEnvDefaults validates the money-moving env-sourced defaults
// at the startup boundary. DEFAULT_RAMP_SCHEDULE is
// validated here so a typo fails startup (issue #1026).
// Empty values are always valid (means "use the purchase manager's built-in default").
func validateAppConfigEnvDefaults(cfg ApplicationConfig) error {
	if err := config.ValidateRampScheduleEnv(cfg.DefaultRampSchedule); err != nil {
		return fmt.Errorf("invalid DEFAULT_RAMP_SCHEDULE: %w", err)
	}
	return nil
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		result, err := strconv.Atoi(val)
		if err != nil {
			logging.Warnf("WARNING: %s=%q is not a valid integer; using default %d", key, val, defaultVal) // #nosec G706 -- env var value is operator-controlled configuration; logged for diagnostics
			return defaultVal
		}
		return result
	}
	return defaultVal
}
