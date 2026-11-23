// Package config provides configuration management using PostgreSQL.
package config

import (
	"fmt"
	"math"
	"net/mail"
	"sort"
	"strings"
)

// ValidProviders lists all supported cloud providers.
var ValidProviders = []string{"aws", "azure", "gcp"}

// ValidPaymentOptions lists the AWS-canonical payment options. Kept for
// backwards compatibility; prefer ValidPaymentOptionsByProvider for
// provider-aware validation.
var ValidPaymentOptions = []string{"no-upfront", "partial-upfront", "all-upfront"}

// ValidPaymentOptionsByProvider maps each provider to the payment option
// tokens it accepts. Each provider's set is the canonical set verified
// against the provider's service-client switch statements:
//
//   - aws:   {no-upfront, partial-upfront, all-upfront}: the three RI/SP
//     billing tiers exposed by AWS APIs.
//
//   - azure: {upfront, monthly}: verified against the 7 Azure service-client
//     switches in providers/azure/services/{compute,cache,cosmosdb,database,
//     search,synapse,managedredis}/client.go. (The savingsplans client mirrors
//     AWS's three-tier set, but Azure savings-plan recs are not currently
//     emitted; GetRecommendations returns []. The canonical set follows the
//     only path that emits today.)
//
//   - gcp:   {monthly}: GCP CUDs are billed monthly across the commitment
//     term; there is no upfront billing tier. See
//     providers/gcp/services/computeengine/client.go:buildCommitmentRequests
//     which takes only a Plan (TWELVE_MONTH/THIRTY_SIX_MONTH) and never reads
//     PaymentOption.
//
// Cross-provider tokens are rejected loudly by the validator. Legacy AWS-style
// tokens emitted by older code paths are canonicalized via NormalizePaymentOption
// at the rec-emission boundary BEFORE reaching this validator (see
// internal/scheduler/scheduler.go:convertRecommendations).
var ValidPaymentOptionsByProvider = map[string][]string{
	"aws":   {"no-upfront", "partial-upfront", "all-upfront"},
	"azure": {"upfront", "monthly"},
	"gcp":   {"monthly"},
}

// validPaymentOptionsUnion is the union of all provider payment option sets,
// used for global-config default validation where no provider context is
// available. Accepts any token that is valid for at least one provider.
// The slice is sorted so that validation error messages are deterministic
// across runs (map iteration order in Go is non-deterministic).
var validPaymentOptionsUnion = func() []string {
	seen := map[string]bool{}
	var all []string
	for _, opts := range ValidPaymentOptionsByProvider {
		for _, o := range opts {
			if !seen[o] {
				seen[o] = true
				all = append(all, o)
			}
		}
	}
	sort.Strings(all)
	return all
}()

// validPaymentOptionsFor returns the provider-canonical payment option slice
// for the given provider (lowercase). Returns nil when the provider is unknown.
func validPaymentOptionsFor(provider string) []string {
	return ValidPaymentOptionsByProvider[provider]
}

// PaymentSchedule is the cash-flow shape a payment-option token implies,
// stripped of the provider-specific spelling of that token. Two tokens that
// map to the same PaymentSchedule bill the customer identically; only the
// word differs (AWS spells all-upfront what Azure spells upfront, and AWS
// spells no-upfront what Azure and GCP spell monthly).
//
// It exists so callers can tell a rename apart from a real change when
// NormalizePaymentOption rewrites a token: a rename is bookkeeping, a real
// change moves the customer's money and has to be disclosed (#1503).
type PaymentSchedule string

const (
	// PaymentScheduleUpfront: the whole commitment is charged once, at purchase.
	PaymentScheduleUpfront PaymentSchedule = "upfront"
	// PaymentSchedulePartialUpfront: part is charged at purchase, the rest recurs.
	PaymentSchedulePartialUpfront PaymentSchedule = "partial-upfront"
	// PaymentScheduleRecurring: nothing is charged at purchase; the commitment
	// is billed per period across the term.
	PaymentScheduleRecurring PaymentSchedule = "recurring"
	// PaymentScheduleUnknown: the token is not one of the modeled schedules.
	// Two unrecognized tokens compare equal under this classification, so
	// callers that must distinguish them have to compare the raw tokens too.
	// In practice unmapped tokens never reach a coercion comparison:
	// NormalizePaymentOption returns ok=false for them and the validator
	// rejects them at the next boundary.
	PaymentScheduleUnknown PaymentSchedule = "unknown"
)

