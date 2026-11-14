package config

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/accountpredicate"
)

// GetPurchaseHistory retrieves purchase history for an account.
func (s *PostgresStore) GetPurchaseHistory(ctx context.Context, accountID string, limit int) ([]PurchaseHistoryRecord, error) {
	basis, err := s.amountBasisColumn(ctx)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`
		SELECT account_id, purchase_id, timestamp, provider, service, region,
		       resource_type, count, term, payment, upfront_cost, monthly_cost,
		       estimated_savings, plan_id, plan_name, ramp_step, cloud_account_id,
		       revocation_window_closes_at, revoked_at, revoked_via, support_case_id,
		       offering_class, listing_id, listing_state, %s
		FROM purchase_history
		WHERE account_id = $1
		ORDER BY timestamp DESC
		LIMIT $2
	`, basis)

	return s.queryPurchaseHistory(ctx, query, accountID, limit)
}

// GetAllPurchaseHistory retrieves all purchase history.
func (s *PostgresStore) GetAllPurchaseHistory(ctx context.Context, limit int) ([]PurchaseHistoryRecord, error) {
	basis, err := s.amountBasisColumn(ctx)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`
		SELECT account_id, purchase_id, timestamp, provider, service, region,
		       resource_type, count, term, payment, upfront_cost, monthly_cost,
		       estimated_savings, plan_id, plan_name, ramp_step, cloud_account_id,
		       revocation_window_closes_at, revoked_at, revoked_via, support_case_id,
		       offering_class, listing_id, listing_state, %s
		FROM purchase_history
		ORDER BY timestamp DESC
		LIMIT $1
	`, basis)

	return s.queryPurchaseHistory(ctx, query, limit)
}

// activeExpiryEpochExpr is a purchase_history row's commitment expiry
// (timestamp + term * 8760 hours) as Unix epoch seconds. It must stay the
// same expression as the one indexed by
// idx_purchase_history_active_expiry (migration 000126), or the planner cannot
// use that index and GetActivePurchaseHistory falls back to evaluating every
// row (issue #33).
//
// The obvious form, timestamp + make_interval(hours => term * 8760), cannot be
// indexed: timestamptz + interval is only STABLE because day and month
// intervals depend on the session time zone. Converting to UTC wall time and
// taking the epoch is IMMUTABLE, and for an hours-only interval it is exact
// arithmetic on the same instant, so the rows matched are unchanged. numeric
// arithmetic cannot overflow, so no term value can make an INSERT fail on the
// index expression. 31536000 = 8760 * 3600, the same 365-day year as the
// analytics collector's HoursPerYear.
const activeExpiryEpochExpr = "(extract(epoch FROM (timestamp AT TIME ZONE 'UTC')) + term::numeric * 31536000)"

// GetActivePurchaseHistory retrieves every purchase_history row still within its
// commitment term at asOf, optionally scoped to a set of accounts via the same
// dual-column predicate as GetPurchaseHistoryFiltered (appendAccountPredicate):
// both accountIDs (cloud_accounts UUIDs matched on cloud_account_id) and
// externalIDsByProvider (provider-scoped external numbers matched on
// account_id) are applied with OR semantics so rows carrying only one
// identifier are still returned (issues #701/#498/#866). Both empty means all
// accounts. The active filter is pushed into SQL so the result is bounded by
// the number of live commitments (not by all history ever recorded), which is
// what the analytics collector, dashboard KPIs, and inventory endpoints need:
// it cannot silently truncate older-but-still-active 1y/3y commitments the way
// a newest-first LIMIT page does (issue #1140). term*8760 hours matches the
// collector's HoursPerYear and the API layer's commitmentExpiry (both 365*24)
// so the SQL and Go term windows agree. The expiry comparison is inclusive
// (expiry >= asOf): a commitment expiring exactly at asOf is still active,
// matching the API layer's isActiveCommitment (!now.After(expiry)) so the SQL
// result set and the Go-side active checks share one boundary definition.
//
// The expiry is compared as Unix epoch seconds (activeExpiryEpochExpr) so the
// predicate can use idx_purchase_history_active_expiry (migration 000126)
// instead of evaluating every row ever recorded (issue #33).
func (s *PostgresStore) GetActivePurchaseHistory(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]PurchaseHistoryRecord, error) {
	basis, err := s.amountBasisColumn(ctx)
	if err != nil {
		return nil, err
	}
	query, args := activePurchaseHistoryQuery(basis, asOf, accountIDs, externalIDsByProvider)
	return s.queryPurchaseHistory(ctx, query, args...)
}

// activePurchaseHistoryQuery builds GetActivePurchaseHistory's SQL and bind
// args. It is separate so the integration tests can EXPLAIN the exact query.
func activePurchaseHistoryQuery(basis string, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) (query string, args []any) {
	conds := []string{
		"term > 0",
		activeExpiryEpochExpr + " >= extract(epoch FROM ($1::timestamptz AT TIME ZONE 'UTC'))",
		"revoked_at IS NULL",
	}
	args = []any{asOf}
	conds, args = appendAccountPredicate(conds, args, accountIDs, externalIDsByProvider)

	// The column list MUST match scanPurchaseHistoryRow's scan destinations
	// exactly (pgx errors on a field/destination count mismatch). #808 added
	// offering_class, listing_id, listing_state to the scanner and every other
	// purchase-history SELECT but missed this one, which broke every real-Postgres
	// call here (dashboard KPIs, /api/inventory/*, analytics collector).
	query = fmt.Sprintf(`
		SELECT account_id, purchase_id, timestamp, provider, service, region,
		       resource_type, count, term, payment, upfront_cost, monthly_cost,
		       estimated_savings, plan_id, plan_name, ramp_step, cloud_account_id,
		       revocation_window_closes_at, revoked_at, revoked_via, support_case_id,
		       offering_class, listing_id, listing_state, %s
		FROM purchase_history
		WHERE %s
		ORDER BY timestamp DESC
	`, basis, strings.Join(conds, " AND "))
	return query, args
}

// appendAccountPredicate appends the dual-column account predicate to the
// GetPurchaseHistoryFiltered / GetActivePurchaseHistory AND chain.
//
// Either input alone is sufficient: a UUID-only filter still matches
// NULL-cloud_account_id rows via the resolved external IDs, and an
// external-only filter matches UUID-only rows once the caller resolves them.
// Both empty returns conds/args unchanged (no account clause -> all accounts).
//
// The predicate is built by internal/accountpredicate, shared with the
// analytics reads in internal/analytics and internal/api (issue #120). This
// path uses the case-insensitive provider gate, where a row whose stored
// provider is empty (unknown) matches on account_id alone; without it a
// NULL-cloud_account_id row stored as "AWS" or "" was silently dropped even
// though its account_id matched (issue #24). A row stored under a different,
// non-empty provider is still excluded.
func appendAccountPredicate(conds []string, args []any, accountIDs []string, externalIDsByProvider map[string][]string) (outConds []string, outArgs []any) {
	clause, args := accountpredicate.Build(args, accountIDs, externalIDsByProvider, accountpredicate.ProviderCaseInsensitiveOrUnknown)
	if clause == "" {
		return conds, args
	}
	return append(conds, clause), args
}
