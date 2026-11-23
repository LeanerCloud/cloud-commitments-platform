package config

import (
	"context"
	"time"
)

// MarketplaceStore holds the marketplace-listing methods of StoreInterface; StoreInterface embeds it.
type MarketplaceStore interface {
	// GetMarketplaceListingAttempt returns the listing id, state and the token
	// of an unresolved list attempt of the row for purchaseID, or (nil, nil)
	// when there is none. The cancel endpoint uses it to recover a pending row
	// (issue #525).
	GetMarketplaceListingAttempt(ctx context.Context, purchaseID string) (*MarketplaceListingAttempt, error)

	// ResolveMarketplaceListingAttempt records listingID in an ended (or, to
	// clear a stuck pending row without a listing, empty) state and drops the
	// attempt token, only while the row still matches seen or already holds
	// listingID. It reports whether the row was written (issue #525).
	ResolveMarketplaceListingAttempt(ctx context.Context, purchaseID string, seen MarketplaceListingAttempt, listingID, state string) (bool, error)

	// StampOfferingClass writes the offering_class value to a purchase_history
	// row identified by purchase_id. Called by the marketplace-list handler
	// when offering_class is absent in the DB (pre-migration 000087 rows and
	// externally-created Standard RIs): after fetching the class from AWS
	// DescribeReservedInstances it is persisted so subsequent requests do not
	// incur an extra AWS API call.
	StampOfferingClass(ctx context.Context, purchaseID, offeringClass string) error

	// ClaimMarketplaceListingSlot atomically reserves the marketplace-listing
	// slot for a purchase_history row so two concurrent marketplace-list
	// requests cannot both proceed to create a duplicate AWS listing (issue
	// #292). It transitions listing_state to ListingStatePending only when the
	// row is not listed, and in the same UPDATE persists clientToken and
	// priceSchedule (JSON) unless an unresolved attempt already stored a token,
	// in which case that token and schedule are kept so the retry is idempotent
	// on AWS (issue #525). A pending row that holds a token is claimable again:
	// the same token makes a concurrent or crashed attempt safe to resume.
	// It returns the claimed row's values, or (nil, nil) when the row is active,
	// pending without a token, or absent (the caller maps that to a 409). The
	// caller must persist the real listing on success or call
	// ReleaseMarketplaceListingClaim on failure.
	ClaimMarketplaceListingSlot(ctx context.Context, purchaseID, clientToken string, priceSchedule []byte) (*MarketplaceListingClaim, error)

	// ReleaseMarketplaceListingClaim moves a pending row back to priorState
	// (empty for none) after a failed attempt. It only acts on a row whose
	// persisted token is clientToken, so a request never releases or drops the
	// attempt of another request. keepAttempt keeps the persisted token and
	// schedule so the retry resends them (ambiguous AWS outcome); otherwise
	// they are cleared because AWS created nothing reusable.
	ReleaseMarketplaceListingClaim(ctx context.Context, purchaseID, clientToken, priorState string, keepAttempt bool) error

	// ListMarketplaceListingsToPoll returns the AWS listings the marketplace
	// status poller reads (issue #27): active listings, listings recorded as
	// pending with no list attempt in flight (issue #1494), and
	// closed/canceled ones it has seen whose proceeds are not recorded yet,
	// both the RI's current listing and the listings it was relisted from
	// (issue #1494).
	ListMarketplaceListingsToPoll(ctx context.Context) ([]PurchaseHistoryRecord, error)

	// RecordMarketplaceListingActive records a listing AWS reports active:
	// it stamps listed_at (a zero listedAt keeps the stored one) while the row
	// holds listingID as active, and also moves the state to active while the
	// row holds it as pending with no list attempt in flight (issue #1494).
	// It never writes the state of any other row. Reports whether the row
	// changed.
	RecordMarketplaceListingActive(ctx context.Context, purchaseID, listingID string, listedAt time.Time) (bool, error)

	// SettleMarketplaceListing records a listing that closed or was canceled
	// on AWS, with its proceeds and AWS fee when known, in the listing's own
	// settlement row and, while the RI still holds the listing, on its
	// purchase_history row (issue #1494). It applies once per listing, only
	// while the listing is active, pending with no list attempt in flight, or
	// already in the same terminal state. Reports whether a row was written.
	SettleMarketplaceListing(ctx context.Context, settlement MarketplaceListingSettlement) (bool, error)

	// GetMarketplaceListingProceedsTotals sums each RI's marketplace proceeds
	// and fees over all of its listings (issue #1494), one total per purchase
	// and currency. RIs with no recorded listing are left out. Only purchases
	// of the given accounts count (same account arguments as
	// GetActivePurchaseHistory; empty scope is an error), and each total is an
	// estimate (issue #1586).
	GetMarketplaceListingProceedsTotals(ctx context.Context, purchaseIDs, accountIDs []string, externalIDsByProvider map[string][]string) ([]MarketplaceListingProceedsTotal, error)
}
