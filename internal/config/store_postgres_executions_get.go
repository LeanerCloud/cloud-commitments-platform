package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// GetExecutionByID retrieves a purchase execution by execution ID.
// Returns an error wrapping ErrNotFound when no row matches executionID so
// callers can cleanly distinguish "not found" (errors.Is(err, ErrNotFound))
// from a real DB failure (any other non-nil error). A nil error guarantees
// a non-nil execution (fail-loud contract; issues #976, #1339).
func (s *PostgresStore) GetExecutionByID(ctx context.Context, executionID string) (*PurchaseExecution, error) {
	return getExecutionByIDOn(ctx, s.db, executionID)
}

// getExecutionByIDOn is GetExecutionByID on q, the pool or a transaction.
func getExecutionByIDOn(ctx context.Context, q executionQuerier, executionID string) (*PurchaseExecution, error) {
	query := `
		SELECT plan_id, execution_id, status, step_number, scheduled_date,
		       notification_sent, approval_token_hash, recommendations,
		       total_upfront_cost, estimated_savings, completed_at, error, expires_at,
		       cloud_account_id, source, approved_by, canceled_by, capacity_percent,
		       created_by_user_id, retry_execution_id, retry_attempt_n,
		       approval_token_expires_at,
		       executed_by_user_id, executed_at, pre_approval_skip_reason,
		       idempotency_key, scheduled_execution_at, no_submission, created_by_system, first_dispatch_at
		FROM purchase_executions
		WHERE execution_id = $1
	`

	executions, err := queryExecutionsOn(ctx, q, query, executionID)
	if isInvalidTextRepresentation(err) {
		// execution_id is a UUID column, and Postgres rejects an ID it cannot
		// cast instead of matching no row. No execution can have such an ID,
		// so it is not found. Callers such as the purchase revoke route look up
		// IDs that may be an Azure ARM path and fall through on ErrNotFound.
		return nil, fmt.Errorf("%w: execution %s", ErrNotFound, executionID)
	}
	if err != nil {
		return nil, err
	}

	if len(executions) == 0 {
		return nil, fmt.Errorf("%w: execution %s", ErrNotFound, executionID)
	}

	return &executions[0], nil
}

// pgInvalidTextRepresentation is the SQLSTATE Postgres raises when a text
// value cannot be cast to the column type, for example a non-UUID string
// compared with a uuid column.
const pgInvalidTextRepresentation = "22P02"

// isInvalidTextRepresentation reports whether err is Postgres refusing a text value
// for a typed column (SQLSTATE 22P02).
func isInvalidTextRepresentation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgInvalidTextRepresentation
}

// GetExecutionByPlanAndDate retrieves execution for a specific plan and date.
func (s *PostgresStore) GetExecutionByPlanAndDate(ctx context.Context, planID string, scheduledDate time.Time) (*PurchaseExecution, error) {
	query := `
		SELECT plan_id, execution_id, status, step_number, scheduled_date,
		       notification_sent, approval_token_hash, recommendations,
		       total_upfront_cost, estimated_savings, completed_at, error, expires_at,
		       cloud_account_id, source, approved_by, canceled_by, capacity_percent,
		       created_by_user_id, retry_execution_id, retry_attempt_n,
		       approval_token_expires_at,
		       executed_by_user_id, executed_at, pre_approval_skip_reason,
		       idempotency_key, scheduled_execution_at, no_submission, created_by_system, first_dispatch_at
		FROM purchase_executions
		WHERE plan_id = $1 AND scheduled_date = $2
	`

	executions, err := s.queryExecutions(ctx, query, planID, scheduledDate)
	if err != nil {
		return nil, err
	}

	if len(executions) == 0 {
		return nil, fmt.Errorf("%w: plan %s at %v", ErrNotFound, planID, scheduledDate)
	}

	return &executions[0], nil
}

// GetUserEmailByID resolves the email address of the auth user identified by
// userID. The `users` table belongs to internal/auth's schema, but internal/
// auth already imports internal/config (service_mfa.go), so internal/config
// cannot import internal/auth back without a cycle. This method queries the
// shared database directly by table/column name instead, returning a plain
// string so no auth.User type crosses the package boundary. Returns ("", nil)
// when userID does not resolve to a row -- callers must treat that as
// "identity unresolved," not as a legitimately blank email.
func (s *PostgresStore) GetUserEmailByID(ctx context.Context, userID string) (string, error) {
	var email string
	err := s.db.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("failed to get user email: %w", err)
	}
	return email, nil
}