// PaymentScheduleFor classifies a payment-option token by the billing
// schedule it implies. The token must already be lowercased and trimmed
// (validatePurchaseRecommendation and NormalizePaymentOption both work on
// such tokens).
func PaymentScheduleFor(token string) PaymentSchedule {
	switch token {
	case "all-upfront", "upfront":
		return PaymentScheduleUpfront
	case "partial-upfront":
		return PaymentSchedulePartialUpfront
	case "no-upfront", "monthly":
		return PaymentScheduleRecurring
	default:
		return PaymentScheduleUnknown
	}
}

// PaymentCoercionChangesSchedule reports whether rewriting raw to canonical
// actually changes what the customer pays and when, as opposed to merely
// renaming the same schedule into the target provider's vocabulary.
//
// Azure "all-upfront" -> "upfront" and Azure/GCP "no-upfront" -> "monthly"
// are renames: identical cash flow, different spelling. Azure
// "partial-upfront" -> "monthly" and GCP "upfront" -> "monthly" are real
// changes: the customer is billed on a schedule they did not ask for.
//
// Only real changes are worth putting in front of a user (#1503). The
// fan-out purchase modal submits the AWS-style "all-upfront" for Azure
// buckets by construction (frontend/src/lib/purchase-compatibility.ts:
// paymentOptionsFor), so treating every rewrite as a change would fire a
// "billing schedule adjusted" warning on the ordinary Azure upfront
// purchase and train users to dismiss the one notice that matters.
func PaymentCoercionChangesSchedule(raw, canonical string) bool {
	return PaymentScheduleFor(raw) != PaymentScheduleFor(canonical)
}

// ValidOfferingClasses lists the accepted EC2 RI offering class values for
// GlobalConfig. The empty string is also accepted (maps to "convertible" at
// purchase time to preserve pre-694 behavior).
var ValidOfferingClasses = []string{"convertible", "standard"}

// RIExchangeModeManual and RIExchangeModeAuto are the only accepted
// ri_exchange_mode values.
const (
	RIExchangeModeManual = "manual"
	RIExchangeModeAuto   = "auto"
)

// IsValidRIExchangeMode reports whether mode is exactly "manual" or "auto".
// The match is case-sensitive and does not trim, because the RI exchange
// consumer compares the literal values: "Auto", "" or " auto" are not
// equivalent to "auto" there.
func IsValidRIExchangeMode(mode string) bool {
	return mode == RIExchangeModeManual || mode == RIExchangeModeAuto
}

// ValidRampScheduleTypes lists all supported ramp schedule types.
var ValidRampScheduleTypes = []string{"immediate", "weekly", "monthly", "custom"}

// ValidCollectionSchedules lists all valid collection schedule values.
var ValidCollectionSchedules = []string{"", "hourly", "daily", "weekly"}

// Validate validates the GlobalConfig.
func (c *GlobalConfig) Validate() error {
	if err := c.validateProviders(); err != nil {
		return err
	}
	if err := c.validateNotificationEmail(); err != nil {
		return err
	}
	if err := validateGlobalTerm(c.DefaultTerm); err != nil {
		return err
	}
	if err := validatePaymentOption(c.DefaultPayment); err != nil {
		return err
	}
	if err := validateCoverage(c.DefaultCoverage); err != nil {
		return err
	}
	if err := c.validateScheduleAndNotifications(); err != nil {
		return err
	}
	if err := validateOfferingClass(c.OfferingClass); err != nil {
		return err
	}
	if err := c.validateRIExchangeLimits(); err != nil {
		return err
	}
	return c.validateRecommendationsFields()
}

// MaxRIExchangeUtilizationThreshold is the upper bound of
// ri_exchange_utilization_threshold, a percentage.
const MaxRIExchangeUtilizationThreshold = 100.0

// ValidateRIExchangeUtilizationThreshold is the rule for a utilization threshold
// a caller writes: above 0 and at most MaxRIExchangeUtilizationThreshold. 0 is
// refused because the store replaces a stored 0 with its 95 default, so
// accepting it would silently apply a different threshold. Both write routes
// (PUT /api/ri-exchange/config and PUT /api/config) use it; name is the field
// as that route's caller knows it.
func ValidateRIExchangeUtilizationThreshold(name string, t float64) error {
	if math.IsNaN(t) || t <= 0 || t > MaxRIExchangeUtilizationThreshold {
		return fmt.Errorf("%s must be greater than 0 and at most %v, got: %v", name, MaxRIExchangeUtilizationThreshold, t)
	}
	return nil
}

