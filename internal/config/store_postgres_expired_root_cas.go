package config

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// readoptExpiredRootQuery re-arms the root: pending, no recommendations, no
// token, the given date, marker set. The creator columns and source are never
// written, so 4-eyes identity is unchanged. $4 execution ID, $5 new date.
const readoptExpiredRootQuery = `
	UPDATE purchase_executions e
	   SET status = 'pending', scheduled_date = $5, recommendations = '[]'::jsonb,
	       total_upfront_cost = 0, estimated_savings = 0,
	       approval_token_hash = NULL, approval_token_expires_at = NULL,
	       notification_sent = NULL, approval_readopted_at = NOW(), updated_at = NOW()
	 WHERE e.execution_id = $4 AND ` + readoptRootPredicate + `
	-- an expired root was never canceled, so the legacy column needs no COALESCE
	RETURNING plan_id, execution_id, status, step_number, scheduled_date,
	          notification_sent, approval_token_hash, recommendations,
	          total_upfront_cost, estimated_savings, completed_at, error, expires_at,
	          cloud_account_id, source, approved_by, canceled_by, capacity_percent,
	          created_by_user_id, retry_execution_id, retry_attempt_n,
	          approval_token_expires_at,
	          executed_by_user_id, executed_at, pre_approval_skip_reason,
	          idempotency_key, scheduled_execution_at, no_submission, created_by_system, first_dispatch_at`

// ReadoptExpiredRootTx re-arms one expired root inside the caller's
// transaction, once. It is the row-level backstop: the plan gates live in the
// caller. It checks the schema itself and answers ErrReadoptNotReady before
// migration 164, and ErrExecutionNotInExpectedStatus when the row no longer
// qualifies (lost race, already re-armed, too old, shape changed).
func (s *PostgresStore) ReadoptExpiredRootTx(ctx context.Context, tx pgx.Tx, executionID string, scheduledDate, now time.Time, requireDifferentApprover bool) (*PurchaseExecution, error) {
	ready, err := readoptSchemaReady(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to check the re-adoption schema: %w", err)
	}
	if !ready {
		return nil, ErrReadoptNotReady
	}
	records, err := queryExecutionsOn(ctx, tx, readoptExpiredRootQuery,
		now, ReadoptMaxExpiredAge.Seconds(), requireDifferentApprover, executionID, scheduledDate)
	if err != nil {
		return nil, fmt.Errorf("failed to re-arm expired execution %s: %w", executionID, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: execution %s is not a re-armable expired root", ErrExecutionNotInExpectedStatus, executionID)
	}
	return &records[0], nil
}
