package config

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The list's page size: the default, and the most a caller may ask for.
const (
	RIExchangeDirectListDefaultLimit = 50
	RIExchangeDirectListMaxLimit     = 200
)

const riExchangeDirectRowColumns = `
	id::text, status, cloud_account_id, COALESCE(region, ''), source_ids, targets::text,
	max_payment_due::text, max_payment_currency, COALESCE(error, ''), executed_by,
	created_at, completed_at, resolved_at`

func scanRIExchangeDirectRow(row pgx.Row) (RIExchangeDirectRow, error) {
	var r RIExchangeDirectRow
	var targets string
	err := row.Scan(&r.ID, &r.Status, &r.CloudAccountID, &r.Region, &r.SourceIDs, &targets,
		&r.MaxPaymentDue, &r.MaxPaymentCurrency, &r.Error, &r.ExecutedBy,
		&r.CreatedAt, &r.CompletedAt, &r.ResolvedAt)
	r.Targets = []byte(targets)
	return r, err
}

// ListUnresolvedRIExchangeDirectExecutions returns one keyset page of
// unresolved AWS direct rows (submitted or unknown), oldest first, restricted
// to the filter's allowed (account, region) pairs. The pairs travel as two
// arrays so no caller input reaches the SQL text, a NULL account matches only
// a NULL pair entry, and a NULL region matches the empty region.
func (s *PostgresStore) ListUnresolvedRIExchangeDirectExecutions(ctx context.Context, f RIExchangeDirectListFilter) (*RIExchangeDirectListPage, error) {
	limit, statuses, err := validateRIExchangeDirectListFilter(f)
	if err != nil {
		return nil, err
	}
	page := &RIExchangeDirectListPage{}
	if len(f.Allowed) == 0 {
		return page, nil
	}
	accounts := make([]*string, len(f.Allowed))
	regions := make([]string, len(f.Allowed))
	for i, p := range f.Allowed {
		accounts[i], regions[i] = p.CloudAccountID, p.Region
	}
	riID, region := optionalListText(normaliseRIExchangeSourceIDs([]string{f.RIID})), optionalListText([]string{f.Region})
	var afterAt *time.Time
	var afterID *string
	if f.After != nil {
		afterAt, afterID = &f.After.CreatedAt, &f.After.ID
	}

	rows, err := s.db.Query(ctx, `
		SELECT`+riExchangeDirectRowColumns+`
		FROM ri_exchange_direct_executions t
		WHERE provider = 'aws' AND resolved_at IS NULL
		  AND status = ANY($1::text[])
		  AND EXISTS (SELECT 1 FROM unnest($2::text[], $3::text[]) a(acc, reg)
		              WHERE t.cloud_account_id IS NOT DISTINCT FROM a.acc AND COALESCE(t.region, '') = a.reg)
		  AND ($4::text IS NULL OR $4 = ANY(ARRAY(SELECT lower(btrim(x, E' \t\n\r\f\x0B')) FROM unnest(source_ids) x)))
		  AND ($5::text IS NULL OR region = $5)
		  AND ($6::timestamptz IS NULL OR (created_at, id) > ($6, $7::uuid))
		ORDER BY created_at, id
		LIMIT $8`,
		statuses, accounts, regions, riID, region, afterAt, afterID, limit+1)
	if err != nil {
		return nil, fmt.Errorf("failed to list unresolved ri exchange direct executions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRIExchangeDirectRow(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan ri exchange direct execution: %w", err)
		}
		page.Rows = append(page.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list unresolved ri exchange direct executions: %w", err)
	}
	if len(page.Rows) > limit {
		page.Rows = page.Rows[:limit]
		last := page.Rows[limit-1]
		page.Next = &RIExchangeDirectCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

// validateRIExchangeDirectListFilter applies the limit default and cap and
// checks the status filter.
func validateRIExchangeDirectListFilter(f RIExchangeDirectListFilter) (limit int, statuses []string, err error) {
	limit = f.Limit
	if limit <= 0 {
		limit = RIExchangeDirectListDefaultLimit
	}
	if limit > RIExchangeDirectListMaxLimit {
		return 0, nil, fmt.Errorf("%w: limit must be at most %d", ErrRIExchangeResolveInvalid, RIExchangeDirectListMaxLimit)
	}
	statuses = f.Statuses
	if len(statuses) == 0 {
		statuses = []string{RIExchangeDirectStatusSubmitted, RIExchangeDirectStatusUnknown}
	}
	for _, st := range statuses {
		if st != RIExchangeDirectStatusSubmitted && st != RIExchangeDirectStatusUnknown {
			return 0, nil, fmt.Errorf("%w: status must be %q or %q", ErrRIExchangeResolveInvalid, RIExchangeDirectStatusSubmitted, RIExchangeDirectStatusUnknown)
		}
	}
	return limit, statuses, nil
}

// optionalListText is the first non-empty value as a nullable SQL argument.
func optionalListText(vals []string) *string {
	if len(vals) == 0 || vals[0] == "" {
		return nil
	}
	return &vals[0]
}
