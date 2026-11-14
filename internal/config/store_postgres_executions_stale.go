package config

import (
	"context"
	"fmt"
	"time"
)

// GetStaleApprovedExecutions returns executions stuck in the "approved" status
// whose last update is older than olderThan. These are executions that were
// flipped to "approved" by ApproveAndExecute but whose synchronous purchase run
// never finalized (Lambda timeout, cold-start eviction, panic) — issue #632.
// updated_at is stamped to NOW() at the moment of the approved transition (see
// TransitionExecutionStatus) and is not touched again unless the run finalizes,
// so it is the age of the strand. Mirrors GetStaleProcessingExchanges.
func (s *PostgresStore) GetStaleApprovedExecutions(ctx context.Context, olderThan time.Duration) ([]PurchaseExecution, error) {
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
		WHERE status = 'approved' AND updated_at < NOW() - $1::interval
	`
	return s.queryExecutions(ctx, query, fmt.Sprintf("%d seconds", int(olderThan.Seconds())))
}

// ListStuckExecutions returns purchase executions whose Status is any of the
// supplied values and whose updated_at is older than the given duration. Used
// by the reaper sweep (issue #678) to find executions stuck in
// approved/running long enough that the synchronous executor has clearly
// failed without flipping the row to a terminal state.
//
// Returns rows oldest-first (ORDER BY updated_at ASC) so the longest-stuck
// rows are processed first within a single sweep, capped at MaxListLimit so
// an unbounded backlog doesn't blow up the Lambda's memory budget. The reaper
// invokes the sweep periodically; a backlog larger than MaxListLimit just
// gets drained across successive invocations.
//
// olderThan must be > 0; a zero/negative value would invert the WHERE clause
// into "updated_at < NOW() + |olderThan|" and reap fresh rows. Defense-in-
// depth: the caller (ParseReapAfterFromEnv) also rejects non-positive env
// values.
//
// olderThan is passed as a Postgres interval (seconds) so the comparison
// happens server-side against NOW() — keeping the cutoff in the DB clock
// avoids any drift between the API process and the database.
func (s *PostgresStore) ListStuckExecutions(ctx context.Context, statuses []string, olderThan time.Duration) ([]PurchaseExecution, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	if olderThan <= 0 {
		return nil, fmt.Errorf("ListStuckExecutions: olderThan must be > 0, got %s", olderThan)
	}
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
		WHERE status = ANY($1)
		  AND updated_at < NOW() - $2::interval
		ORDER BY updated_at ASC
		LIMIT $3
	`
	intervalArg := fmt.Sprintf("%d seconds", int(olderThan.Seconds()))
	return s.queryExecutions(ctx, query, statuses, intervalArg, MaxListLimit)
}

// GetPendingExecutions retrieves all pending purchase executions.
func (s *PostgresStore) GetPendingExecutions(ctx context.Context) ([]PurchaseExecution, error) {
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
		WHERE status IN ('pending', 'notified')
		  AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY scheduled_date ASC
		LIMIT 1000
	`

	return s.queryExecutions(ctx, query)
}
