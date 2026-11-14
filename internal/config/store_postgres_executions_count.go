package config

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// CountPendingExecutionsForAccount returns the number of pending/notified
// purchase executions still referencing this cloud account. The deleteAccount
// handler calls this before issuing DELETE FROM cloud_accounts so it can
// short-circuit with a 409 instead of letting migration 000053's ON DELETE
// RESTRICT bubble up as an opaque FK-violation error. See issue #606.
func (s *PostgresStore) CountPendingExecutionsForAccount(ctx context.Context, accountID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM purchase_executions
		WHERE cloud_account_id = $1
		  AND status IN ('pending', 'notified')
	`, accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count pending executions for account: %w", err)
	}
	return n, nil
}

// CountLiveExecutionsForAccount returns the number of purchase executions
// referencing this cloud account whose status is not in
// TerminalExecutionStatuses: rows an executor, scheduler or reaper may still
// act on, which need the account to exist. DeleteCloudAccount detaches every
// other row, so these are exactly the rows that block the delete (issue #265).
// It includes the pending/notified rows CountPendingExecutionsForAccount
// counts.
func (s *PostgresStore) CountLiveExecutionsForAccount(ctx context.Context, accountID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM purchase_executions
		WHERE cloud_account_id = $1
		  AND status <> ALL($2)
	`, accountID, TerminalExecutionStatuses).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count live executions for account: %w", err)
	}
	return n, nil
}

// ListPendingExecutionIDsForAccount returns the execution IDs that the
// frontend's Cancel-All-Then-Delete flow needs to POST cancel for. Capped
// at 1000 rows — a single account with more pending executions than that
// is an unusual operator-cleanup task rather than a button-click flow.
func (s *PostgresStore) ListPendingExecutionIDsForAccount(ctx context.Context, accountID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT execution_id FROM purchase_executions
		WHERE cloud_account_id = $1
		  AND status IN ('pending', 'notified')
		ORDER BY scheduled_date ASC
		LIMIT 1000
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pending execution ids for account: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan pending execution id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate pending execution ids: %w", err)
	}
	return ids, nil
}

// executionQuerier is the query method shared by the pool (dbConn) and a
// pgx.Tx, so the execution read and transition helpers can run on either.
type executionQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// queryExecutions is a helper to query and scan purchase executions.
func (s *PostgresStore) queryExecutions(ctx context.Context, query string, args ...any) ([]PurchaseExecution, error) {
	return queryExecutionsOn(ctx, s.db, query, args...)
}

// queryExecutionsOn is queryExecutions on q, the pool or a transaction.
func queryExecutionsOn(ctx context.Context, q executionQuerier, query string, args ...any) ([]PurchaseExecution, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query executions: %w", err)
	}
	defer rows.Close()
	return scanExecutionRows(rows)
}
