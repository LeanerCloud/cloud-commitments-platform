package server

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/secrets"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/server/scheduledauth"
)

// resolveScheduledTaskSecret resolves SCHEDULED_TASK_SECRET_NAME to its real
// value via the configured SecretResolver (Azure Key Vault / AWS Secrets
// Manager) when possible. Falls back to cfg.ScheduledTaskSecret (plaintext
// SCHEDULED_TASK_SECRET env var) if the resolver is absent or the lookup
// fails.
//
// The second return value is non-nil only when a SecretName was configured
// AND the resolver failed. Callers in bearer mode MUST propagate this error
// so startup fails with the real cause (e.g. "failed to resolve
// scheduled-task secret from <name>: <err>") rather than the misleading
// downstream "bearer mode requires SCHEDULED_TASK_SECRET" that would
// otherwise surface when the empty fallback reaches buildScheduledAuth (04-M4).
//
// Security note: if both SCHEDULED_TASK_SECRET (plaintext) and
// SCHEDULED_TASK_SECRET_NAME (secret-store path) are set, we warn loudly
// because the plaintext value is visible in Lambda env / Terraform state.
// The secret-store path is always preferred when both are present.
func resolveScheduledTaskSecret(ctx context.Context, cfg ApplicationConfig, resolver secrets.Resolver) (string, error) {
	if cfg.ScheduledTaskSecretName != "" && cfg.ScheduledTaskSecret != "" {
		logging.Warnf("SECURITY WARNING: both SCHEDULED_TASK_SECRET (plaintext) and " +
			"SCHEDULED_TASK_SECRET_NAME are set. The plaintext value is visible in " +
			"Lambda environment variables and Terraform state. " +
			"Remove SCHEDULED_TASK_SECRET and rely on SCHEDULED_TASK_SECRET_NAME only.")
	}

	if cfg.ScheduledTaskSecretName == "" || resolver == nil {
		return cfg.ScheduledTaskSecret, nil
	}
	resolved, err := resolver.GetSecret(ctx, cfg.ScheduledTaskSecretName)
	if err != nil {
		logging.Warnf("scheduled task secret resolution failed for %q: %v (falling back to SCHEDULED_TASK_SECRET)", cfg.ScheduledTaskSecretName, err)
		return cfg.ScheduledTaskSecret, err
	}
	return resolved, nil
}

// envSourceOS implements scheduledauth.EnvSource against os.Getenv. The
// scheduledauth package owns the env var names and parse rules so they
// stay aligned with Terraform.
type envSourceOS struct{}

func (envSourceOS) Get(key string) string { return os.Getenv(key) }

// buildScheduledAuthFromConfig wires up the /api/scheduled/* validator from
// a pre-loaded scheduledauth.Config. The bearer secret is injected from cfg
// rather than re-read from env — in production cfg.ScheduledTaskSecret was
// already resolved from Key Vault / Secrets Manager by the caller.
func buildScheduledAuthFromConfig(cfg ApplicationConfig, saCfg scheduledauth.Config) (*scheduledauth.Validator, error) {
	// In bearer mode, override the env-supplied secret with the one
	// already resolved from KV / SM. LoadConfig reads SCHEDULED_TASK_SECRET
	// directly which is fine for local dev where the env carries the
	// plaintext, but in prod cfg.ScheduledTaskSecret is the authoritative
	// value (resolved by resolveScheduledTaskSecret upstream).
	if saCfg.Mode == scheduledauth.ModeBearer {
		saCfg.Bearer = cfg.ScheduledTaskSecret
	}
	return scheduledauth.New(saCfg)
}

// initScheduledAuth loads the scheduledauth config, resolves the bearer
// secret (failing fast in bearer mode if the resolver errors), builds the
// validator, and warms up JWKS. Extracted from NewApplicationFromDeps to
// keep its cyclomatic complexity within the project limit (04-M4).
func initScheduledAuth(ctx context.Context, cfg *ApplicationConfig, resolver secrets.Resolver) (*scheduledauth.Validator, error) {
	saCfg, err := scheduledauth.LoadConfig(envSourceOS{})
	if err != nil {
		return nil, fmt.Errorf("scheduled-task auth init: %w", err)
	}

	resolvedSecret, secretErr := resolveScheduledTaskSecret(ctx, *cfg, resolver)
	if secretErr != nil && saCfg.Mode == scheduledauth.ModeBearer {
		return nil, fmt.Errorf("failed to resolve scheduled-task secret from %q: %w", cfg.ScheduledTaskSecretName, secretErr)
	}
	cfg.ScheduledTaskSecret = resolvedSecret

	v, err := buildScheduledAuthFromConfig(*cfg, saCfg)
	if err != nil {
		return nil, fmt.Errorf("scheduled-task auth init: %w", err)
	}

	warmCtx, warmCancel := context.WithTimeout(ctx, 5*time.Second)
	v.Warmup(warmCtx)
	warmCancel()
	return v, nil
}
