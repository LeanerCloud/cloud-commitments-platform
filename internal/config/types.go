// Package config provides configuration management for CUDly.
package config

import (
	"math"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// GlobalConfig represents the global CUDly configuration.
type GlobalConfig struct {
	AutoPurchaseChangedVia *string `json:"-" db:"auto_purchase_changed_via"`
	NotificationEmail      *string `json:"notification_email,omitempty" dynamodbav:"notification_email,omitempty"`
	// AutoPurchaseChanged{By,At,Via} stamp the last change of the switch
	// (migration 000115): actor, time, credential kind. NULL = never changed.
	// json:"-" so PUT /api/config cannot forge them by unmarshalling onto this
	// struct; the whole-config upsert round-trips them (read, modify, write).
	AutoPurchaseChangedBy     *string    `json:"-" db:"auto_purchase_changed_by"`
	AutoPurchaseChangedAt     *time.Time `json:"-" db:"auto_purchase_changed_at"`
	LadderExecutionChangedVia *string    `json:"-" db:"ladder_execution_changed_via"`
	LadderExecutionChangedAt  *time.Time `json:"-" db:"ladder_execution_changed_at"`
	// LadderExecutionChanged{By,At,Via} stamp the last change of
	// LadderExecutionEnabled (migration 000116); same json:"-" reasoning.
	LadderExecutionChangedBy *string `json:"-" db:"ladder_execution_changed_by"`
	// GracePeriodDays is a per-provider window (in days) during which
	// just-purchased capacity is suppressed from the recommendations
	// list so users don't re-buy the same capacity while the cloud
	// provider's utilization metrics catch up. Keys are provider slugs
	// ("aws", "azure", "gcp"). Missing keys default to DefaultGracePeriodDays
	// (7). An explicit 0 disables suppression for that provider. Use
	// GracePeriodFor to read a specific provider's effective value (it
	// applies the default + safety clamp).
	GracePeriodDays     map[string]int `json:"grace_period_days,omitempty" dynamodbav:"grace_period_days,omitempty"`
	CollectionSchedule  string         `json:"collection_schedule"`
	DefaultRampSchedule string         `json:"default_ramp_schedule" dynamodbav:"default_ramp_schedule"`
	DefaultPayment      string         `json:"default_payment" dynamodbav:"default_payment"`
	RIExchangeMode      string         `json:"ri_exchange_mode" dynamodbav:"ri_exchange_mode"`
	// OfferingClass controls the EC2 Reserved Instance offering class used
	// during purchase. Accepted values: "convertible" (default) and
	// "standard". Convertible RIs can be exchanged for a different
	// instance family/size/region/OS; Standard RIs are locked to the exact
	// instance type for the full term but are ~5% cheaper.
	// Unknown values are rejected at purchase time with an explicit error.
	OfferingClass                  string   `json:"offering_class,omitempty" dynamodbav:"offering_class,omitempty"`
	EnabledProviders               []string `json:"enabled_providers" dynamodbav:"enabled_providers"`
	RIExchangeMaxPerExchangeUSD    float64  `json:"ri_exchange_max_per_exchange_usd" dynamodbav:"ri_exchange_max_per_exchange_usd"`
	RIExchangeUtilizationThreshold float64  `json:"ri_exchange_utilization_threshold" dynamodbav:"ri_exchange_utilization_threshold"`
	RIExchangeLookbackDays         int      `json:"ri_exchange_lookback_days" dynamodbav:"ri_exchange_lookback_days"`
	// RecommendationsCacheStaleHours is the age (hours) at which the
	// recommendations cache is considered stale and a background refresh
	// fires automatically (stale-while-revalidate). 0 disables automatic
	// background refresh; the cron scheduler and the manual Refresh button
	// still work regardless. Valid range: 0–8760 (up to one year).
	// Default: 24.
	RecommendationsCacheStaleHours int `json:"recommendations_cache_stale_hours" db:"recommendations_cache_stale_hours"`
	// RecommendationsLookbackDays is the AWS Cost Explorer lookback window
	// (days) used when fetching fresh recommendations. Must be one of 7,
	// 30, or 60 -- the AWS Cost Explorer LookbackPeriodInDays enum.
	// GCP CUD Recommender has no equivalent lookback parameter (fixed
	// internally); this setting applies to AWS only.
	// Default: 7.
	RecommendationsLookbackDays int `json:"recommendations_lookback_days" db:"recommendations_lookback_days"`
	// PurchaseDelayHours is the Gmail-style pre-fire delay (issue #291 wave-2).
	// When > 0, approving a purchase defers the actual cloud SDK call by this
	// many hours. The user receives a "scheduled, revoke before X" email
	// immediately after approval and may cancel at $0 until the window closes.
	// 0 means immediate-execute (backward compat). Valid range: [0, 168].
	// Default: 48.
	PurchaseDelayHours     int     `json:"purchase_delay_hours" db:"purchase_delay_hours"`
	NotificationDaysBefore int     `json:"notification_days_before"`
	DefaultTerm            int     `json:"default_term" dynamodbav:"default_term"`
	DefaultCoverage        float64 `json:"default_coverage" dynamodbav:"default_coverage"`
	RIExchangeMaxDailyUSD  float64 `json:"ri_exchange_max_daily_usd" dynamodbav:"ri_exchange_max_daily_usd"`
	// RI Exchange automation settings
	RIExchangeEnabled bool `json:"ri_exchange_enabled" dynamodbav:"ri_exchange_enabled"`
	// AutoPurchaseEnabled is the global kill switch for AutoPurchase plans
	// (migration 000112; the scheduling.auto_purchase default). Default false:
	// no AutoPurchase plan notifies or executes until an operator opts in.
	// Manual purchases (Opportunities, token approval) are unaffected.
	AutoPurchaseEnabled bool `json:"auto_purchase_enabled" db:"auto_purchase_enabled"`
	// LadderExecutionEnabled gates ladder purchases and reshapes (migration
	// 000083). BOTH LadderingEnabled AND LadderExecutionEnabled must be true
	// for a sweep that buys or reshapes to act. Planning (ladder_run) never
	// reads it: its capability always has the disabled write side, which
	// returns a typed ErrLadderExecutionDisabled. Default false: deployments
	// that enable laddering produce plans but never call AWS purchase APIs
	// until an operator explicitly opts in.
	LadderExecutionEnabled bool `json:"ladder_execution_enabled" db:"ladder_execution_enabled"`
	ApprovalRequired       bool `json:"approval_required" dynamodbav:"approval_required"`
	// LadderingEnabled is the global kill-switch for the commitment-laddering
	// feature (issue #1333 phase 3). When false (the default), no laddering
	// engine runs fire regardless of per-account LadderConfig.Enabled settings.
	// Set to true to allow per-account configs to activate individually.
	LadderingEnabled bool `json:"laddering_enabled" db:"laddering_enabled"`
	AutoCollect      bool `json:"auto_collect"`
	// RequireDifferentApprover enables 4-eyes approval mode (issue #1005).
	// When true, the user who created a purchase execution cannot approve it
	// themselves; a different person with approval rights must do so. This is
	// a standard SOX / SOC2 segregation-of-duties control. Default: false.
	// Admins who created an execution and need to approve it must disable this
	// mode first (the admin wildcard is NOT exempt from the restriction).
	// While it is on, unattended RI exchange runs (RIExchangeMode "auto" and
	// ladder reshapes) run as "manual" and only raise pending records
	// (issue #679), and recommendation-bearing AutoPurchase steps are not
	// auto-executed: they wait for a human approval in the approval queue
	// (issue #1771); empty steps still route as before. A step root
	// pre-created with the plan keeps its human creator, who then cannot
	// approve it. A step root pre-created through the admin API key (no
	// creator and not system-created) cannot be approved by anyone while this
	// is on: it waits, then expires, and a human recreates the plan; the
	// overdue tick does not re-arm such a root (#1785), nobody could approve it.
	RequireDifferentApprover bool `json:"require_different_approver" dynamodbav:"require_different_approver"`
	// ExperimentalGCPPlanTerm (migration 000173) makes GCP Compute
	// recommendation collection take the CUD term from each recommendation's
	// root Commitment plan (RecommendationParams.ExperimentalGCPPlanTerm in
	// cloud-commitments-go). EXPERIMENTAL: the plan payload shape is unverified
	// against a live Recommender. Default false keeps the 1yr term default.
	ExperimentalGCPPlanTerm bool `json:"experimental_gcp_plan_term" db:"experimental_gcp_plan_term"`
}

// DefaultGracePeriodDays is the fallback window used when a provider
// has no entry in GlobalConfig.GracePeriodDays. A week gives cloud
// providers enough time to reflect a fresh commitment in their
// utilization metrics before we'd re-propose the same capacity.
const DefaultGracePeriodDays = 7

// MaxGracePeriodDays is the ceiling enforced at read time as a safety
// net. The UI clamps input to [0, 30]; the DB isn't constrained, so a
// rogue write through psql shouldn't be able to suppress recs for years.
const MaxGracePeriodDays = 90

// DefaultRecommendationsCacheStaleHours is the default age (hours) after
// which the recommendations cache triggers a background refresh.
const DefaultRecommendationsCacheStaleHours = 24

// MaxRecommendationsCacheStaleHours is the maximum configurable stale
// threshold: one year. Values above this are rejected at validation time.
const MaxRecommendationsCacheStaleHours = 8760

// DefaultRecommendationsLookbackDays is the default AWS Cost Explorer
// lookback window when no explicit value is configured.
const DefaultRecommendationsLookbackDays = 7

// ValidRecommendationsLookbackDays lists the AWS Cost Explorer
// LookbackPeriodInDays enum values. Other values are rejected.
var ValidRecommendationsLookbackDays = []int{7, 30, 60}

// DefaultPurchaseDelayHours is the default Gmail-style pre-fire delay.
// 48 hours gives most users a working-day window to spot and cancel
// an approval they didn't intend.
const DefaultPurchaseDelayHours = 48

// MaxPurchaseDelayHours is the ceiling for PurchaseDelayHours. One week
// is long enough for any reasonable review cycle; longer delays make the
// UX confusing and the scheduler overhead non-trivial.
const MaxPurchaseDelayHours = 168

// GetPurchaseDelay returns the pre-fire delay as a time.Duration. Nil
// receiver returns the default. Values outside [0, MaxPurchaseDelayHours]
// are clamped so a rogue DB write cannot break the scheduler.
func (g *GlobalConfig) GetPurchaseDelay() time.Duration {
	if g == nil {
		return time.Duration(DefaultPurchaseDelayHours) * time.Hour
	}
	h := g.PurchaseDelayHours
	if h < 0 {
		h = 0
	}
	if h > MaxPurchaseDelayHours {
		h = MaxPurchaseDelayHours
	}
	return time.Duration(h) * time.Hour
}

// GracePeriodFor returns the effective grace-period window (in days)
// for the given provider slug ("aws", "azure", "gcp"). Returns the
// default when the provider has no explicit entry. Preserves an
// explicit 0 (which disables the feature for that provider). Clamps
// the result to [0, MaxGracePeriodDays] so a misconfigured DB row
// can't suppress recs indefinitely.
func (g *GlobalConfig) GracePeriodFor(provider string) int {
	if g == nil {
		return DefaultGracePeriodDays
	}
	days, ok := g.GracePeriodDays[provider]
	if !ok {
		return DefaultGracePeriodDays
	}
	if days < 0 {
		days = 0
	}
	if days > MaxGracePeriodDays {
		days = MaxGracePeriodDays
	}
	return days
}

// ServiceConfig represents per-service configuration.
//
// Coverage is a percentage whose meaning depends on where the config lives.
// In a PurchasePlan's Services it is the plan's sizing target (planstep sizes
// each ramp step by it). In the global and per-account service settings it is
// display-only: the dashboard scales projected savings by it and nothing in
// purchase execution reads it (issue #345).
type ServiceConfig struct {
	MaxStepCommitment *float64   `json:"max_step_commitment,omitempty" dynamodbav:"max_step_commitment,omitempty"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty" dynamodbav:"updated_at,omitempty"`
	// CreatedAt and UpdatedAt are the service_configs row's own timestamps
	// (issue #8), read-only. The store fills them on every read and from the
	// upsert's RETURNING clause; no write path stores a caller's value. Nil
	// (omitted from JSON) on a config that is not a stored row, such as an
	// entry of a purchase plan's services map.
	CreatedAt *time.Time `json:"created_at,omitempty" dynamodbav:"created_at,omitempty"`
	Provider  string     `json:"provider" dynamodbav:"provider"`
	Service   string     `json:"service" dynamodbav:"service"`
	// GroupID, BoundIdentities and MaxStepCommitment describe one config group
	// of a bound plan (issue #1746). BoundIdentities holds RecIdentityKey values;
	// MaxStepCommitment is the group's own per-step cap. All are empty on legacy
	// entries and on the global and per-account service settings.
	GroupID         string   `json:"group_id,omitempty" dynamodbav:"group_id,omitempty"`
	Payment         string   `json:"payment" dynamodbav:"payment"`
	RampSchedule    string   `json:"ramp_schedule" dynamodbav:"ramp_schedule"`
	ExcludeRegions  []string `json:"exclude_regions,omitempty" dynamodbav:"exclude_regions,omitempty"`
	IncludeRegions  []string `json:"include_regions,omitempty" dynamodbav:"include_regions,omitempty"`
	ExcludeEngines  []string `json:"exclude_engines,omitempty" dynamodbav:"exclude_engines,omitempty"`
	IncludeTypes    []string `json:"include_types,omitempty" dynamodbav:"include_types,omitempty"`
	ExcludeTypes    []string `json:"exclude_types,omitempty" dynamodbav:"exclude_types,omitempty"`
	IncludeEngines  []string `json:"include_engines,omitempty" dynamodbav:"include_engines,omitempty"`
	BoundIdentities []string `json:"bound_identities,omitempty" dynamodbav:"bound_identities,omitempty"`
	// MinCount is the GUI/persisted equivalent of the CLI --min-count flag:
	// the minimum instance/node count a recommendation must carry to be
	// surfaced. Applied at read time by
	// scheduler.filterRecsByResolvedConfigs against the persisted
	// RecommendationRecord.Count. 0 (the default) disables the filter,
	// matching the CLI flag's 0-no-floor semantics.
	MinCount int     `json:"min_count,omitempty" dynamodbav:"min_count,omitempty"`
	Coverage float64 `json:"coverage" dynamodbav:"coverage"`
	Term     int     `json:"term" dynamodbav:"term"`
	// BoundCount is response-only: plan responses replace BoundIdentities with
	// its length so a large selection does not bloat every plan read. It is
	// never stored or read from a request.
	BoundCount int  `json:"bound_count,omitempty" dynamodbav:"-"`
	Enabled    bool `json:"enabled" dynamodbav:"enabled"`
}

// PurchasePlan represents a saved purchase plan for automated execution.
type PurchasePlan struct {
	CreatedAt            time.Time                `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt            time.Time                `json:"updated_at" dynamodbav:"updated_at"`
	Services             map[string]ServiceConfig `json:"services" dynamodbav:"services"`
	LastNotificationSent *time.Time               `json:"last_notification_sent,omitempty" dynamodbav:"last_notification_sent,omitempty"`
	LastExecutionDate    *time.Time               `json:"last_execution_date,omitempty" dynamodbav:"last_execution_date,omitempty"`
	NextExecutionDate    *time.Time               `json:"next_execution_date,omitempty" dynamodbav:"next_execution_date,omitempty"`
	// MaxStepCommitment caps the TOTAL commitment (RecommendationRecord.TotalCommitment)
	// one ramp step may auto-purchase. nil means no cap is set, and auto-purchase of
	// recommendation-bearing steps is refused (fail closed). Always > 0 when set.
	MaxStepCommitment      *float64     `json:"max_step_commitment,omitempty" dynamodbav:"max_step_commitment,omitempty"`
	ID                     string       `json:"id" dynamodbav:"id"`
	Name                   string       `json:"name" dynamodbav:"name"`
	RampSchedule           RampSchedule `json:"ramp_schedule" dynamodbav:"ramp_schedule"`
	NotificationDaysBefore int          `json:"notification_days_before" dynamodbav:"notification_days_before"`
	AutoPurchase           bool         `json:"auto_purchase" dynamodbav:"auto_purchase"`
	Enabled                bool         `json:"enabled" dynamodbav:"enabled"`
	// Unassigned is true when the plan has zero rows in plan_accounts.
	// This can happen for legacy plans created before target_accounts was
	// required (issue #743). Such plans are invisible when an account filter
	// is active because the normal JOIN excludes them; ListPurchasePlans
	// surfaces them alongside filtered results so operators can find and
	// re-scope them. The field is omitted (false) in the no-filter case
	// where all plans are returned unconditionally.
	Unassigned bool `json:"unassigned,omitempty" dynamodbav:"unassigned,omitempty"`
}

type RampAllocationMode string

const (
	RampAllocationEqualCount RampAllocationMode = "equal_count"
	// RampAllocationBoundGroups marks a plan whose services entries are bound
	// to selected recommendations (issue #1746). It sizes like the default
	// mode; binaries that predate it refuse the plan (fail closed on rollback).
	RampAllocationBoundGroups RampAllocationMode = "bound_groups"
)

// SizesByPercent reports whether the mode sizes each step by PercentPerStep.
func (m RampAllocationMode) SizesByPercent() bool {
	return m == "" || m == RampAllocationBoundGroups
}

// RampSchedule defines how purchases are spread over time.
type RampSchedule struct {
	StartDate        time.Time          `json:"start_date" dynamodbav:"start_date"`
	Type             string             `json:"type" dynamodbav:"type"` // immediate, weekly, monthly, custom
	AllocationMode   RampAllocationMode `json:"allocation_mode,omitempty" dynamodbav:"allocation_mode,omitempty"`
	PercentPerStep   float64            `json:"percent_per_step" dynamodbav:"percent_per_step"`
	StepIntervalDays int                `json:"step_interval_days" dynamodbav:"step_interval_days"`
	CurrentStep      int                `json:"current_step" dynamodbav:"current_step"`
	TotalSteps       int                `json:"total_steps" dynamodbav:"total_steps"`
	DatesManaged     bool               `json:"dates_managed,omitempty" dynamodbav:"dates_managed,omitempty"`
}

// PresetRampSchedules provides common ramp-up configurations.
var PresetRampSchedules = map[string]RampSchedule{
	"immediate": {
		Type:           "immediate",
		PercentPerStep: 100,
		TotalSteps:     1,
	},
	"weekly-25pct": {
		Type:             "weekly",
		PercentPerStep:   25,
		StepIntervalDays: 7,
		TotalSteps:       4,
	},
	"monthly-10pct": {
		Type:             "monthly",
		PercentPerStep:   10,
		StepIntervalDays: 30,
		TotalSteps:       10,
	},
}

// CumulativePercent is the share of the base (0..100) a ramp has bought once
// step steps are complete: step*PercentPerStep capped at 100, and 100 for an
// immediate schedule. It is the one definition of cumulative ramp progress.
func (r *RampSchedule) CumulativePercent(step int) float64 {
	if r.Type == "immediate" {
		return 100
	}
	if r.AllocationMode == RampAllocationEqualCount && r.TotalSteps > 0 {
		return 100 * float64(min(step, r.TotalSteps)) / float64(r.TotalSteps)
	}
	return math.Min(100, float64(step)*r.PercentPerStep)
}

// GetCurrentCoverage calculates the current effective coverage based on ramp progress.
func (r *RampSchedule) GetCurrentCoverage(baseCoverage float64) float64 {
	if r.Type == "immediate" {
		return baseCoverage // base*100/100 is not exact in floating point
	}
	return baseCoverage * r.CumulativePercent(r.CurrentStep) / 100
}

// GetNextPurchaseDate calculates when the next purchase step should occur.
func (r *RampSchedule) GetNextPurchaseDate() time.Time {
	if r.StartDate.IsZero() {
		return time.Now()
	}
	return r.StartDate.AddDate(0, 0, r.CurrentStep*r.StepIntervalDays)
}

// IsComplete returns true if all ramp steps are done.
func (r *RampSchedule) IsComplete() bool {
	return r.CurrentStep >= r.TotalSteps
}

// PurchaseSuppression records the per-tuple grace window after a bulk
// purchase. See migration 000037 for the full SQL shape + lifecycle
// documentation. Written inside the same transaction as the execution
// insert; deleted inside the same transaction as a cancel/expire status
// transition.
type PurchaseSuppression struct {
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	// Variant narrows the row to one availability zone, platform and tenancy
	// on its 6-tuple (issue #816, migration 000134). nil means the variant is
	// unknown: rows written before 000134 or by code predating #816. Such a
	// row is a wildcard over its 6-tuple (see AppliesTo).
	Variant         *SuppressionVariant `json:"variant,omitempty"`
	ID              string              `json:"id"`
	ExecutionID     string              `json:"execution_id"`
	AccountID       string              `json:"account_id"`
	Provider        string              `json:"provider"`
	Service         string              `json:"service"`
	Region          string              `json:"region"`
	ResourceType    string              `json:"resource_type"`
	Engine          string              `json:"engine"`
	SuppressedCount int                 `json:"suppressed_count"`
}

// SuppressionVariant is the part of the suppression key beyond the 6-tuple:
// the zonal EC2 availability zone, the EC2 platform and tenancy, and the AWS
// RDS az_config, the same fields the rec identity key carries (issues #792,
// #806, #408). Empty AvailabilityZone, Platform and Tenancy are known values
// (a regional rec, a non-EC2 rec), not wildcards. AZConfig is the exception:
// "" means "not applicable or unknown" and matches any az_config (see
// AppliesTo), because suppressions written before migration 000159 have none
// and must keep suppressing both the Single-AZ and the Multi-AZ rec.
type SuppressionVariant struct {
	AvailabilityZone string `json:"availability_zone"`
	Platform         string `json:"platform"`
	Tenancy          string `json:"tenancy"`
	AZConfig         string `json:"az_config,omitempty"`
}

// awsLegacyServiceSlugs maps the AWS service slugs that rec rows persisted
// before slug normalisation still carry to their canonical spelling.
// memorydb has no canonical slug and is deliberately absent. Keyed and valued by the
// library constants; a purchase test checks it against mapServiceSlug.
var awsLegacyServiceSlugs = map[string]string{
	string(common.ServiceEC2):         string(common.ServiceCompute),
	string(common.ServiceRDS):         string(common.ServiceRelationalDB),
	string(common.ServiceElastiCache): string(common.ServiceCache),
	string(common.ServiceOpenSearch):  string(common.ServiceSearch),
	string(common.ServiceRedshift):    string(common.ServiceDataWarehouse),
}

// awsSavingsPlansSpellings maps the dash-free spelling of each per-plan-type
// Savings Plans slug, which the purchase manager's mapSavingsPlansSlug also
// accepts, to the hyphenated library constant that recs carry (issue #1422).
// The "savingsplans" umbrella is deliberately absent: folding it into one plan
// type would let a single umbrella suppression hide another plan type's recs.
// A purchase test checks every per-type key of mapSavingsPlansSlug against it.
var awsSavingsPlansSpellings = map[string]string{
	"savingsplans-compute":     string(common.ServiceSavingsPlansCompute),
	"savingsplans-ec2instance": string(common.ServiceSavingsPlansEC2Instance),
	"savingsplans-sagemaker":   string(common.ServiceSavingsPlansSageMaker),
	"savingsplans-database":    string(common.ServiceSavingsPlansDatabase),
}

// CanonicalSuppressionService maps legacy AWS service slugs, and the dash-free
// spellings of the per-type Savings Plans slugs, to their canonical slug so a
// suppression and a rec match whichever spelling each was stored under
// (issues #1412, #1420, #1422). Other providers' services, including Azure and
// GCP "compute" and Azure "savingsplans", are returned unchanged. It lives in
// config, the lowest package both the scheduler and BuildSuppressions import;
// the pinned cloud-commitments-go common package has no such mapping.
func CanonicalSuppressionService(provider, service string) string {
	if provider != "aws" {
		return service
	}
	if canonical, ok := awsLegacyServiceSlugs[service]; ok {
		return canonical
	}
	if canonical, ok := awsSavingsPlansSpellings[service]; ok {
		return canonical
	}
	return service
}

// CanonicalSuppressionEngine reads an engine "" as the engine the pinned
// library now emits, so a suppression and a rec match across the bumps that
// gave those recs an engine: go#702 for Azure cache ("redis", Advisor Redis
// recs had none) and go#735 for Azure relational-db ("sqlserver", every Azure
// SQL Database rec is SQL Server; issue #1950). Suppressions written before
// the bump keep engine "" in the table and are canonicalized on read. Other
// tuples are unchanged: an AWS RDS "" never matches "mysql".
func CanonicalSuppressionEngine(provider, service, engine string) string {
	if provider != string(common.ProviderAzure) || engine != "" {
		return engine
	}
	switch service {
	case string(common.ServiceCache):
		return "redis"
	case string(common.ServiceRelationalDB):
		return "sqlserver"
	}
	return engine
}

// StoredRecommendationService is the service slug the platform stores and keys
// a provider recommendation under. go#702 relabelled Azure Redis recs from
// cache to memorydb; the platform's IDs, suppressions and per-account config
// key Azure Redis as cache, so it maps back here. AWS memorydb is a distinct
// service and is unchanged.
func StoredRecommendationService(provider string, service common.ServiceType) string {
	if provider == string(common.ProviderAzure) && service == common.ServiceMemoryDB {
		return string(common.ServiceCache)
	}
	return string(service)
}

// AppliesTo reports whether the suppression counts against a recommendation
// on the same 6-tuple whose variant is v. A row without a variant (legacy)
// applies to every variant, and a nil v (the rec's Details could not be read)
// matches every row: both fall back to the pre-#816 6-tuple match, which
// over-suppresses (under-buys) rather than over-shows.
func (s *PurchaseSuppression) AppliesTo(v *SuppressionVariant) bool {
	if s.Variant == nil || v == nil {
		return true
	}
	a, b := *s.Variant, *v
	// An empty az_config on either side is a wildcard: legacy rows predate it
	// and an RDS rec without one cannot say which offering it is (#408).
	azConfigOK := a.AZConfig == "" || b.AZConfig == "" || a.AZConfig == b.AZConfig
	a.AZConfig, b.AZConfig = "", ""
	// Only the stored side needs canonicalising: SuppressionVariant already
	// canonicalises every rec variant, so a raw "shared" never reaches v.
	if isAWSEC2(s.Provider, s.Service) {
		a.Tenancy = canonicalSuppressionTenancy(a.Tenancy)
	}
	return azConfigOK && a == b
}

// RecommendationFilter parameterises ListStoredRecommendations and the
// handler-facing scheduler.ListRecommendations wrapper. Zero-value fields
// mean "no filter"; non-empty AccountIDs restricts to the given IDs.
//
// MinSavingsUSD is a dollar floor: only recommendations whose monthly savings
// are >= MinSavingsUSD are returned. 0 means no floor.
//
// MinSavingsPct is a percentage floor (0–100): only recommendations whose
// effective savings percentage (savings/on-demand*100) meets or exceeds this
// threshold are returned. 0 means no floor. Applied in-process after the DB
// query rather than in SQL (avoids a computed column). These two filters are
// independent and can be combined.
type RecommendationFilter struct {
	Provider                     string   // "aws" / "azure" / "gcp" / "" (all)
	Service                      string   // "" = all services
	Region                       string   // "" = all regions
	ID                           string   // "" = all ids; non-empty = exact match on the id column
	AccountIDs                   []string // nil/empty = all accounts
	MinSavingsUSD                float64  // 0 = no floor on monthly savings dollar amount
	MinSavingsPct                float64  // 0 = no floor on savings percentage (0–100 scale)
	RequireAzurePricingMigration bool
}

// RecommendationScanKey is a stored recommendation's position in the keyset
// scan order of ListStoredRecommendationsChunk (issue #981): the application
// recommendation ID compared byte-wise, then the cache table's UUID primary
// key (RowID), which keeps rows that share an ID in a total order.
type RecommendationScanKey struct {
	ID    string
	RowID string
}

// RecommendationChunk is one keyset read of the recommendations cache.
// Recs holds the rows that passed the filter, in scan order. Last is the
// position of the last row read (nil when no row was read) and is where the
// next chunk resumes. Done is true when fewer rows than the limit were read,
// so nothing matches after Last.
type RecommendationChunk struct {
	Last *RecommendationScanKey
	Recs []RecommendationRecord
	Done bool
}

// PurchasePlanFilter parameterises ListPurchasePlans. Zero-value means "no
// filter" (all plans are returned). Non-empty AccountIDs restricts the result
// to plans that reference at least one of the given account IDs via the
// plan_accounts join table.
type PurchasePlanFilter struct {
	AccountIDs []string // nil/empty = all plans
	// IncludeUnassigned adds plans with zero plan_accounts rows to an
	// AccountIDs-filtered result. Only unrestricted callers may set it.
	IncludeUnassigned bool
}