// validateRIExchangeLimits rejects an RI-exchange money cap that is negative
// or not finite, and a utilization threshold outside
// [0, MaxRIExchangeUtilizationThreshold] (#120). PUT /api/config unmarshals
// onto the whole GlobalConfig, so without this a negative daily cap reached
// the store unchecked. 0 stays valid for every field here: a 0 cap is the "not
// configured" state the exchange guard refuses to run under, and this check
// also runs on stored rows, so a legacy 0 threshold must not block unrelated
// saves. A threshold the caller WRITES is held to
// ValidateRIExchangeUtilizationThreshold by the write routes.
func (c *GlobalConfig) validateRIExchangeLimits() error {
	caps := []struct {
		name  string
		value float64
	}{
		{"ri_exchange_max_per_exchange_usd", c.RIExchangeMaxPerExchangeUSD},
		{"ri_exchange_max_daily_usd", c.RIExchangeMaxDailyUSD},
	}
	for _, cp := range caps {
		if math.IsNaN(cp.value) || math.IsInf(cp.value, 0) || cp.value < 0 {
			return fmt.Errorf("%s must be a finite amount >= 0, got: %v", cp.name, cp.value)
		}
	}
	t := c.RIExchangeUtilizationThreshold
	if math.IsNaN(t) || t < 0 || t > MaxRIExchangeUtilizationThreshold {
		return fmt.Errorf("ri_exchange_utilization_threshold must be between 0 and %v, got: %v", MaxRIExchangeUtilizationThreshold, t)
	}
	return nil
}

// validateScheduleAndNotifications validates collection_schedule and
// notification_days_before. Extracted to keep Validate's cyclomatic
// complexity under the project limit.
func (c *GlobalConfig) validateScheduleAndNotifications() error {
	if !isValidCollectionSchedule(c.CollectionSchedule) {
		return fmt.Errorf("invalid collection_schedule: %q (valid: hourly, daily, weekly)", c.CollectionSchedule)
	}
	if c.NotificationDaysBefore < 0 || c.NotificationDaysBefore > MaxNotificationDaysBefore {
		return fmt.Errorf("notification_days_before must be between 0 and %d, got: %d", MaxNotificationDaysBefore, c.NotificationDaysBefore)
	}
	return c.validateGracePeriodDays()
}

// validateRecommendationsFields validates the recommendation-cycle parameters
// added in #301 (cache-staleness threshold and AWS Cost Explorer lookback).
// Extracted to keep Validate's cyclomatic complexity under the project limit.
func (c *GlobalConfig) validateRecommendationsFields() error {
	if err := c.validateRecommendationsCacheStaleHours(); err != nil {
		return err
	}
	if err := c.validateRecommendationsLookbackDays(); err != nil {
		return err
	}
	return c.validatePurchaseDelayHours()
}

// validatePurchaseDelayHours validates the Gmail-style pre-fire delay
// (issue #291 wave-2). Valid range: [0, MaxPurchaseDelayHours]. 0 means
// immediate-execute (backward compat).
func (c *GlobalConfig) validatePurchaseDelayHours() error {
	if c.PurchaseDelayHours < 0 || c.PurchaseDelayHours > MaxPurchaseDelayHours {
		return fmt.Errorf("purchase_delay_hours must be between 0 and %d, got: %d (0 = immediate execute)", MaxPurchaseDelayHours, c.PurchaseDelayHours)
	}
	return nil
}

// validateRecommendationsCacheStaleHours validates the stale-while-revalidate
// cache age threshold. 0 disables background refresh; negative values are
// rejected. Maximum is MaxRecommendationsCacheStaleHours (1 year).
func (c *GlobalConfig) validateRecommendationsCacheStaleHours() error {
	if c.RecommendationsCacheStaleHours < 0 || c.RecommendationsCacheStaleHours > MaxRecommendationsCacheStaleHours {
		return fmt.Errorf("recommendations_cache_stale_hours must be between 0 and %d, got: %d (0 = disable auto-refresh)", MaxRecommendationsCacheStaleHours, c.RecommendationsCacheStaleHours)
	}
	return nil
}

