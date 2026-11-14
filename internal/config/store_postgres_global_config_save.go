package config

import (
	"context"
	"encoding/json"
	"fmt"
)

// saveGlobalConfigWith upserts the global_config singleton via q (the pool or a
// transaction).
func saveGlobalConfigWith(ctx context.Context, q globalConfigExecutor, config *GlobalConfig) error {
	// Ensure EnabledProviders is never nil (empty slice is ok, nil is not)
	if config.EnabledProviders == nil {
		config.EnabledProviders = []string{}
	}

	planTerm, err := planTermUpsertFragments(ctx, q, config.ExperimentalGCPPlanTerm)
	if err != nil {
		return err
	}

	query := `
		INSERT INTO global_config (
			id, enabled_providers, notification_email, approval_required,
			default_term, default_payment, default_coverage, default_ramp_schedule,
			ri_exchange_enabled, ri_exchange_mode, ri_exchange_utilization_threshold,
			ri_exchange_max_per_exchange_usd, ri_exchange_max_daily_usd, ri_exchange_lookback_days,
			auto_collect, collection_schedule, notification_days_before,
			grace_period_days,
			recommendations_cache_stale_hours, recommendations_lookback_days,
			purchase_delay_hours, laddering_enabled, ladder_execution_enabled, offering_class,
			require_different_approver, auto_purchase_enabled,
			auto_purchase_changed_by, auto_purchase_changed_at, auto_purchase_changed_via,
			ladder_execution_changed_by, ladder_execution_changed_at, ladder_execution_changed_via` + planTerm.col + `
		) VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31` + planTerm.val + `)
		ON CONFLICT (id) DO UPDATE SET
			enabled_providers = $1,
			notification_email = $2,
			approval_required = $3,
			default_term = $4,
			default_payment = $5,
			default_coverage = $6,
			default_ramp_schedule = $7,
			ri_exchange_enabled = $8,
			ri_exchange_mode = $9,
			ri_exchange_utilization_threshold = $10,
			ri_exchange_max_per_exchange_usd = $11,
			ri_exchange_max_daily_usd = $12,
			ri_exchange_lookback_days = $13,
			auto_collect = $14,
			collection_schedule = $15,
			notification_days_before = $16,
			grace_period_days = $17,
			recommendations_cache_stale_hours = $18,
			recommendations_lookback_days = $19,
			purchase_delay_hours = $20,
			laddering_enabled = $21,
			ladder_execution_enabled = $22,
			offering_class = $23,
			require_different_approver = $24,
			auto_purchase_enabled = $25,
			auto_purchase_changed_by = $26,
			auto_purchase_changed_at = $27,
			auto_purchase_changed_via = $28,
			ladder_execution_changed_by = $29,
			ladder_execution_changed_at = $30,
			ladder_execution_changed_via = $31` + planTerm.set + `,
			updated_at = NOW()
	`

	// Use local copies for defaults so we don't mutate the caller's struct.
	riExchangeMode := config.RIExchangeMode
	if riExchangeMode == "" {
		riExchangeMode = "manual"
	}
	riExchangeLookbackDays := config.RIExchangeLookbackDays
	if riExchangeLookbackDays == 0 {
		riExchangeLookbackDays = 30
	}
	recommendationsLookbackDays := config.RecommendationsLookbackDays
	if recommendationsLookbackDays == 0 {
		// Validate() treats 0 as "unset → use default". Mirror that here so
		// the DB never stores 0, matching the ErrNoRows default path.
		recommendationsLookbackDays = DefaultRecommendationsLookbackDays
	}
	riExchangeUtilizationThreshold := config.RIExchangeUtilizationThreshold
	if riExchangeUtilizationThreshold == 0 {
		riExchangeUtilizationThreshold = 95.0
	}

	// Marshal GracePeriodDays -> JSON text column. Empty map encodes as
	// "{}" so the DB column is never NULL and GetGlobalConfig can
	// treat "{}" and "" uniformly as "no explicit entries".
	gracePeriodJSON, err := encodeGracePeriodDays(config.GracePeriodDays)
	if err != nil {
		return err
	}

	// Default offering_class to "convertible" when unset so the DB column
	// never stores an empty string (the NOT NULL DEFAULT 'convertible'
	// column handles inserts, but upserts overwrite with whatever we pass).
	offeringClass := config.OfferingClass
	if offeringClass == "" {
		offeringClass = "convertible"
	}

	args := append(make([]any, 0, 32),
		config.EnabledProviders,
		config.NotificationEmail,
		config.ApprovalRequired,
		config.DefaultTerm,
		config.DefaultPayment,
		config.DefaultCoverage,
		config.DefaultRampSchedule,
		config.RIExchangeEnabled,
		riExchangeMode,
		riExchangeUtilizationThreshold,
		config.RIExchangeMaxPerExchangeUSD,
		config.RIExchangeMaxDailyUSD,
		riExchangeLookbackDays,
		config.AutoCollect,
		config.CollectionSchedule,
		config.NotificationDaysBefore,
		gracePeriodJSON,
		config.RecommendationsCacheStaleHours,
		recommendationsLookbackDays,
		config.PurchaseDelayHours,
		config.LadderingEnabled,
		config.LadderExecutionEnabled,
		offeringClass,
		config.RequireDifferentApprover,
		config.AutoPurchaseEnabled,
		config.AutoPurchaseChangedBy,
		config.AutoPurchaseChangedAt,
		config.AutoPurchaseChangedVia,
		config.LadderExecutionChangedBy,
		config.LadderExecutionChangedAt,
		config.LadderExecutionChangedVia,
	)
	args = append(args, planTerm.args...)

	_, err = q.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to save global config: %w", err)
	}

	return nil
}

// planTermFragments are the upsert pieces for experimental_gcp_plan_term.
type planTermFragments struct {
	col, val, set string
	args          []any
}

// planTermUpsertFragments returns the upsert pieces for the column. main
// deploys before prod migrations are applied: below 000173 the column does not
// exist, so it is left out of the upsert and the setting is not persisted
// until the migration lands.
func planTermUpsertFragments(ctx context.Context, q rowQuerier, enabled bool) (planTermFragments, error) {
	ready, err := migrationApplied(ctx, q, planTermMigrationVersion)
	if err != nil || !ready {
		return planTermFragments{}, err
	}
	return planTermFragments{
		col:  ", experimental_gcp_plan_term",
		val:  ", $32",
		set:  ",\n\t\t\texperimental_gcp_plan_term = $32",
		args: []any{enabled},
	}, nil
}

// encodeGracePeriodDays marshals the per-service grace windows; an empty map is
// "{}" so the column is never NULL.
func encodeGracePeriodDays(days map[string]int) (string, error) {
	if len(days) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(days)
	if err != nil {
		return "", fmt.Errorf("failed to encode grace_period_days JSON: %w", err)
	}
	return string(b), nil
}
