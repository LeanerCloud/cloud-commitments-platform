package config

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// FlipPurchaseRevocationInFlight claims the revocation of the
// purchase_history row for purchaseID by setting revocation_in_flight=true.
// Called immediately before the Azure Return API call to enable
// partial-success reconciliation (issue #290 Finding #6).
//
// It is a compare-and-set on two predicates:
//   - revoked_at IS NULL (issue #316): a stale request that reaches the flip
//     after another request's MarkPurchaseRevoked committed must not flag the
//     revoked row again, since MarkPurchaseRevoked (guarded on the same
//     predicate) would then never clear it. That returns an error wrapping
//     ErrPurchaseAlreadyRevoked.
//   - revocation_in_flight = false (issue #1518): only one request may hold
//     the claim, so two first-time revokes cannot both post a Return. The
//     request that loses gets an error wrapping ErrRevocationInFlight.
//
// The row lock serializes concurrent flips and marks, and under READ
// COMMITTED a waiting UPDATE re-checks both predicates against the committed
// row, so exactly one flip of an unrevoked, unflagged row succeeds. A missing
// row keeps the not-found error.
func (s *PostgresStore) FlipPurchaseRevocationInFlight(ctx context.Context, purchaseID string) error {
	// revocation_in_flight_at (migration 000148, issue #1523) is the database
	// clock at the flip; the finalize_revocations sweep uses it to release a
	// flag whose Return was never recorded.
	tag, err := s.db.Exec(ctx, `
		UPDATE purchase_history
		   SET revocation_in_flight = true, revocation_in_flight_at = now()
		 WHERE purchase_id = $1
		   AND revoked_at IS NULL
		   AND revocation_in_flight = false
	`, purchaseID)
	if err != nil {
		return fmt.Errorf("FlipPurchaseRevocationInFlight: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	// Nothing matched: tell a missing row from a revoked one and from one
	// another request has claimed. A row that is unrevoked when re-read lost
	// the claim to a concurrent request; it is reported in flight even if
	// that request has since cleared the flag, so this caller never posts a
	// Return it did not claim.
	var total, unrevoked int
	if err := s.db.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE revoked_at IS NULL)
		  FROM purchase_history
		 WHERE purchase_id = $1
	`, purchaseID).Scan(&total, &unrevoked); err != nil {
		return fmt.Errorf("FlipPurchaseRevocationInFlight existence check: %w", err)
	}
	if total == 0 {
		return fmt.Errorf("FlipPurchaseRevocationInFlight: purchase_id %q not found", purchaseID)
	}
	if unrevoked == 0 {
		return fmt.Errorf("FlipPurchaseRevocationInFlight: purchase_id %q: %w", purchaseID, ErrPurchaseAlreadyRevoked)
	}
	return fmt.Errorf("FlipPurchaseRevocationInFlight: purchase_id %q: %w", purchaseID, ErrRevocationInFlight)
}

// ClearRevocationInFlight sets revocation_in_flight=false on a purchase_history
// row. Called when the Azure Return call fails transiently (before Azure actually
// issued a refund) so the row is not left stuck in the in-flight state, which
// would mislead the finalize_revocations sweep into thinking Azure succeeded
// (issue #290, second-wave CR Finding D). No-op when already false. The
// UPDATE is unconditional: only the request whose
// FlipPurchaseRevocationInFlight claimed the row may call it (issue #1518).
func (s *PostgresStore) ClearRevocationInFlight(ctx context.Context, purchaseID string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE purchase_history
		   SET revocation_in_flight = false
		 WHERE purchase_id = $1
	`, purchaseID)
	if err != nil {
		return fmt.Errorf("ClearRevocationInFlight: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ClearRevocationInFlight: purchase_id %q not found", purchaseID)
	}
	return nil
}

// GetPurchaseHistoryInFlight returns the purchase_history rows whose Azure
// Return was recorded as accepted (return_posted_at set by
// MarkRevocationReturnPosted) but whose revoked_at is still NULL. Used by the
// finalize_revocations scheduled sweep to retry MarkPurchaseRevoked for rows
// where the Azure Return succeeded but the subsequent DB write failed
// (issue #290 Finding #6).
//
// revocation_in_flight alone is not enough (issue #1523): a row is also left
// flagged when a request dies between the flip and the Return, and marking
// that row revoked would record a refund that never happened. The flag is not
// required either: once the Return is recorded the purchase is refunded, even
// if a concurrent request's failed Return cleared the flag.
func (s *PostgresStore) GetPurchaseHistoryInFlight(ctx context.Context) ([]*PurchaseHistoryRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT account_id, purchase_id, timestamp, provider, service, region,
		       resource_type, count, term, payment, upfront_cost, monthly_cost,
		       estimated_savings, plan_id, plan_name, ramp_step, cloud_account_id,
		       revocation_window_closes_at, revoked_at, revoked_via, support_case_id,
		       revocation_in_flight
		FROM purchase_history
		WHERE return_posted_at IS NOT NULL
		  AND revoked_at IS NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("GetPurchaseHistoryInFlight: %w", err)
	}
	defer rows.Close()

	var result []*PurchaseHistoryRecord
	for rows.Next() {
		var r PurchaseHistoryRecord
		var planID, planName, cloudAccountID sql.NullString
		var revocationWindowClosesAt, revokedAt *time.Time
		var revokedVia, supportCaseID sql.NullString
		var upfrontCost sql.NullFloat64

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
			&upfrontCost,
			&r.MonthlyCost,
			&r.EstimatedSavings,
			&planID,
			&planName,
			&r.RampStep,
			&cloudAccountID,
			&revocationWindowClosesAt,
			&revokedAt,
			&revokedVia,
			&supportCaseID,
			&r.RevocationInFlight,
		); err != nil {
			return nil, fmt.Errorf("GetPurchaseHistoryInFlight scan: %w", err)
		}
		r.UpfrontCost = nullPtrFromNullFloat64(upfrontCost)

		if planID.Valid {
			r.PlanID = planID.String
		}
		if planName.Valid {
			r.PlanName = planName.String
		}
		if cloudAccountID.Valid {
			r.CloudAccountID = &cloudAccountID.String
		}
		r.RevocationWindowClosesAt = revocationWindowClosesAt
		r.RevokedAt = revokedAt
		if revokedVia.Valid {
			r.RevokedVia = revokedVia.String
		}
		if supportCaseID.Valid {
			r.SupportCaseID = supportCaseID.String
		}
		result = append(result, &r)
	}
	return result, rows.Err()
}
