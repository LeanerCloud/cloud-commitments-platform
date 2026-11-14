package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// applyNullTimesToExecution sets the nullable timestamp fields on exec from the
// sql.NullTime wrappers, pulled out of scanExecutionRows to keep that function
// under the cyclomatic limit.
func applyNullTimesToExecution(exec *PurchaseExecution, notifSent, completedAt, expiresAt, tokenExpiresAt, executedAt, scheduledExecutionAt sql.NullTime) {
	if notifSent.Valid {
		exec.NotificationSent = &notifSent.Time
	}
	if completedAt.Valid {
		exec.CompletedAt = &completedAt.Time
	}
	if expiresAt.Valid {
		exec.TTL = ttlFromTime(expiresAt.Time)
	}
	if tokenExpiresAt.Valid {
		exec.ApprovalTokenExpiresAt = &tokenExpiresAt.Time
	}
	if executedAt.Valid {
		exec.ExecutedAt = &executedAt.Time
	}
	if scheduledExecutionAt.Valid {
		exec.ScheduledExecutionAt = &scheduledExecutionAt.Time
	}
}

// scanExecutionRows scans a pgx.Rows cursor into a slice of PurchaseExecution.
// It is used by queryExecutions (pool query) and ListRecentSubmitsTx (tx
// query) so the scan logic lives in one place.
func scanExecutionRows(rows pgx.Rows) ([]PurchaseExecution, error) {
	executions := make([]PurchaseExecution, 0)
	for rows.Next() {
		var exec PurchaseExecution
		var recommendationsJSON []byte
		var notifSent, completedAt, expiresAt, tokenExpiresAt, executedAt, scheduledExecutionAt sql.NullTime
		// plan_id is nullable since migration 000033 (direct-execute
		// rows from the Recommendations page have no originating plan).
		var planID sql.NullString
		// idempotency_key is NULL on rows created before migration 000066;
		// leave exec.IdempotencyKey "" for those so the derivation falls back
		// to ExecutionID (issue #1012).
		var idempotencyKey sql.NullString
		// approval_token_hash is NULL on rows migration 000100 found with no
		// raw token (every canceled row among them).
		var approvalTokenHash sql.NullString
		// error is nullable (migration 000001); one NULL row must not fail the
		// whole list scan (issue #1465). exec.Error stays "" for those.
		var errText sql.NullString

		err := rows.Scan(
			&planID,
			&exec.ExecutionID,
			&exec.Status,
			&exec.StepNumber,
			&exec.ScheduledDate,
			&notifSent,
			&approvalTokenHash,
			&recommendationsJSON,
			&exec.TotalUpfrontCost,
			&exec.EstimatedSavings,
			&completedAt,
			&errText,
			&expiresAt,
			&exec.CloudAccountID,
			&exec.Source,
			&exec.ApprovedBy,
			&exec.CancelledBy,
			&exec.CapacityPercent,
			&exec.CreatedByUserID,
			&exec.RetryExecutionID,
			&exec.RetryAttemptN,
			&tokenExpiresAt,
			&exec.ExecutedByUserID,
			&executedAt,
			&exec.PreApprovalSkipReason,
			&idempotencyKey,
			&scheduledExecutionAt,
			&exec.NoSubmission,
			&exec.CreatedBySystem,
			&exec.FirstDispatchAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan execution: %w", err)
		}

		if planID.Valid {
			exec.PlanID = planID.String
		}
		if idempotencyKey.Valid {
			exec.IdempotencyKey = idempotencyKey.String
		}
		exec.ApprovalToken = approvalTokenHash.String
		exec.Error = errText.String

		// Unmarshal recommendations
		if err := json.Unmarshal(recommendationsJSON, &exec.Recommendations); err != nil {
			return nil, fmt.Errorf("failed to unmarshal recommendations: %w", err)
		}

		applyNullTimesToExecution(&exec, notifSent, completedAt, expiresAt, tokenExpiresAt, executedAt, scheduledExecutionAt)

		executions = append(executions, exec)
	}

	return executions, rows.Err()
}

// GetScheduledExecutionsDue returns purchase_executions with status='scheduled'
// whose scheduled_execution_at has elapsed (scheduled_execution_at <= NOW()).
// Used by the Gmail-style pre-fire delay scheduler tick (issue #291 wave-2).
// Results are ordered oldest-due-first so the scheduler fires them in FIFO order.
// Capped at MaxListLimit per sweep to bound the per-tick blast radius.
func (s *PostgresStore) GetScheduledExecutionsDue(ctx context.Context) ([]PurchaseExecution, error) {
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
		WHERE status = 'scheduled'
		  AND scheduled_execution_at IS NOT NULL
		  AND scheduled_execution_at <= NOW()
		ORDER BY scheduled_execution_at ASC
		LIMIT $1
	`
	rows, err := s.db.Query(ctx, query, MaxListLimit)
	if err != nil {
		return nil, fmt.Errorf("GetScheduledExecutionsDue: query failed: %w", err)
	}
	defer rows.Close()
	return scanExecutionRows(rows)
}
