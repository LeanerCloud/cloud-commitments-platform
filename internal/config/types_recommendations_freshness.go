package config

import (
	"time"
)

// RecommendationsFreshness describes the cache staleness state surfaced to
// the frontend. LastCollectedAt is nil on a cold start.
// LastCollectionError is non-nil when the most recent collect attempt
// partially or fully failed.
// LastCollectionStartedAt is non-nil while an async collect is in flight;
// the scheduler clears it on completion (success or failure). A value older
// than 5 minutes means the scheduler crashed mid-run — the refresh handler
// treats it as stale and allows a new collection.
// LastCollectionPartial is non-nil when the most recent persisted collection
// kept rows from a sweep that did not cover everything it was asked to (for
// example 9 of 10 Azure subscriptions answered). It is cleared by the next
// collection whose every sweep is complete.
// NextCollectionAt is not stored: the API handler computes it from the
// deployment's schedule (issue #756) and leaves it nil when scheduled
// collection is off or its next run cannot be known.
type RecommendationsFreshness struct {
	LastCollectedAt         *time.Time                        `json:"last_collected_at"`
	LastCollectionError     *string                           `json:"last_collection_error"`
	LastCollectionStartedAt *time.Time                        `json:"last_collection_started_at"`
	LastCollectionPartial   *RecommendationsCollectionPartial `json:"last_collection_partial"`
	NextCollectionAt        *time.Time                        `json:"next_collection_at"`
	// LastCollectionPartialUnreadable is set when the stored marker could not be
	// decoded (LastCollectionPartial is then nil). It stays out of the API: it
	// only lets the purchase gate refuse instead of reading "no marker".
	LastCollectionPartialUnreadable bool `json:"-"`
}

// RecommendationsCollectionPartial records that the most recent collection
// was incomplete without having failed: its rows were kept and stored, but
// some subscriptions, projects or collection scopes were never queried, so
// the stored recommendations under-state the real opportunity (issue #87).
//
// It is deliberately aggregate-only. recommendations_state is one global row
// that every user with view:recommendations reads, while accounts are
// access-scoped, so it carries counts and a fixed set of reason categories --
// never raw provider error text or subscription/project identifiers. The
// failed identifiers are named in the scheduler's WARN log for operators.
type RecommendationsCollectionPartial struct {
	// Providers lists one entry per provider with at least one incomplete
	// sweep, in the configured provider order.
	Providers []PartialProviderSweep `json:"providers"`
}

// Reason categories for PartialProviderSweep.Reasons. The set is closed so
// the API never echoes provider error text.
const (
	PartialReasonPermissionDenied    = "permission_denied"
	PartialReasonThrottled           = "throttled"
	PartialReasonTimeout             = "timeout"
	PartialReasonCanceled            = "canceled"
	PartialReasonNotFound            = "not_found"
	PartialReasonProviderUnavailable = "provider_unavailable"
	PartialReasonOther               = "other"
	// PartialReasonPricingUnavailable marks accounts whose previous-cycle
	// Advisor rows were kept because the Retail Prices catalog could not be
	// reached. Those rows may carry an outdated price and must not be
	// purchased until a good collection re-prices them.
	PartialReasonPricingUnavailable = "pricing_unavailable"
	// PartialReasonPricingHoldExpired counts accounts whose pricing hold hit
	// PricingHoldMaxAge and whose stale rows were evicted; it equals the length
	// of PartialProviderSweep.ExpiredHoldAccountIDs.
	PartialReasonPricingHoldExpired = "pricing_hold_expired"
)

// PricingHoldMaxAge is how long an account's Azure rows stay held through a
// pricing outage before they are evicted. It is age based because manual and
// stale-cache refreshes also run collections, so a cycle count is not a time.
// On a daily schedule the hold is kept on the second run (about 24h) and the
// account is dropped on the third (about 48h).
const PricingHoldMaxAge = 36 * time.Hour

