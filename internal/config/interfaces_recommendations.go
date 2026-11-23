package config

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// RecommendationStore holds the recommendation cache and suppression methods of StoreInterface; StoreInterface embeds it.
type RecommendationStore interface {
	// Recommendations cache (ADR: store recommendations in Postgres so the
	// dashboard serves provider-switch clicks from SQL instead of live cloud
	// API calls). UpsertRecommendations is the only write path and takes a
	// list of (provider, account) pairs that successfully collected this
	// cycle. Stale-row eviction is scoped to that union so a partially-failed
	// provider preserves the failed accounts' previous-cycle rows. See
	// SuccessfulCollect for the per-row semantics. There is deliberately no
	// unscoped "replace everything" method (issue #159).
	UpsertRecommendations(ctx context.Context, collectedAt time.Time, recs []RecommendationRecord, successfulCollects []SuccessfulCollect) error
	// UpsertRecommendationsWithPartial is UpsertRecommendations that also
	// records the collection's incomplete-sweep summary (nil clears it) in the
	// same transaction, so the rows and the marker commit or roll back together.
	UpsertRecommendationsWithPartial(ctx context.Context, collectedAt time.Time, recs []RecommendationRecord, successfulCollects []SuccessfulCollect, partial *RecommendationsCollectionPartial) error
	// PruneStaleRecommendations deletes the rows of every (provider,
	// account) pair with no row collected in the last retentionDays days,
	// leaving every pair that collected inside the window untouched, and
	// returns the number of rows deleted (issue #120).
	PruneStaleRecommendations(ctx context.Context, retentionDays int) (int64, error)
	ListStoredRecommendations(ctx context.Context, filter RecommendationFilter) ([]RecommendationRecord, error)
	// ListStoredRecommendationsChunk is ListStoredRecommendations for a
	// keyset walk: at most limit rows after the position after, in
	// byte-wise ID order then primary-key order (issue #981).
	ListStoredRecommendationsChunk(ctx context.Context, filter RecommendationFilter, after *RecommendationScanKey, limit int) (RecommendationChunk, error)
	// ListStoredRecommendationTriples lists the distinct (account,
	// provider, service) keys of the rows scope selects, for resolving
	// account overrides before PageStoredRecommendations (issue #981).
	ListStoredRecommendationTriples(ctx context.Context, scope RecommendationListScope) ([]RecommendationTriple, error)
	// PageStoredRecommendations reads one page of the recommendations list
	// and the summary of the whole filtered set with every list filter
	// evaluated in SQL, in one snapshot (issue #981).
	PageStoredRecommendations(ctx context.Context, q RecommendationPageQuery) (RecommendationPage, error)
	GetRecommendationsFreshness(ctx context.Context) (*RecommendationsFreshness, error)
	SetRecommendationsCollectionError(ctx context.Context, errMsg string) error
	// MarkCollectionStarted atomically sets last_collection_started_at = now
	// only when no in-flight collection is running (last_collection_started_at IS NULL
	// OR older than 5 minutes), and stamps a freshly generated owner token
	// alongside it. Returns the token and true when this caller won the race
	// and should proceed with the async invoke, passing the token through so
	// only this caller can later clear the marker; ("", false, nil) when
	// another collection is already in flight and the caller should return
	// 409.
	MarkCollectionStarted(ctx context.Context) (token string, ok bool, err error)
	// ClearCollectionStarted clears last_collection_started_at, but only if
	// last_collection_owner_id still matches token: a compare-and-clear
	// guard (issue #261) so a caller that never won MarkCollectionStarted
	// (cron, cold-start) cannot wipe another caller's in-flight marker. A
	// mismatched token is a documented silent no-op (the marker belongs to
	// someone else); an empty token is a boundary error, since only a
	// caller that actually owns a marker should ever call Clear.
	ClearCollectionStarted(ctx context.Context, token string) error
	// ClaimCollectionInvocation is called by a token-carrying collect run
	// once it holds the task advisory lock (issue #84). It re-stamps the
	// marker with a fresh invocation-scoped token when the marker is still
	// owned by markerToken, or free/stale, so a lock-skipped duplicate
	// delivery of the same event (same markerToken) can no longer clear
	// it. Returns the new token and true when the caller now owns the
	// marker; ("", false, nil) when another run holds a fresh marker.
	ClaimCollectionInvocation(ctx context.Context, markerToken string) (token string, ok bool, err error)

	// RI utilization cache. Postgres-backed TTL cache for Cost Explorer
	// GetReservationUtilization; shared across Lambda containers so
	// dashboard loads don't each fan out to a paid CE API call. A
	// per-process in-memory cache effectively never hits because each
	// cold container starts empty. GetRIUtilizationCache returns nil
	// when the (cloud_account_id, region, lookback_days) key is absent —
	// callers treat that as a miss and re-fetch.
	GetRIUtilizationCache(ctx context.Context, key RIUtilizationCacheKey) (*RIUtilizationCacheEntry, error)
	UpsertRIUtilizationCache(ctx context.Context, key RIUtilizationCacheKey, payload []byte, fetchedAt time.Time) error

	// Purchase suppressions. Written inside a WithTx block during bulk
	// purchase submit so the execution insert + the suppression rows
	// commit atomically. Deleted on cancel/expire of the execution,
	// also inside a WithTx block paired with the status update.
	//
	// The plain variants (no Tx) open their own single-call transaction
	// — useful for tests and one-off admin operations. The Tx variants
	// reuse a caller-provided transaction so multi-write operations
	// can roll back atomically.
	CreateSuppression(ctx context.Context, sup *PurchaseSuppression) error
	CreateSuppressionTx(ctx context.Context, tx pgx.Tx, sup *PurchaseSuppression) error
	DeleteSuppressionsByExecution(ctx context.Context, executionID string) error
	DeleteSuppressionsByExecutionTx(ctx context.Context, tx pgx.Tx, executionID string) error
	ListActiveSuppressions(ctx context.Context) ([]PurchaseSuppression, error)
}
