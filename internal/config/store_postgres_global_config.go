package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ==========================================
// GLOBAL CONFIGURATION
// ==========================================

// globalConfigExecutor is the subset of query methods needed to read and write
// the global_config singleton. It is satisfied by both the pool (dbConn) and a
// pgx.Tx, so the read/write helpers can run either on the pool directly or
// inside the locked transaction opened by UpdateGlobalConfigAtomic.
type globalConfigExecutor interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// globalConfigLockKey is the advisory-lock key that serializes concurrent
// read-modify-write updates to the global_config singleton (updateConfig
// partial PUTs). Derived like the scheduled-task locks (FNV-64a of a
// namespaced string) so it cannot collide with those or the migration lock.
var globalConfigLockKey = func() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("cudly:global_config:singleton"))
	return int64(h.Sum64())
}()

// GetGlobalConfig retrieves the global configuration.
func (s *PostgresStore) GetGlobalConfig(ctx context.Context) (*GlobalConfig, error) {
	return getGlobalConfigFrom(ctx, s.db)
}

// getGlobalConfigFrom reads the global_config singleton via q (the pool or a
// transaction), returning defaults when no row exists yet.
func getGlobalConfigFrom(ctx context.Context, q globalConfigExecutor) (*GlobalConfig, error) {
	query := `
		SELECT enabled_providers, notification_email, approval_required,
		       default_term, default_payment, default_coverage, default_ramp_schedule,
		       ri_exchange_enabled, ri_exchange_mode, ri_exchange_utilization_threshold,
		       ri_exchange_max_per_exchange_usd, ri_exchange_max_daily_usd, ri_exchange_lookback_days,
		       auto_collect, collection_schedule, notification_days_before,
		       grace_period_days,
		       recommendations_cache_stale_hours, recommendations_lookback_days,
		       COALESCE(purchase_delay_hours, 0),
		       COALESCE(laddering_enabled, false),
		       COALESCE(ladder_execution_enabled, false),
		       offering_class,
		       require_different_approver,
		       auto_purchase_enabled,
		       auto_purchase_changed_by, auto_purchase_changed_at, auto_purchase_changed_via,
		       ladder_execution_changed_by, ladder_execution_changed_at, ladder_execution_changed_via
		FROM global_config
		WHERE id = 1
	`

	var config GlobalConfig
	var enabledProviders []string
	var gracePeriodJSON string

	err := q.QueryRow(ctx, query).Scan(
		&enabledProviders,
		&config.NotificationEmail,
		&config.ApprovalRequired,
		&config.DefaultTerm,
		&config.DefaultPayment,
		&config.DefaultCoverage,
		&config.DefaultRampSchedule,
		&config.RIExchangeEnabled,
		&config.RIExchangeMode,
		&config.RIExchangeUtilizationThreshold,
		&config.RIExchangeMaxPerExchangeUSD,
		&config.RIExchangeMaxDailyUSD,
		&config.RIExchangeLookbackDays,
		&config.AutoCollect,
		&config.CollectionSchedule,
		&config.NotificationDaysBefore,
		&gracePeriodJSON,
		&config.RecommendationsCacheStaleHours,
		&config.RecommendationsLookbackDays,
		&config.PurchaseDelayHours,
		&config.LadderingEnabled,
		&config.LadderExecutionEnabled,
		&config.OfferingClass,
		&config.RequireDifferentApprover,
		&config.AutoPurchaseEnabled,
		&config.AutoPurchaseChangedBy,
		&config.AutoPurchaseChangedAt,
		&config.AutoPurchaseChangedVia,
		&config.LadderExecutionChangedBy,
		&config.LadderExecutionChangedAt,
		&config.LadderExecutionChangedVia,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Return default config if none exists.
			// Values must align with DefaultSettings in defaults.go and DB DEFAULT clauses.
			return &GlobalConfig{
				EnabledProviders:               []string{},
				ApprovalRequired:               true,
				DefaultTerm:                    3,
				DefaultPayment:                 "no-upfront",
				DefaultCoverage:                float64(DefaultCoveragePercent),
				DefaultRampSchedule:            RampImmediate,
				RIExchangeMode:                 "manual",
				RIExchangeUtilizationThreshold: 95.0,
				RIExchangeLookbackDays:         30,
				AutoCollect:                    true,
				CollectionSchedule:             "daily",
				NotificationDaysBefore:         3,
				RecommendationsCacheStaleHours: DefaultRecommendationsCacheStaleHours,
				RecommendationsLookbackDays:    DefaultRecommendationsLookbackDays,
				PurchaseDelayHours:             DefaultPurchaseDelayHours,
				OfferingClass:                  "convertible",
			}, nil
		}
		return nil, fmt.Errorf("failed to get global config: %w", err)
	}

	config.EnabledProviders = enabledProviders
	if gracePeriodJSON != "" && gracePeriodJSON != "{}" {
		var gp map[string]int
		if err := json.Unmarshal([]byte(gracePeriodJSON), &gp); err != nil {
			return nil, fmt.Errorf("failed to decode grace_period_days JSON: %w", err)
		}
		config.GracePeriodDays = gp
	}
	return &config, nil
}

// SaveGlobalConfig saves the global configuration.
func (s *PostgresStore) SaveGlobalConfig(ctx context.Context, config *GlobalConfig) error {
	return saveGlobalConfigWith(ctx, s.db, config)
}
