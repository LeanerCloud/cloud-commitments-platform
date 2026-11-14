package config

import (
	"context"
	"fmt"
	"strings"
)

// GetPurchaseHistoryFiltered reads purchase_history rows matching the
// supplied filter set, newest-first, capped at filter.Limit. See the
// StoreInterface docstring and PurchaseHistoryFilter for the per-field
// semantics. Each WHERE clause is appended only when its field is populated,
// so an empty filter gets the same plan-shape as GetAllPurchaseHistory.
// Implementation mirrors buildRecommendationFilter
// (store_postgres_recommendations.go).
//
// The account predicate matches BOTH identifier columns:
//
//	(cloud_account_id = ANY($uuids)
//	   OR ((provider = '' OR LOWER(provider) = LOWER($p)) AND account_id = ANY($extsForP)) OR ...)
//
// purchase_history carries two account identifiers and either may be the only
// one populated on a given row: cloud_account_id (the cloud_accounts UUID FK,
// added in migration 000011 with no backfill, so NULL on every direct-execute /
// ambient / pre-000011 row) and account_id (the cloud-provider external number,
// e.g. an AWS account number, always populated). The top-bar Account chip emits
// the UUID, so a UUID-only predicate silently dropped every NULL-cloud_account_id
// row (issue #701/#498) while an external-only predicate dropped every row that
// only has the UUID (issue #866). Matching both columns includes rows written by
// either path. The caller resolves the requested UUIDs to their (provider,
// external_id) pairs scoped to the user's accessible accounts and groups the
// external ids by provider, so the external-id half stays provider-scoped and a
// reused external number (aws/123 vs azure/123) cannot leak the wrong rows.
func (s *PostgresStore) GetPurchaseHistoryFiltered(
	ctx context.Context,
	filter PurchaseHistoryFilter,
) ([]PurchaseHistoryRecord, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}

	basis, err := s.amountBasisColumn(ctx)
	if err != nil {
		return nil, err
	}
	where, args := purchaseHistoryFilterWhere(filter)
	args = append(args, limit)

	query := fmt.Sprintf(`
		SELECT account_id, purchase_id, timestamp, provider, service, region,
		       resource_type, count, term, payment, upfront_cost, monthly_cost,
		       estimated_savings, plan_id, plan_name, ramp_step, cloud_account_id,
		       revocation_window_closes_at, revoked_at, revoked_via, support_case_id,
		       offering_class, listing_id, listing_state, %s
		FROM purchase_history%s
		ORDER BY timestamp DESC
		LIMIT $%d
	`, basis, where, len(args))

	return s.queryPurchaseHistory(ctx, query, args...)
}

// purchaseHistoryFilterWhere builds the WHERE clause (with a leading space, or
// "" for an empty filter) and its bind args for a PurchaseHistoryFilter. It is
// the single predicate builder shared by GetPurchaseHistoryFiltered (the
// History row fetch) and HistorySummaryAggregate (the History summary totals),
// so the totals always count exactly the rows the fetch would return without
// its LIMIT (issue #675). filter.Limit is not part of the predicate and is
// ignored here; placeholders are numbered from $1.
func purchaseHistoryFilterWhere(filter PurchaseHistoryFilter) (where string, args []any) {
	var conds []string
	add := func(cond string, val any) {
		conds = append(conds, fmt.Sprintf(cond, len(args)+1))
		args = append(args, val)
	}
	if filter.Provider != "" {
		add("provider = $%d", filter.Provider)
	}
	if filter.PlanID != "" {
		add("plan_id = $%d", filter.PlanID)
	}
	conds, args = appendAccountPredicate(conds, args, filter.AccountIDs, filter.ExternalIDsByProvider)
	if filter.Scope != nil {
		// A non-nil empty slice, so no allowed account binds NULL and the
		// comparison matches no row.
		add("account_id = ANY($%d)", append([]string{}, filter.Scope.AccountIDs...))
	}
	if filter.Start != nil {
		add("timestamp >= $%d", *filter.Start)
	}
	if filter.End != nil {
		add("timestamp <= $%d", *filter.End)
	}
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	return where, args
}

// queryPurchaseHistory is a helper to query and scan purchase history.
// The query must SELECT the following columns in order:
//
//	account_id, purchase_id, timestamp, provider, service, region,
//	resource_type, count, term, payment, upfront_cost, monthly_cost,
//	estimated_savings, plan_id, plan_name, ramp_step, cloud_account_id,
//	revocation_window_closes_at, revoked_at, revoked_via, support_case_id,
//	offering_class, listing_id, listing_state
//
// The revocation columns were added in migration 000057; the marketplace
// listing columns (offering_class, listing_id, listing_state) in the #292
// migration. Queries must include them explicitly so the Scan targets stay
// in sync.
func (s *PostgresStore) queryPurchaseHistory(ctx context.Context, query string, args ...any) ([]PurchaseHistoryRecord, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query purchase history: %w", err)
	}
	defer rows.Close()

	records := make([]PurchaseHistoryRecord, 0)
	for rows.Next() {
		record, err := scanPurchaseHistoryRow(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}

	return records, rows.Err()
}