// PartialProviderSweep summarizes one provider's incomplete sweeps in a
// collection. A "scope" is the unit a sweep fails in: a subscription for
// Azure, a project for GCP, and a service/region collection scope for AWS.
type PartialProviderSweep struct {
	// Reasons counts the failure causes by PartialReason* category.
	Reasons map[string]int `json:"reasons"`
	// HeldSince records when each held account (by HeldAccountIDs entry, or the
	// nil UUID for an ambient hold) was first held in the current streak.
	HeldSince map[string]time.Time `json:"held_since,omitempty"`
	Provider  string               `json:"provider"`
	// HeldAccountIDs lists the platform cloud-account UUIDs (not cloud
	// account numbers) whose previous-cycle rows are held through a pricing
	// outage; the purchase path refuses their recommendations. It can contain
	// the nil UUID (all zeros) for the ambient identity, which has no account;
	// the API shows it only to callers with an unrestricted account scope.
	HeldAccountIDs []string `json:"held_account_ids,omitempty"`
	// ExpiredHoldAccountIDs lists accounts whose hold passed PricingHoldMaxAge
	// and whose stale rows were evicted. It is carried until the account
	// re-prices or stops being an enabled Azure account.
	ExpiredHoldAccountIDs []string `json:"expired_hold_account_ids,omitempty"`
	// IncompleteAccounts is how many accounts (registered, or the single
	// ambient host identity) returned rows from an incomplete sweep.
	IncompleteAccounts int `json:"incomplete_accounts"`
	// AttemptedScopes is how many scopes the incomplete sweeps were asked to
	// cover. Zero when the provider does not report it (AWS).
	AttemptedScopes int `json:"attempted_scopes"`
	// FailedScopes is how many of those scopes could not be queried.
	FailedScopes int `json:"failed_scopes"`
	// FailedDetails is how many individual recommendation details were
	// rejected inside scopes that otherwise answered (AWS only).
	FailedDetails int `json:"failed_details"`
	// SkippedRecommendations is how many recommendations the provider
	// dropped as invalid (GCP: non-USD, malformed amount or plan, unknown
	// term) while returning the rest. Those accounts' sweeps were complete.
	SkippedRecommendations int `json:"skipped_recommendations"`
}

// SuccessfulCollect identifies a (provider, account) pair whose collection
// completed in the current cycle. UpsertRecommendations scopes the
// stale-row eviction DELETE to the union of these pairs so a partially-
// failed provider preserves the failed accounts' previous-cycle rows
// (the dashboard for those accounts isn't blanked out by transient
// cloud-API failures).
//
// CloudAccountID is nil for the AWS ambient-credentials path (no
// registered account); the eviction collapses nil to the zero UUID via
// the same generated-column rule that applies to inserts, so ambient
// rows are evicted independently of any registered-account rows under
// the same provider.
type SuccessfulCollect struct {
	CloudAccountID *string
	Provider       string
}

// RIUtilizationCacheKey identifies one cached Cost Explorer
// GetReservationUtilization result. CloudAccountID is the cloud_accounts
// UUID of the account whose credentials made the call, or "" when the
// deployment's ambient identity is not registered in cloud_accounts. It is
// part of the key so two accounts reading the same region never share a
// row (issue #120).
type RIUtilizationCacheKey struct {
	CloudAccountID string
	Region         string
	LookbackDays   int
}

// RIUtilizationCacheEntry is a single cached Cost Explorer
// GetReservationUtilization result keyed by RIUtilizationCacheKey.
// Payload is the JSON encoding of the caller's utilization slice — kept
// opaque here so the config package stays free of AWS-provider types.
// TTL freshness is evaluated in the caller (api-layer cache wrapper)
// based on FetchedAt vs. a caller-supplied TTL.
type RIUtilizationCacheEntry struct {
	FetchedAt      time.Time
	CloudAccountID string
	Region         string
	Payload        []byte
	LookbackDays   int
}
