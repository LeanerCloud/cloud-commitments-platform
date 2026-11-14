package config

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ==========================================
// PURCHASE EXECUTIONS
// ==========================================

// SavePurchaseExecution saves a purchase execution record.
func (s *PostgresStore) SavePurchaseExecution(ctx context.Context, execution *PurchaseExecution) error {
	// Generate the execution ID before we attempt to open a tx so
	// pre-existing tests (which passed a nil DB and relied on ID
	// generation as a side effect) still work as-is.
	if execution.ExecutionID == "" {
		execution.ExecutionID = uuid.New().String()
	}
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		return s.SavePurchaseExecutionTx(ctx, tx, execution)
	})
}

// SavePurchaseExecutionTx is the tx-accepting variant of
// SavePurchaseExecution. Used from handlers that need to bundle the
// execution insert with other writes (e.g. purchase_suppressions rows)
// in a single atomic transaction.
func (s *PostgresStore) SavePurchaseExecutionTx(ctx context.Context, tx pgx.Tx, execution *PurchaseExecution) error {
	// Generate execution ID if not provided
	if execution.ExecutionID == "" {
		execution.ExecutionID = uuid.New().String()
	}

	// Stamp a stable idempotency lineage key on first creation (issue #1012).
	// Unlike ExecutionID this is generated once and then copied verbatim onto
	// Retry successors / multi-account fan-out rows by the callers, so the
	// derived provider token survives a re-drive. INSERT-only below (omitted
	// from the ON CONFLICT DO UPDATE SET), so an upsert of an existing row
	// never overwrites the key already persisted at creation.
	if execution.IdempotencyKey == "" {
		execution.IdempotencyKey = uuid.New().String()
	}

	// Marshal recommendations to JSONB
	recommendationsJSON, err := json.Marshal(execution.Recommendations)
	if err != nil {
		return fmt.Errorf("failed to marshal recommendations: %w", err)
	}

	// Capacity % defaults to 100 (= "bought the full recommendation")
	// for legacy / scheduler paths that don't set the field.
	capacityPercent := execution.CapacityPercent
	if capacityPercent <= 0 {
		capacityPercent = 100
	}

	// canceled_by keeps an actor already on the row: a save from a copy read
	// before a concurrent cancel must not erase that cancel's attribution.
	query := `
		INSERT INTO purchase_executions (
			plan_id, execution_id, status, step_number, scheduled_date,
			notification_sent, approval_token_hash, recommendations,
			total_upfront_cost, estimated_savings, completed_at, error, expires_at,
			cloud_account_id, source, approved_by, canceled_by, capacity_percent,
			created_by_user_id, retry_execution_id, retry_attempt_n,
			approval_token_expires_at,
			executed_by_user_id, executed_at, pre_approval_skip_reason,
			idempotency_key, scheduled_execution_at, no_submission, created_by_system, first_dispatch_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30)
		ON CONFLICT (execution_id) DO UPDATE SET
			status = $3,
			notification_sent = $6,
			approval_token_hash = $7,
			recommendations = $8,
			total_upfront_cost = $9,
			estimated_savings = $10,
			completed_at = $11,
			error = $12,
			expires_at = $13,
			cloud_account_id = $14,
			source = $15,
			approved_by = $16,
			canceled_by = COALESCE(purchase_executions.canceled_by, $17),
			capacity_percent = $18,
			approval_token_expires_at = $22,
			executed_by_user_id = $23,
			executed_at = $24,
			pre_approval_skip_reason = $25,
			scheduled_execution_at = $27,
			no_submission = CASE WHEN $3 = 'failed'
				THEN purchase_executions.no_submission OR $28
				ELSE false END,
			updated_at = NOW()
	`

	// Direct-execute purchases (from the Recommendations page, no plan)
	// arrive with an empty PlanID. The column is UUID — pass nil rather
	// than the empty string so PostgreSQL stores NULL instead of trying
	// to parse "" as a UUID (which crashed the handler with a generic
	// 500). Migration 000033 relaxed the NOT NULL so this is safe.
	var planIDArg any
	if execution.PlanID != "" {
		planIDArg = execution.PlanID
	}

	// created_by_user_id, created_by_system, first_dispatch_at and retry_attempt_n are INSERT-only: the original
	// creator and the retry-chain position must not be rewritten by an
	// ON CONFLICT update (e.g. the scheduler upserting status transitions).
	// They are omitted from the DO UPDATE SET clause above.
	//
	// retry_execution_id is also omitted from the DO UPDATE SET clause
	// (issue #220 CR follow-up): any other caller that upserts a stale
	// in-memory copy of a failed row (e.g. the reaper) through this same
	// path must not be able to clear a link LinkRetryExecutionAtomic
	// already wrote, which would defeat the already-retried guard and
	// let the row be retried a second time. The retry handler links the
	// original failed row to its successor exclusively through
	// LinkRetryExecutionAtomic's targeted, conditional UPDATE.
	_, err = tx.Exec(ctx, query,
		planIDArg,
		execution.ExecutionID,
		execution.Status,
		execution.StepNumber,
		execution.ScheduledDate,
		execution.NotificationSent,
		execution.ApprovalToken,
		recommendationsJSON,
		execution.TotalUpfrontCost,
		execution.EstimatedSavings,
		execution.CompletedAt,
		execution.Error,
		timeFromTTL(execution.TTL),
		execution.CloudAccountID,
		execution.Source,
		execution.ApprovedBy,
		execution.CancelledBy,
		capacityPercent,
		execution.CreatedByUserID,
		execution.RetryExecutionID,
		execution.RetryAttemptN,
		execution.ApprovalTokenExpiresAt,
		execution.ExecutedByUserID,
		execution.ExecutedAt,
		execution.PreApprovalSkipReason,
		execution.IdempotencyKey,
		execution.ScheduledExecutionAt,
		execution.NoSubmission,
		execution.CreatedBySystem,
		execution.FirstDispatchAt,
	)

	if err != nil {
		return fmt.Errorf("failed to save purchase execution: %w", err)
	}

	return nil
}
