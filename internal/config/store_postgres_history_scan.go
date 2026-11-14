package config

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// scanPurchaseHistoryRow scans a single purchase_history row and reconciles the
// nullable columns into a PurchaseHistoryRecord. It is pulled out of
// queryPurchaseHistory to keep that function under the cyclomatic limit. The
// column order must match the SELECT lists in GetPurchaseHistory /
// GetAllPurchaseHistory / GetPurchaseHistoryFiltered (base columns, then the
// revocation columns from issue #290, then the marketplace columns from #292).
// purchaseHistoryNullables holds the nullable scan targets shared by every
// purchase_history reader (scanPurchaseHistoryRow, GetPurchaseHistoryByPurchaseID).
// Centralizing the NULL reconciliation in applyTo keeps each reader's
// cyclomatic complexity under the gocyclo budget as columns accrue across
// issues #290 (revocation) and #292 (marketplace).
type purchaseHistoryNullables struct {
	upfrontCost              sql.NullFloat64
	monthlyCost              sql.NullFloat64
	planID                   sql.NullString
	planName                 sql.NullString
	cloudAccountID           sql.NullString
	revocationWindowClosesAt *time.Time
	revokedAt                *time.Time
	revokedVia               sql.NullString
	supportCaseID            sql.NullString
	offeringClass            sql.NullString
	listingID                sql.NullString
	listingState             sql.NullString
	amountBasis              sql.NullString
}

// applyTo reconciles the nullable columns into record. monthly_cost is left as
// a nil pointer when the provider returned no monthly breakdown (distinct from
// an explicit $0 recurring charge); the revocation-window timestamps map
// directly onto their pointer fields.
func (n *purchaseHistoryNullables) applyTo(record *PurchaseHistoryRecord) {
	record.UpfrontCost = nullPtrFromNullFloat64(n.upfrontCost)
	if n.monthlyCost.Valid {
		v := n.monthlyCost.Float64
		record.MonthlyCost = &v
	}
	if n.planID.Valid {
		record.PlanID = n.planID.String
	}
	if n.planName.Valid {
		record.PlanName = n.planName.String
	}
	if n.cloudAccountID.Valid {
		record.CloudAccountID = &n.cloudAccountID.String
	}
	record.RevocationWindowClosesAt = n.revocationWindowClosesAt
	record.RevokedAt = n.revokedAt
	if n.revokedVia.Valid {
		record.RevokedVia = n.revokedVia.String
	}
	if n.supportCaseID.Valid {
		record.SupportCaseID = n.supportCaseID.String
	}
	if n.offeringClass.Valid {
		record.OfferingClass = n.offeringClass.String
	}
	if n.listingID.Valid {
		record.ListingID = n.listingID.String
	}
	if n.listingState.Valid {
		record.ListingState = n.listingState.String
	}
	record.AmountBasis = PurchaseAmountBasis(n.amountBasis.String) // NULL scans as ""
}

func scanPurchaseHistoryRow(rows pgx.Rows) (PurchaseHistoryRecord, error) {
	var record PurchaseHistoryRecord
	var n purchaseHistoryNullables

	if err := rows.Scan(
		&record.AccountID,
		&record.PurchaseID,
		&record.Timestamp,
		&record.Provider,
		&record.Service,
		&record.Region,
		&record.ResourceType,
		&record.Count,
		&record.Term,
		&record.Payment,
		&n.upfrontCost,
		&n.monthlyCost,
		&record.EstimatedSavings,
		&n.planID,
		&n.planName,
		&record.RampStep,
		&n.cloudAccountID,
		&n.revocationWindowClosesAt,
		&n.revokedAt,
		&n.revokedVia,
		&n.supportCaseID,
		&n.offeringClass,
		&n.listingID,
		&n.listingState,
		&n.amountBasis,
	); err != nil {
		return PurchaseHistoryRecord{}, fmt.Errorf("failed to scan purchase history: %w", err)
	}

	n.applyTo(&record)
	return record, nil
}
