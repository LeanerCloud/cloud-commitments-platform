package config

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ==========================================
// PURCHASE HISTORY
// ==========================================

// UpdatePurchaseHistoryListing stamps the marketplace listing fields onto a
// purchase_history row identified by its purchase_id (RI reservation ID).
// Called by the marketplace-list handler after CreateReservedInstancesListing
// succeeds (listing_id + listing_state="active") and by the status-poll path on
// the later closed/canceled transitions.
func (s *PostgresStore) UpdatePurchaseHistoryListing(ctx context.Context, purchaseID, listingID, listingState string) error {
	// A listing recorded as canceled or closed is never marked live again
	// (issue #525): a list request that lost a race with the cancel endpoint
	// gets ErrMarketplaceListingEnded instead of reviving the canceled listing.
	// Writing a new listing id (a relist) drops the previous listing's
	// listed_at and settlement in the same statement (issue #27), so the new
	// listing can be settled even if it ends before the poller sees it active.
	// The previous listing itself is kept in marketplace_listing_settlements
	// by trigger purchase_history_keep_superseded_listing (migration 000153,
	// issue #1494), where the poller still settles it.
	query := `
		UPDATE purchase_history
		SET listing_id = $1, listing_state = $2,
		    listing_client_token = NULL, listing_price_schedule = NULL,
		    listed_at = CASE WHEN COALESCE(listing_id, '') = $1 THEN listed_at END,
		    listing_proceeds_received = CASE WHEN COALESCE(listing_id, '') = $1 THEN listing_proceeds_received END,
		    listing_fee_paid = CASE WHEN COALESCE(listing_id, '') = $1 THEN listing_fee_paid END
		WHERE purchase_id = $3
		  AND NOT (COALESCE(listing_id, '') = $1
		           AND lower(COALESCE(listing_state, '')) IN ($4, $5)
		           AND lower($2) NOT IN ($4, $5))
	`
	tag, err := s.db.Exec(ctx, query, listingID, listingState, purchaseID, ListingStateCancelled, ListingStateClosed)
	if err != nil {
		return fmt.Errorf("failed to update listing state for purchase %s: %w", purchaseID, err)
	}
	if tag.RowsAffected() == 0 {
		return s.listingEndedOrMissing(ctx, purchaseID)
	}
	return nil
}

// StampOfferingClass writes the offering_class value onto a purchase_history
// row identified by purchase_id. Used by the marketplace-list handler to
// lazily persist offering_class fetched from AWS DescribeReservedInstances for
// rows created before migration 000087 or for externally-created Standard RIs.
// A no-match (row not found) is treated as a non-fatal warning by callers.
func (s *PostgresStore) StampOfferingClass(ctx context.Context, purchaseID, offeringClass string) error {
	query := `UPDATE purchase_history SET offering_class = $1 WHERE purchase_id = $2`
	_, err := s.db.Exec(ctx, query, offeringClass, purchaseID)
	if err != nil {
		return fmt.Errorf("failed to stamp offering_class for purchase %s: %w", purchaseID, err)
	}
	return nil
}

// ClaimMarketplaceListingSlot atomically reserves the marketplace-listing slot
// for a purchase_history row so two concurrent marketplace-list requests cannot
// both proceed to create a duplicate AWS listing (issue #292). The single
// conditional UPDATE transitions listing_state to ListingStatePending unless the
// row is active, or pending without a persisted token. It stores clientToken and
// priceSchedule in the same statement unless an earlier attempt already stored a
// token, and returns the row's values as of the claim (issue #525): the handler
// must not trust a row read before the claim. It returns (nil, nil) when the row
// could not be claimed (active, pending without a token, or absent). The
// claimed row keeps the previous listing id but no longer records that
// listing's state, so trigger purchase_history_keep_superseded_listing copies
// the previous listing into marketplace_listing_settlements when the token is
// first stored (migration 000153, issue #1494).
//
// Rows saved before migration 000108 can share a purchase_id (#267). The claim
// then works on the earliest row (created_at, then id), the one
// GetPurchaseHistoryByPurchaseID returns, and refuses while any duplicate
// records an active listing, so the returned prior state and listing id belong
// to one row and a duplicate never reopens a listed RI.
func (s *PostgresStore) ClaimMarketplaceListingSlot(ctx context.Context, purchaseID, clientToken string, priceSchedule []byte) (*MarketplaceListingClaim, error) {
	// prev locks the earliest row first, so a claim that races another one
	// waits for it and then sees its committed token and state instead of a
	// stale snapshot. created_at and id never change, so the row prev picks
	// stays the earliest while it waits.
	query := `
		WITH prev AS (
			SELECT id, listing_state AS prior_state
			FROM purchase_history WHERE purchase_id = $2
			ORDER BY created_at, id
			LIMIT 1
			FOR UPDATE
		)
		UPDATE purchase_history AS p
		SET listing_state = $1,
		    listing_client_token = COALESCE(p.listing_client_token, $3::text),
		    listing_price_schedule = CASE WHEN p.listing_client_token IS NULL
		                                  THEN $4::jsonb ELSE p.listing_price_schedule END
		FROM prev
		WHERE p.id = prev.id
		  AND lower(COALESCE(p.listing_state, '')) <> $5
		  AND (lower(COALESCE(p.listing_state, '')) <> $1 OR p.listing_client_token IS NOT NULL)
		  AND NOT EXISTS (
			SELECT 1 FROM purchase_history AS twin
			WHERE twin.purchase_id = $2 AND twin.id <> p.id
			  AND lower(COALESCE(twin.listing_state, '')) = $5
		  )
		RETURNING COALESCE(p.listing_id, ''), COALESCE(prev.prior_state, ''),
		          p.listing_client_token, p.listing_price_schedule::text
	`
	var claim MarketplaceListingClaim
	var schedule string
	err := s.db.QueryRow(ctx, query, ListingStatePending, purchaseID, clientToken, priceSchedule, ListingStateActive).
		Scan(&claim.ListingID, &claim.PriorState, &claim.ClientToken, &schedule)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to claim marketplace listing slot for purchase %s: %w", purchaseID, err)
	}
	claim.PriceSchedule = []byte(schedule)
	claim.Resumed = claim.ClientToken != clientToken
	return &claim, nil
}

// ReleaseMarketplaceListingClaim moves a pending row back to priorState and,
// unless keepAttempt, clears the persisted token and schedule. It only touches
// a pending row whose token is clientToken, so it never overwrites a recorded
// listing or the attempt of another request.
func (s *PostgresStore) ReleaseMarketplaceListingClaim(ctx context.Context, purchaseID, clientToken, priorState string, keepAttempt bool) error {
	query := `
		UPDATE purchase_history
		SET listing_state = NULLIF($2, ''),
		    listing_client_token = CASE WHEN $3 THEN listing_client_token END,
		    listing_price_schedule = CASE WHEN $3 THEN listing_price_schedule END
		WHERE purchase_id = $1 AND lower(COALESCE(listing_state, '')) = $4
		  AND listing_client_token = $5
	`
	if _, err := s.db.Exec(ctx, query, purchaseID, priorState, keepAttempt, ListingStatePending, clientToken); err != nil {
		return fmt.Errorf("failed to release marketplace listing claim for purchase %s: %w", purchaseID, err)
	}
	return nil
}
