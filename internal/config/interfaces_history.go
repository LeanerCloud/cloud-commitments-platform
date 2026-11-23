package config

import (
	"context"
	"time"
)

// HistoryStore holds the purchase-history methods of StoreInterface; StoreInterface embeds it.
type HistoryStore interface {
	// Purchase history
	SavePurchaseHistory(ctx context.Context, record *PurchaseHistoryRecord) error
	GetPurchaseHistory(ctx context.Context, accountID string, limit int) ([]PurchaseHistoryRecord, error)
	GetAllPurchaseHistory(ctx context.Context, limit int) ([]PurchaseHistoryRecord, error)
	// GetActivePurchaseHistory returns every purchase_history row whose commitment
	// is still within its term at asOf (term > 0 AND timestamp + term years >= asOf;
	// the expiry boundary is inclusive, matching the API layer's isActiveCommitment),
	// newest-first, optionally scoped to a set of accounts. The account scope uses
	// the same dual-column predicate as GetPurchaseHistoryFiltered: accountIDs
	// match cloud_account_id (cloud_accounts UUIDs) and externalIDsByProvider
	// matches account_id per provider, OR'd together so rows carrying only one
	// identifier are still returned (issues #701/#498/#866). Both empty means all
	// accounts. Unlike GetAllPurchaseHistory it is not row-capped: the analytics
	// collector, dashboard KPIs, and inventory endpoints need the complete active
	// set, and filtering expired commitments in SQL keeps the result bounded by
	// the number of live commitments rather than by all history ever recorded (so
	// it cannot silently truncate older-but-still-active 1y/3y commitments the
	// way a newest-first capped page does, issue #1140).
	GetActivePurchaseHistory(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]PurchaseHistoryRecord, error)
	// GetPurchaseHistoryFiltered reads purchase_history rows matching the
	// PurchaseHistoryFilter, newest-first, capped at filter.Limit. Each field is
	// applied independently and only when populated (see PurchaseHistoryFilter).
	// The account predicate matches BOTH identifier columns:
	//   (cloud_account_id = ANY(AccountIDs)
	//      OR ((provider = '' OR LOWER(provider) = LOWER($p))
	//          AND account_id = ANY(ExternalIDsByProvider[p])) OR ...)
	// because purchase_history rows carry the cloud_accounts UUID FK
	// (cloud_account_id, NULL on direct-execute/ambient/pre-000011 rows) and the
	// cloud-provider external number (account_id, always populated) independently.
	// The top-bar Account chip emits the UUID; matching only one column silently
	// dropped rows that carried only the other (issues #701/#498/#866). The caller
	// resolves AccountIDs to their external account numbers grouped by provider
	// and populates ExternalIDsByProvider so rows that carry only account_id are
	// also matched, while the per-provider grouping keeps a reused external number
	// across providers (aws/123 vs azure/123) from leaking the wrong rows. The
	// provider gate is case-insensitive and a row with an empty (unknown) stored
	// provider matches on account_id alone (issue #24).
	GetPurchaseHistoryFiltered(ctx context.Context, filter PurchaseHistoryFilter) ([]PurchaseHistoryRecord, error)
	// HistorySummaryAggregate returns counts and dollar sums over every row the
	// History view would list without its row caps: purchase_history rows
	// matched with GetPurchaseHistoryFiltered's predicate and purchase_executions
	// rows matched with GetExecutionsByStatuses' predicate (issue #675). Rows are
	// grouped by the columns the History handler's account-scope check reads, so
	// the caller applies that check to the groups with the same code it applies
	// to fetched rows. Both halves are read from one snapshot.
	HistorySummaryAggregate(ctx context.Context, filter HistorySummaryFilter) (*HistorySummaryGroups, error)
	// GetPurchaseHistoryByPurchaseID returns the single purchase_history row
	// whose purchase_id matches (AWS ReservedInstancesId / Azure reservation
	// ID). Returns (nil, nil) when no row is found. Used by the revoke
	// endpoint to load the record before calling the provider cancel API
	// (issue #290) and by the marketplace-list handler to validate
	// offering_class and look up the cloud account (issue #292).
	GetPurchaseHistoryByPurchaseID(ctx context.Context, purchaseID string) (*PurchaseHistoryRecord, error)
	// MarkPurchaseRevoked stamps revoked_at, revoked_via, and optionally
	// support_case_id on a purchase_history row identified by purchase_id.
	// calcRefundAmount and calcRefundCurrency capture the Azure CalculateRefund
	// quote for audit (migration 000071, Finding #4); both nil/empty for
	// non-Azure paths or legacy rows written before the migration. Also
	// clears revocation_in_flight (migration 000072): a revoked row is no
	// longer in flight.
	// Returns a not-found error when no row matches. Idempotent: a second
	// call for the same row is a no-op (revoked_at is not overwritten when
	// it is already non-null). Used by the revoke endpoint (issue #290).
	MarkPurchaseRevoked(ctx context.Context, purchaseID string, revokedAt time.Time, revokedVia string, supportCaseID string, calcRefundAmount *float64, calcRefundCurrency string) error

	// FlipPurchaseRevocationInFlight atomically claims a purchase_history row
	// by setting revocation_in_flight=true. Called immediately before the Azure
	// Return API call so that the row can be identified by the finalize sweep if
	// the subsequent MarkPurchaseRevoked DB write fails (partial-success
	// reconciliation, issue #290 Finding #6, migration 000072). Only an
	// unrevoked, unflagged row is flipped, so exactly one concurrent caller
	// wins: a revoked row returns an error wrapping ErrPurchaseAlreadyRevoked
	// (issue #316), and a row another request already flagged returns an error
	// wrapping ErrRevocationInFlight (issue #1518). Returns a not-found error
	// when no row matches.
	FlipPurchaseRevocationInFlight(ctx context.Context, purchaseID string) error

	// ClearRevocationInFlight resets revocation_in_flight=false on a
	// purchase_history row. Called when the Azure Return call fails with a
	// transient or client error (not "already returned"), so the row is not
	// left in a permanently-sticky in-flight state that would prevent future
	// retries or mislead the finalize_revocations sweep (issue #290, second-wave
	// CR Finding D). No-op when the row is already false. Best-effort: callers
	// should log on error but not surface it to the user. Only the caller whose
	// FlipPurchaseRevocationInFlight succeeded may call it, so it never clears
	// a flag another request set (issue #1518).
	ClearRevocationInFlight(ctx context.Context, purchaseID string) error

	// GetPurchaseHistoryInFlight returns the purchase_history rows whose
	// Return was recorded by MarkRevocationReturnPosted but whose revoked_at
	// is still NULL: the Azure Return call succeeded but MarkPurchaseRevoked
	// failed. The finalize_revocations scheduled sweep calls this to retry the
	// DB write. A flagged row with no recorded Return is not returned
	// (issue #1523): no refund is known to have gone out for it.
	GetPurchaseHistoryInFlight(ctx context.Context) ([]*PurchaseHistoryRecord, error)

	// MarkRevocationReturnPosted records that the provider accepted the
	// Return for purchaseID (return_posted_at, migration 000148). Called by
	// the revoke handler after the Azure Return succeeds and before
	// MarkPurchaseRevoked. The first stamp stands; a revoked row is a no-op.
	// Returns a not-found error when no row matches (issue #1523).
	MarkRevocationReturnPosted(ctx context.Context, purchaseID string) error

	// ReleaseStaleRevocationsInFlight clears revocation_in_flight on unrevoked
	// rows flagged for longer than after with no Return recorded, so the
	// purchase can be revoked again, and returns their purchase IDs. A flagged
	// row with no flip timestamp is stamped now and aged from then. Called by
	// the finalize_revocations sweep (issue #1523).
	ReleaseStaleRevocationsInFlight(ctx context.Context, after time.Duration) ([]string, error)

	// UpdatePurchaseHistoryListing stamps the AWS marketplace listing_id and
	// listing_state onto a purchase_history row. Called after
	// CreateReservedInstancesListing succeeds (listing_state="active") and
	// on subsequent poll/cancel transitions (issue #292). Recording a listing
	// resolves the attempt, so it also clears the persisted client token and
	// price schedule (issue #525). It never marks a listing the row records as
	// canceled or closed live again; that write returns
	// ErrMarketplaceListingEnded.
	UpdatePurchaseHistoryListing(ctx context.Context, purchaseID, listingID, listingState string) error
}
