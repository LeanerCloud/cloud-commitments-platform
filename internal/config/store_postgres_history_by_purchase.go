package config

import (
	"context"
	"fmt"
	"time"
)

// GetPurchaseHistoryByPurchaseID returns the single purchase_history row
// whose purchase_id matches purchaseID. Returns (nil, nil) when the row
// does not exist. The revocation-window columns (revocation_window_closes_at,
// revoked_at, revoked_via, support_case_id) are read alongside the base
// columns so the revoke endpoint can check idempotency without a second round
// trip (issue #290). The marketplace columns (offering_class, listing_id,
// listing_state) are read so the marketplace-list handler can validate
// offering_class and look up the cloud account (issue #292). Rows saved before
// migration 000108 can share a purchase_id; the earliest one (created_at, then
// id) is returned, the same row ClaimMarketplaceListingSlot claims (#267).
// return_posted_at (migration 000148) is read last so the revoke handler can
// tell an accepted Azure Return from one still in progress (issue #1522).
func (s *PostgresStore) GetPurchaseHistoryByPurchaseID(ctx context.Context, purchaseID string) (*PurchaseHistoryRecord, error) {
	basis, err := s.amountBasisColumn(ctx)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`
		SELECT account_id, purchase_id, timestamp, provider, service, region,
		       resource_type, count, term, payment, upfront_cost, monthly_cost,
		       estimated_savings, plan_id, plan_name, ramp_step, cloud_account_id,
		       revocation_window_closes_at, revoked_at, revoked_via, support_case_id,
		       revocation_in_flight, offering_class, listing_id, listing_state,
		       return_posted_at, %s
		FROM purchase_history
		WHERE purchase_id = $1
		ORDER BY created_at, id
		LIMIT 1
	`, basis)
	rows, err := s.db.Query(ctx, query, purchaseID)
	if err != nil {
		return nil, fmt.Errorf("GetPurchaseHistoryByPurchaseID: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, rows.Err()
	}

	var r PurchaseHistoryRecord
	var n purchaseHistoryNullables

	// Note the extra revocation_in_flight bool between support_case_id and the
	// marketplace columns; it scans straight into r.RevocationInFlight rather
	// than through the shared nullables struct.
	if err := rows.Scan(
		&r.AccountID,
		&r.PurchaseID,
		&r.Timestamp,
		&r.Provider,
		&r.Service,
		&r.Region,
		&r.ResourceType,
		&r.Count,
		&r.Term,
		&r.Payment,
		&n.upfrontCost,
		&r.MonthlyCost,
		&r.EstimatedSavings,
		&n.planID,
		&n.planName,
		&r.RampStep,
		&n.cloudAccountID,
		&n.revocationWindowClosesAt,
		&n.revokedAt,
		&n.revokedVia,
		&n.supportCaseID,
		&r.RevocationInFlight,
		&n.offeringClass,
		&n.listingID,
		&n.listingState,
		&r.ReturnPostedAt,
		&n.amountBasis,
	); err != nil {
		return nil, fmt.Errorf("GetPurchaseHistoryByPurchaseID scan: %w", err)
	}

	// MonthlyCost is scanned directly above (not through n.monthlyCost), so
	// leave n.monthlyCost zero-valued; applyTo will not overwrite r.MonthlyCost.
	n.applyTo(&r)

	return &r, rows.Err()
}

// MarkPurchaseRevoked stamps revoked_at / revoked_via / support_case_id and
// the refund-quote audit columns (calc_refund_amount, calc_refund_currency) on
// the purchase_history row identified by purchaseID, and clears
// revocation_in_flight as migration 000072 documents: a completed revocation
// is no longer in flight (issue #316). The UPDATE is a no-op when revoked_at
// is already non-null (idempotency guard). Returns a not-found error when zero
// rows are affected and revoked_at was previously NULL.
func (s *PostgresStore) MarkPurchaseRevoked(ctx context.Context, purchaseID string, revokedAt time.Time, revokedVia, supportCaseID string, calcRefundAmount *float64, calcRefundCurrency string) error {
	var supportCaseIDPtr *string
	if supportCaseID != "" {
		supportCaseIDPtr = &supportCaseID
	}
	var calcCurrencyPtr *string
	if calcRefundCurrency != "" {
		calcCurrencyPtr = &calcRefundCurrency
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE purchase_history
		   SET revoked_at            = $2,
		       revoked_via           = $3,
		       support_case_id       = $4,
		       calc_refund_amount    = $5,
		       calc_refund_currency  = $6,
		       revocation_in_flight  = false
		 WHERE purchase_id = $1
		   AND revoked_at IS NULL
	`, purchaseID, revokedAt, revokedVia, supportCaseIDPtr, calcRefundAmount, calcCurrencyPtr)
	if err != nil {
		return fmt.Errorf("MarkPurchaseRevoked: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either no row found or already revoked — check which.
		var exists bool
		err2 := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM purchase_history WHERE purchase_id=$1)`, purchaseID).Scan(&exists)
		if err2 != nil {
			return fmt.Errorf("MarkPurchaseRevoked existence check: %w", err2)
		}
		if !exists {
			return fmt.Errorf("MarkPurchaseRevoked: purchase_id %q not found", purchaseID)
		}
		// Already revoked — idempotent, treat as success.
	}
	return nil
}