// validateRecommendationsLookbackDays validates the AWS Cost Explorer lookback
// window. Only the enum values {7, 30, 60} are accepted (LookbackPeriodInDays).
// A value of 0 means "unset / use the backend default" and is always accepted;
// this matches the zero-value of the Go int field when the DB row predates the
// column (the migration adds DEFAULT 7, but in-memory structs constructed
// without explicitly setting the field carry 0).
func (c *GlobalConfig) validateRecommendationsLookbackDays() error {
	if c.RecommendationsLookbackDays == 0 {
		// Unset → backend defaults to DefaultRecommendationsLookbackDays.
		return nil
	}
	for _, valid := range ValidRecommendationsLookbackDays {
		if c.RecommendationsLookbackDays == valid {
			return nil
		}
	}
	validStrs := make([]string, len(ValidRecommendationsLookbackDays))
	for i, v := range ValidRecommendationsLookbackDays {
		validStrs[i] = fmt.Sprintf("%d", v)
	}
	return fmt.Errorf("recommendations_lookback_days must be one of [%s], got: %d", strings.Join(validStrs, ", "), c.RecommendationsLookbackDays)
}

// validateGracePeriodDays validates the per-provider grace-period map.
// Keys must be known provider slugs; values must be in [0, MaxGracePeriodDays].
// A nil / empty map is always valid (falls back to the default).
func (c *GlobalConfig) validateGracePeriodDays() error {
	for provider, days := range c.GracePeriodDays {
		if !isValidProvider(provider) {
			return fmt.Errorf("grace_period_days: invalid provider key %q (valid: %s)", provider, strings.Join(ValidProviders, ", "))
		}
		if days < 0 || days > MaxGracePeriodDays {
			return fmt.Errorf("grace_period_days[%s] must be between 0 and %d, got: %d", provider, MaxGracePeriodDays, days)
		}
	}
	return nil
}

// validateProviders checks that all enabled providers are valid.
func (c *GlobalConfig) validateProviders() error {
	for _, p := range c.EnabledProviders {
		if !isValidProvider(p) {
			return fmt.Errorf("invalid provider: %s (valid: %s)", p, strings.Join(ValidProviders, ", "))
		}
	}
	return nil
}

// validateNotificationEmail validates the notification email format if provided.
func (c *GlobalConfig) validateNotificationEmail() error {
	if c.NotificationEmail != nil && *c.NotificationEmail != "" {
		if _, err := mail.ParseAddress(*c.NotificationEmail); err != nil {
			return fmt.Errorf("invalid notification email format: %s", *c.NotificationEmail)
		}
	}
	return nil
}

// validateTerm validates that the term is 1 or 3 years (or 0 for not set - service-level only).
func validateTerm(term int) error {
	if term != 0 && term != 1 && term != 3 {
		return fmt.Errorf("default term must be 1 or 3 years, got: %d", term)
	}
	return nil
}

// validateGlobalTerm validates that the term is 1 or 3 years (0 is not allowed for global config).
func validateGlobalTerm(term int) error {
	if term != 1 && term != 3 {
		return fmt.Errorf("default term must be 1 or 3 years, got: %d", term)
	}
	return nil
}

// validatePaymentOption validates that the payment option is in the union of
// all provider sets. Used by GlobalConfig.Validate where no provider context
// is available; any token valid for at least one provider is accepted.
func validatePaymentOption(payment string) error {
	if payment != "" && !isValidPaymentOption(payment) {
		return fmt.Errorf("invalid payment option: %s (valid: %s)", payment, strings.Join(validPaymentOptionsUnion, ", "))
	}
	return nil
}

// validateCoverage validates that coverage is within acceptable range.
func validateCoverage(coverage float64) error {
	if coverage < MinCoverage || coverage > MaxCoverage {
		return fmt.Errorf("default coverage must be between %d and %d, got: %.2f", MinCoverage, MaxCoverage, coverage)
	}
	return nil
}

// Validate validates the ServiceConfig.
func (c *ServiceConfig) Validate() error {
	if err := c.validateProvider(); err != nil {
		return err
	}
	if err := c.validateService(); err != nil {
		return err
	}
	if err := c.validateTerm(); err != nil {
		return err
	}
	if err := c.validatePayment(); err != nil {
		return err
	}
	if err := c.validateMinCount(); err != nil {
		return err
	}
	return c.validateConfigCoverage()
}

