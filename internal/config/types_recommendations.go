package config

import (
	"encoding/json"
	"time"
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
