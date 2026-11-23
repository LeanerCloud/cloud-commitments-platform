package config

import (
	"encoding/json"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// RecommendationRecord stores a recommendation with purchase status.
type RecommendationRecord struct {
	// MonthlyCost is nil when the provider API did not return a monthly
	// recurring breakdown (rendered as "—" in the UI, not "$0").
	// Backward-compatible with DynamoDB: existing items with a numeric 0
	// attribute unmarshal as a pointer to 0.0; absent attributes unmarshal
	// as nil. No migration needed.
	MonthlyCost *float64 `json:"monthly_cost" dynamodbav:"monthly_cost"`
	MemoryGB    *float64 `json:"memory_gb,omitempty" dynamodbav:"-"`
	// VCPU and MemoryGB surface the compute size of the recommended
	// instance type so the frontend's Capacity column can render
	// "<vcpu> vCPU / <memory> GB" without parsing the opaque Details blob
	// (#219). They are NOT persisted: the canonical source is the typed
	// ComputeDetails nested inside Details (config must stay free of
	// pkg/common imports). The api layer decodes Details via
	// common.DecodeServiceDetailsFor in buildRecommendationsResponse and
	// stamps these top-level fields on the way out, so the API JSON carries
	// them at the top level where the frontend already reads them.
	//
	// Pointers (not plain int/float64) so "absent / non-compute / unknown
	// size" serializes as omitted rather than a misleading 0: the frontend
	// renders absent as "—", and a literal 0 would otherwise look like a
	// real "0 vCPU / 0 GB" capacity. dynamodbav:"-" because they are
	// derived-on-read, never stored.
	VCPU *int `json:"vcpu,omitempty" dynamodbav:"-"`
	// PrimarySuppressionExecutionID identifies the execution whose
	// suppression contributed the most to this tuple (ties broken by
	// newest created_at). The frontend badge deep-links to Purchase
	// History filtered to this execution.
	PrimarySuppressionExecutionID *string `json:"primary_suppression_execution_id,omitempty" dynamodbav:"primary_suppression_execution_id,omitempty"`
	// SuppressionExpiresAt is the earliest expiry across all active
	// suppression rows contributing to this tuple. The frontend uses it
	// to render "Xd remaining" on the recently-purchased badge.
	SuppressionExpiresAt *time.Time `json:"suppression_expires_at,omitempty" dynamodbav:"suppression_expires_at,omitempty"`
	SourceAccountID      *string    `json:"source_account_id,omitempty" dynamodbav:"source_account_id,omitempty"`
	CloudAccountID       *string    `json:"cloud_account_id,omitempty" dynamodbav:"cloud_account_id,omitempty"`
	// SavingsPercentage is the provider-authoritative effective savings %
	// reported directly by the cloud provider (AWS Cost Explorer
	// `EstimatedMonthlySavingsPercentage`, Azure / GCP converters' computed
	// SavingsPercentage). It is the same figure the CLI/reporter prints
	// verbatim (github.com/LeanerCloud/cloud-commitments-go/pkg/reporter);
	// persisting it lets the GUI show the identical number instead of
	// re-deriving it client-side from savings / on-demand. Persisted via
	// the recommendations row's JSONB `payload` column; no DDL needed.
	//
	// nil means the provider did not report a percentage; the frontend then
	// falls back to the client-side reconstruction (effectiveSavingsPct).
	// When non-nil, the frontend prefers this value so the displayed % cannot
	// drift from the provider's authoritative number and AWS recs missing
	// on_demand_cost still render a real % rather than an em-dash (see #323).
	SavingsPercentage *float64 `json:"savings_percentage" dynamodbav:"savings_percentage,omitempty"`
	// OnDemandCost is the canonical on-demand monthly baseline for the
	// recommended commitment, sourced directly from the cloud provider
	// (Azure `CostWithNoReservedInstances`, AWS Cost Explorer
	// `EstimatedMonthlyOnDemandCost`). Persisted via the recommendations
	// row's JSONB `payload` column — no DDL needed.
	//
	// nil means the provider API did not return a baseline; the frontend
	// falls back to reconstructing on-demand from `monthly_cost + savings
	// + amortized_upfront`. When non-nil, the frontend prefers the raw
	// value over reconstruction so anomalies in the reconstructed
	// denominator (e.g. Azure all-upfront recs where monthly_cost=$0
	// collapses the denominator) don't inflate the displayed effective
	// savings %. See #274.
	OnDemandCost *float64 `json:"on_demand_cost,omitempty" dynamodbav:"on_demand_cost,omitempty"`
	Error        string   `json:"error,omitempty" dynamodbav:"error,omitempty"`
	ResourceType string   `json:"resource_type" dynamodbav:"resource_type"`
	Payment      string   `json:"payment" dynamodbav:"payment"`
	Provider     string   `json:"provider" dynamodbav:"provider"`
	Service      string   `json:"service" dynamodbav:"service"`
	Region       string   `json:"region" dynamodbav:"region"`
	Engine       string   `json:"engine,omitempty" dynamodbav:"engine,omitempty"`
	// CommitmentCurrency is the ISO 4217 code UpfrontCost and MonthlyCost are
	// denominated in, copied from common.Recommendation.CommitmentCurrency at
	// collection time. Empty means unknown: the provider parser could not
	// prove the currency of every payment leg, or the row predates the field.
	// The purchase spend caps are USD, so a rec that is not provably USD is
	// never compared against one (issue #361). Persisted via the
	// recommendations row's JSONB `payload` column, and with the execution's
	// recommendations JSONB; no DDL needed.
	CommitmentCurrency string `json:"commitment_currency,omitempty" dynamodbav:"commitment_currency,omitempty"`
	// AvailabilityZone is the zonal EC2 reservation's AZ name (e.g.
	// "us-east-1a"), empty for regional and non-EC2 recs. It mirrors
	// ComputeDetails.AvailabilityZone inside Details (config must not import
	// pkg/common) so the identity tuple, the rec ID and the recommendations
	// natural key can distinguish two zonal recs that differ only by AZ
	// (issue #792). The scheduler stamps it at collection time; request recs
	// echo it from the listed rec.
	AvailabilityZone string `json:"availability_zone,omitempty" dynamodbav:"availability_zone,omitempty"`
	ID               string `json:"id" dynamodbav:"id"`
	PurchaseID       string `json:"purchase_id,omitempty" dynamodbav:"purchase_id,omitempty"`
	// Details preserves the full common.ServiceDetails payload from the
	// source common.Recommendation so the purchase path can reconstruct
	// the correct typed *Details pointer at execute time (issue #453).
	// Stored as raw JSON because RecommendationRecord lives in the
	// config package, which must NOT import pkg/common (the dependency
	// graph is config <- common in callers, never the reverse). The
	// scheduler populates this at collection time via
	// common.MarshalServiceDetails; the purchase manager reads it via
	// common.DecodeServiceDetailsFor when it builds the
	// common.Recommendation handed to the cloud service client.
	//
	// Empty for rows persisted before #453 — DecodeServiceDetailsFor
	// returns a zero-valued typed pointer in that case so the cloud
	// client's findOfferingID type-assertion still succeeds (the
	// service-side buildOfferingFilters substitutes default
	// Platform / Tenancy / Scope / AZConfig values). New rows always
	// carry the full Details, so non-default platforms (Windows EC2,
	// Postgres RDS, etc.) round-trip correctly.
	Details json.RawMessage `json:"details,omitempty" dynamodbav:"-"`
	// UsageHistory is a short time-series of daily RI-coverage percentages
	// (0-100) for the last N days of the lookback window, ordered from
	// oldest to newest. nil means the collector did not populate it (e.g.
	// provider not yet wired); an empty non-nil slice means the collector
	// ran but returned no daily data. The frontend renders nil as "—" and
	// a non-empty slice as a thumbnail sparkline. Stored inside the
	// recommendations JSONB payload — no DDL change needed (closes #239
	// Part 1 for AWS).
	UsageHistory []float64 `json:"usage_history,omitempty" dynamodbav:"usage_history,omitempty"`
	Count        int       `json:"count" dynamodbav:"count"`
	// SuppressedCount is the cumulative count already committed against
	// this recommendation's suppression key (account, provider, service,
	// region, resource_type, engine, plus availability zone, platform and
	// tenancy, issue #816) within the active grace window. The
	// scheduler subtracts this from Count before returning the rec to
	// the frontend; a rec where SuppressedCount ≥ original count is
	// dropped entirely. Populated by the scheduler — zero on writes.
	SuppressedCount int     `json:"suppressed_count,omitempty" dynamodbav:"suppressed_count,omitempty"`
	Term            int     `json:"term" dynamodbav:"term"`
	Savings         float64 `json:"savings" dynamodbav:"savings"`
	// RecommendedCount is the pre-scaling count the collector originally
	// recommended, before the bulk-purchase Capacity % slider scaled it down.
	// The web execute path stamps it so the backend can verify the
	// client-supplied capacity_percent against the scaled Count
	// (floor(RecommendedCount*pct/100) must equal Count) rather than trusting
	// a decorative audit field that could silently disagree (#647). Optional:
	// 0 / absent means "not supplied" (legacy callers, scheduler/CLI rows,
	// retry replays) and the consistency check is skipped for that rec.
	RecommendedCount int     `json:"recommended_count,omitempty" dynamodbav:"recommended_count,omitempty"`
	UpfrontCost      float64 `json:"upfront_cost" dynamodbav:"upfront_cost"`
	Purchased        bool    `json:"purchased" dynamodbav:"purchased"`
	Selected         bool    `json:"selected" dynamodbav:"selected"`
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
