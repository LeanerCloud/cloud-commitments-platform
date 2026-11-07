package config

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ListUnresolvedRIExchangeDirectAccountRegions returns the distinct (account,
// region) pairs of unresolved AWS rows, so the handler can ask the
// authorization service about each pair once. The unresolved partial index
// serves it.
func (s *PostgresStore) ListUnresolvedRIExchangeDirectAccountRegions(ctx context.Context) ([]RIExchangeDirectAccountRegion, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT cloud_account_id, COALESCE(region, '')
		FROM ri_exchange_direct_executions
		WHERE provider = 'aws' AND status IN ('submitted', 'unknown') AND resolved_at IS NULL
		ORDER BY 1 NULLS FIRST, 2`)
	if err != nil {
		return nil, fmt.Errorf("failed to list unresolved ri exchange direct accounts: %w", err)
	}
	defer rows.Close()
	var out []RIExchangeDirectAccountRegion
	for rows.Next() {
		var p RIExchangeDirectAccountRegion
		if err := rows.Scan(&p.CloudAccountID, &p.Region); err != nil {
			return nil, fmt.Errorf("failed to scan unresolved ri exchange direct account: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetRIExchangeDirectExecution returns one AWS direct row of any status, or
// ErrRIExchangeResolveNotFound for a missing or non-AWS row.
func (s *PostgresStore) GetRIExchangeDirectExecution(ctx context.Context, id string) (*RIExchangeDirectRow, error) {
	r, err := scanRIExchangeDirectRow(s.db.QueryRow(ctx, `
		SELECT`+riExchangeDirectRowColumns+`
		FROM ri_exchange_direct_executions
		WHERE id = $1 AND provider = 'aws'`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRIExchangeResolveNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read ri exchange direct execution %s: %w", id, err)
	}
	return &r, nil
}

// RIExchangeDirectListStore is the read side of the direct-row resolve API.
// Errors are the ErrRIExchangeResolve* values.
type RIExchangeDirectListStore interface {
	ListUnresolvedRIExchangeDirectAccountRegions(ctx context.Context) ([]RIExchangeDirectAccountRegion, error)
	ListUnresolvedRIExchangeDirectExecutions(ctx context.Context, f RIExchangeDirectListFilter) (*RIExchangeDirectListPage, error)
	GetRIExchangeDirectExecution(ctx context.Context, id string) (*RIExchangeDirectRow, error)
}

var _ RIExchangeDirectListStore = (*PostgresStore)(nil)
