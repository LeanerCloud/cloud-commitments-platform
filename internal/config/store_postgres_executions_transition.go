package config

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// RescheduleAwaitingExecution is a conditional single-column update: the
// status predicate makes a lost race a benign no-op instead of an overwrite.
func (s *PostgresStore) RescheduleAwaitingExecution(ctx context.Context, executionID string, scheduledDate time.Time) (bool, error) {
	var planID *string
	err := s.db.QueryRow(ctx, `SELECT plan_id::text FROM purchase_executions WHERE execution_id = $1`, executionID).Scan(&planID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reschedule execution %s: %w", executionID, err)
	}
	updated := false
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		if planID != nil {
			plan, lockErr := s.LockPurchasePlanTx(ctx, tx, *planID)
			if lockErr != nil {
				return lockErr
			}
			if plan != nil && plan.RampSchedule.DatesManaged {
				return fmt.Errorf("%w: reschedule of execution %s", ErrEditorReconciliationRequired, executionID)
			}
		}
		tag, execErr := tx.Exec(ctx, `
			UPDATE purchase_executions SET scheduled_date = $2, updated_at = NOW()
			WHERE execution_id = $1 AND status IN ('pending', 'notified')`, executionID, scheduledDate)
		updated = execErr == nil && tag.RowsAffected() == 1
		return execErr
	})
	if err != nil {
		return false, fmt.Errorf("reschedule execution %s: %w", executionID, err)
	}
	return updated, nil
}

func (s *PostgresStore) transitionExecutionStatus(ctx context.Context, q executionQuerier, executionID string, fromStatuses []string, toStatus string, actor *string, sawEmpty *bool, tokenHash *string) (*PurchaseExecution, error) {
	var cancelActor *string
	if toStatus == StatusCanceled {
		cancelActor = actor
	}
	query := `
		UPDATE purchase_executions
		SET status = $2, updated_at = NOW(),
		    transitioned_by = $4, transitioned_at = NOW(),
		    canceled_by = COALESCE((SELECT email FROM users WHERE id = $5), canceled_by)
		WHERE execution_id = $1 AND status = ANY($3)
		  AND ($6::boolean IS NULL OR (recommendations IN ('[]'::jsonb, 'null'::jsonb)) = $6)
		  AND ($7::text IS NULL OR (approval_token_hash = $7
		       AND (approval_token_expires_at IS NULL OR approval_token_expires_at > NOW())))
		RETURNING plan_id, execution_id, status, step_number, scheduled_date,
		          notification_sent, approval_token_hash, recommendations,
		          total_upfront_cost, estimated_savings, completed_at, error, expires_at,
		          cloud_account_id, source, approved_by, canceled_by, capacity_percent,
		          created_by_user_id, retry_execution_id, retry_attempt_n,
		          approval_token_expires_at,
		          executed_by_user_id, executed_at, pre_approval_skip_reason,
		          idempotency_key, scheduled_execution_at, no_submission, created_by_system, first_dispatch_at
	`

	records, err := queryExecutionsOn(ctx, q, query, executionID, toStatus, fromStatuses, actor, cancelActor, sawEmpty, tokenHash)
	if err != nil {
		return nil, err
	}

	if len(records) == 0 {
		return nil, transitionRejection(ctx, q, executionID, fromStatuses, toStatus, tokenHash != nil)
	}

	return &records[0], nil
}

// transitionRejection explains a zero-row transitionExecutionStatus by probing
// the row through q (the caller's transaction for the Tx variant): gone
// (ErrNotFound), a hard DB error, a token pin lost on a row still in an
// allowed status (ErrApprovalTokenChanged), or a status CAS lost
// (ErrExecutionNotInExpectedStatus). Extracted to keep the caller under the
// gocyclo threshold.
func transitionRejection(ctx context.Context, q executionQuerier, executionID string, fromStatuses []string, toStatus string, tokenPinned bool) error {
	existing, existErr := getExecutionByIDOn(ctx, q, executionID)
	if errors.Is(existErr, ErrNotFound) {
		// Wrap ErrNotFound so callers (e.g. the purchase reaper) can
		// use errors.Is to distinguish "row vanished mid-flight" — a
		// legitimate CAS race-loss — from a hard DB error.
		return fmt.Errorf("%w: execution %s", ErrNotFound, executionID)
	}
	if existErr != nil {
		// A hard DB error during the probe must NOT read as a benign
		// race-loss: propagate it so callers see the outage.
		return fmt.Errorf("transition %s: probe after zero-row CAS failed: %w", executionID, existErr)
	}
	// The status still allowed the transition, so the token pin is what
	// rejected it: the stored token was rotated or expired (issue #590).
	if tokenPinned && slices.Contains(fromStatuses, existing.Status) {
		return fmt.Errorf("%w: execution %s", ErrApprovalTokenChanged, executionID)
	}
	// Wrap ErrExecutionNotInExpectedStatus so callers can use
	// errors.Is to recognize CAS rejection (status changed between
	// SELECT and UPDATE) as race-lost rather than a real error.
	return fmt.Errorf("%w: execution %s cannot transition from %q to %q", ErrExecutionNotInExpectedStatus, executionID, existing.Status, toStatus)
}

// SetCancelledBy stamps canceled_by for a single execution without touching
// any other column. This avoids the full-row overwrite that a
// SavePurchaseExecution follow-up would perform, eliminating the lost-update
// window between TransitionExecutionStatus and the attribution write
// (Finding #5).
func (s *PostgresStore) SetCancelledBy(ctx context.Context, executionID, cancelledBy string) error {
	q := `UPDATE purchase_executions SET canceled_by = $2, updated_at = NOW()
	       WHERE execution_id = $1`
	_, err := s.db.Exec(ctx, q, executionID, cancelledBy)
	return err
}
