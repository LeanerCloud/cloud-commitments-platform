package config

import (
	"context"
	"fmt"
)

var _ RIExchangeDirectHistoryStore = (*PostgresStore)(nil)

// ListRIExchangeDirectHistory returns direct exchanges created since f.Since,
// newest first, restricted by the filter's account set. Until migration 163
// (resolved_* columns) is applied it returns ErrRIExchangeDirectResolveNotReady.
func (s *PostgresStore) ListRIExchangeDirectHistory(ctx context.Context, f RIExchangeDirectHistoryFilter) ([]RIExchangeDirectHistoryRow, error) {
	if err := requireDirectResolveSchema(ctx, s.db); err != nil {
		return nil, err
	}
	ids := f.AccountIDs
	if ids == nil {
		ids = []string{}
	}
	rows, err := s.db.Query(ctx, `
		SELECT id::text, provider, status, cloud_account_id, provider_account_id,
		       COALESCE(region, ''), source_ids, targets::text,
		       max_payment_due::text, max_payment_currency,
		       net_payable::text, COALESCE(net_payable_currency, ''),
		       COALESCE(error, ''), executed_by, created_at, completed_at,
		       COALESCE(resolved_status, ''), resolved_at
		FROM ri_exchange_direct_executions
		WHERE created_at >= $1
		  AND ($2::boolean OR cloud_account_id = ANY($3::text[]))
		ORDER BY created_at DESC, id
		LIMIT $4`, f.Since, f.AllAccounts, ids, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list ri exchange direct history: %w", err)
	}
	defer rows.Close()
	var out []RIExchangeDirectHistoryRow
	for rows.Next() {
		var r RIExchangeDirectHistoryRow
		var targets string
		if err := rows.Scan(&r.ID, &r.Provider, &r.Status, &r.CloudAccountID, &r.ProviderAccountID,
			&r.Region, &r.SourceIDs, &targets, &r.MaxPaymentDue, &r.MaxPaymentCurrency,
			&r.NetPayable, &r.NetPayableCurrency, &r.Error, &r.ExecutedBy, &r.CreatedAt, &r.CompletedAt,
			&r.ResolvedStatus, &r.ResolvedAt); err != nil {
			return nil, fmt.Errorf("failed to scan ri exchange direct history row: %w", err)
		}
		r.Targets = []byte(targets)
		out = append(out, r)
	}
	return out, rows.Err()
}