func (c *ServiceConfig) validateMinCount() error {
	if c.MinCount < 0 {
		return fmt.Errorf("min_count must be 0 (no filter) or a positive number, got: %d", c.MinCount)
	}
	if c.MinCount > MaxServiceMinCount {
		return fmt.Errorf("min_count (%d) exceeds reasonable limit of %d", c.MinCount, MaxServiceMinCount)
	}
	return nil
}

func (c *ServiceConfig) validateProvider() error {
	if c.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if !isValidProvider(c.Provider) {
		return fmt.Errorf("invalid provider: %s (valid: %s)", c.Provider, strings.Join(ValidProviders, ", "))
	}
	return nil
}

func (c *ServiceConfig) validateService() error {
	if c.Service == "" {
		return fmt.Errorf("service is required")
	}
	return nil
}

func (c *ServiceConfig) validateTerm() error {
	if c.Term != 0 && c.Term != 1 && c.Term != 3 {
		return fmt.Errorf("term must be 1 or 3 years, got: %d", c.Term)
	}
	return nil
}

func (c *ServiceConfig) validatePayment() error {
	if c.Payment == "" {
		return nil
	}
	// Provider-canonical validation: each provider accepts only its own token
	// set. A cross-provider token (e.g. "all-upfront" on an Azure service) is
	// rejected even though that token is valid somewhere else.
	opts := validPaymentOptionsFor(c.Provider)
	if opts == nil {
		// Provider was already validated; unknown provider here is a bug.
		return fmt.Errorf("internal error: no payment options defined for provider %q", c.Provider)
	}
	for _, v := range opts {
		if c.Payment == v {
			return nil
		}
	}
	return fmt.Errorf("invalid payment option: %s (valid for %s: %s)", c.Payment, c.Provider, strings.Join(opts, ", "))
}

func (c *ServiceConfig) validateConfigCoverage() error {
	if c.Coverage < MinCoverage || c.Coverage > MaxCoverage {
		return fmt.Errorf("coverage must be between %d and %d, got: %.2f", MinCoverage, MaxCoverage, c.Coverage)
	}
	return nil
}

// Helper functions

func isValidProvider(p string) bool {
	for _, valid := range ValidProviders {
		if p == valid {
			return true
		}
	}
	return false
}

// isValidPaymentOption reports whether p is a valid payment option for any
// provider. It checks against the union of all provider payment option sets.
func isValidPaymentOption(p string) bool {
	for _, valid := range validPaymentOptionsUnion {
		if p == valid {
			return true
		}
	}
	return false
}

func isValidCollectionSchedule(s string) bool {
	for _, valid := range ValidCollectionSchedules {
		if s == valid {
			return true
		}
	}
	return false
}

func isValidRampScheduleType(t string) bool {
	for _, valid := range ValidRampScheduleTypes {
		if t == valid {
			return true
		}
	}
	return false
}

// ValidateRampScheduleEnv validates a ramp-schedule value read from an
// environment variable (e.g. DEFAULT_RAMP_SCHEDULE). Empty string is
// always valid ("use the purchase manager's built-in default"). Non-empty
// values must be in ValidRampScheduleTypes.
// Called by the server startup boundary so misconfiguration is caught at
// boot time rather than silently propagated into purchases (issue #1026).
func ValidateRampScheduleEnv(val string) error {
	if val == "" {
		return nil
	}
	if !isValidRampScheduleType(val) {
		return fmt.Errorf("value %q is not a recognized ramp schedule type (valid: %s)", val, strings.Join(ValidRampScheduleTypes, ", "))
	}
	return nil
}

// validateOfferingClass validates the EC2 RI offering class value.
// The empty string is accepted (maps to "convertible" at purchase time).
// Only lowercase "convertible" and "standard" are valid non-empty values;
// the check is case-sensitive to match how resolveOfferingClassType parses
// the field in providers/aws/services/ec2/client.go.
func validateOfferingClass(oc string) error {
	if oc == "" {
		return nil
	}
	for _, valid := range ValidOfferingClasses {
		if oc == valid {
			return nil
		}
	}
	return fmt.Errorf("invalid offering_class: %q (valid: %s)", oc, strings.Join(ValidOfferingClasses, ", "))
}
