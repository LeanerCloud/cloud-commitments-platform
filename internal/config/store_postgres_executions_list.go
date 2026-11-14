package config

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// GetExecutionsByStatuses returns executions whose Status is any of the
// supplied values, newest-first, capped at `limit`. Used by the History
// handler to merge pending/failed/expired rows alongside completed purchases
// without changing the narrower GetPendingExecutions contract the scheduler
// depends on.
//
// Pending rows dated in the future are the one exception to newest-first
// (issue #680): plan creation writes every ramp step up front as a pending
// row, up to 52 per plan, so about 20 plans fill MaxListLimit with steps
// months ahead and a plain scheduled_date DESC would cut off a pending
// approval due today. Those future pending steps sort after every other row,
// and among themselves soonest first, so the cap drops the farthest steps.
// No other status is affected: a notified, scheduled or in-flight row is
// actionable whatever its date, and terminal statuses never match the
// pending predicate, so their order is unchanged.
//
// Executions with status 'completed' and an empty error are excluded: they are
// already represented by their purchase_history rows, so returning them would
// only consume the cap (the History handler skips them anyway).
func (s *PostgresStore) GetExecutionsByStatuses(ctx context.Context, statuses []string, limit int) ([]PurchaseExecution, error) {
	return s.GetExecutionsByStatusesFiltered(ctx, ExecutionsByStatusesQuery{Statuses: statuses, Limit: limit})
}

// GetExecutionsByStatusesFiltered is GetExecutionsByStatuses with the History
// filters and account scope in q applied in SQL, before the limit (issue
// #632). Order and the clean-completed exclusion are GetExecutionsByStatuses'.
func (s *PostgresStore) GetExecutionsByStatusesFiltered(ctx context.Context, q ExecutionsByStatusesQuery) ([]PurchaseExecution, error) {
	if len(q.Statuses) == 0 {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	limit = min(limit, MaxListLimit)
	conds, args := executionsByStatusesWhere(&q)
	args = append(args, limit)
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
		WHERE ` + strings.Join(conds, "\n\t\t  AND ") + `
		ORDER BY (status = 'pending' AND scheduled_date > now()) ASC,
		         CASE WHEN status = 'pending' AND scheduled_date > now() THEN scheduled_date END ASC,
		         scheduled_date DESC
		LIMIT $` + fmt.Sprint(len(args))
	return s.queryExecutions(ctx, query, args...)
}

// CountExecutionsByPlanAndStatus returns, keyed by plan ID, the exact number
// of executions in each of the supplied statuses that last changed state at
// or after `since`.
//
// Aggregated with GROUP BY rather than counted from a GetExecutionsByStatuses
// page: that method's DESC + LIMIT truncation would silently understate any
// plan whose executions fall outside the newest `limit` rows, which for the
// plan health score means an unhealthy plan quietly renders as healthy once
// newer rows from other plans push its failures out of the window. The result
// set here is bounded by plans x statuses, not by execution volume, so it
// needs no limit of its own.
//
// The window is on updated_at, NOT scheduled_date, because for the terminal
// statuses this method exists to count those are different dates pointing in
// opposite directions. A plan's executions are created up front for the whole
// ramp, so a pending row carries a scheduled_date months in the FUTURE;
// canceling it (CancelExecutionAtomic) leaves that future date untouched.
// Windowing on scheduled_date would therefore count a purchase cancelled
// today under a date next year, and answer "how many rows are scheduled
// recently-or-later" instead of "how many changed state recently".
// updated_at is stamped by CancelExecutionAtomic and
// TransitionExecutionStatus, and backstopped by the
// update_purchase_executions_updated_at trigger, so it is when the row
// actually entered the status being counted.
//
// plan_id has been nullable since migration 000033 (direct-execute purchases
// from the Recommendations page have no originating plan, and deleting a plan
// SET NULLs its executions). Those rows belong to no plan and are excluded in
// SQL; the scan still goes through sql.NullString so a NULL can never turn a
// per-plan count into a scan error that blanks the score for every plan.
func (s *PostgresStore) CountExecutionsByPlanAndStatus(ctx context.Context, statuses []string, since time.Time) (map[string]ExecutionStatusCounts, error) {
	counts := make(map[string]ExecutionStatusCounts)
	if len(statuses) == 0 {
		return counts, nil
	}

	rows, err := s.db.Query(ctx, `
		SELECT plan_id, status, COUNT(*)
		FROM purchase_executions
		WHERE status = ANY($1)
		  AND plan_id IS NOT NULL
		  AND updated_at >= $2
		GROUP BY plan_id, status
	`, statuses, since)
	if err != nil {
		return nil, fmt.Errorf("failed to count executions by plan and status: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var planID sql.NullString
		var status string
		var n int
		if scanErr := rows.Scan(&planID, &status, &n); scanErr != nil {
			return nil, fmt.Errorf("failed to scan execution status count: %w", scanErr)
		}
		if !planID.Valid {
			continue
		}
		if counts[planID.String] == nil {
			counts[planID.String] = ExecutionStatusCounts{}
		}
		counts[planID.String][status] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate execution status counts: %w", err)
	}
	return counts, nil
}

// GetPlannedExecutions returns executions whose Status is any of the supplied
// values, ordered by scheduled_date ASC (soonest first), capped at `limit`.
// Used by the Planned Purchases handler where the user expects to act on
// imminent rows first.
//
// Distinct from GetExecutionsByStatuses (DESC + LIMIT for History's
// newest-first semantics): when total rows exceed `limit`, a DESC truncation
// drops the soonest rows, exactly the ones this list must surface. Sorting
// the already-truncated subset in-memory cannot recover them.
//
// Secondary sort by id ASC keeps ordering stable when multiple rows share a
// scheduled_date. NULLS LAST is defensive: the schema makes scheduled_date
// NOT NULL today, but the clause guards against a future relaxation silently
// hiding rows at the top of the list.
func (s *PostgresStore) GetPlannedExecutions(ctx context.Context, statuses []string, limit int) ([]PurchaseExecution, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
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
		ORDER BY scheduled_date ASC NULLS LAST, id ASC
		LIMIT $2
	`
	return s.queryExecutions(ctx, query, statuses, limit)
}
