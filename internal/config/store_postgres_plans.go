package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ==========================================
// PURCHASE PLANS
// ==========================================

// CreatePurchasePlan creates a new purchase plan.
func (s *PostgresStore) CreatePurchasePlan(ctx context.Context, plan *PurchasePlan) error {
	return s.createPurchasePlan(ctx, s.db, plan)
}

const purchasePlanSelectCols = `
	SELECT id, name, enabled, auto_purchase, notification_days_before,
	       services, ramp_schedule, created_at, updated_at,
	       next_execution_date, last_execution_date, last_notification_sent,
	       max_step_commitment
	FROM purchase_plans`

// scanPurchasePlanRow deserialises one purchase_plans row returned by QueryRow
// or the first row of a query with FOR UPDATE. Extracted so GetPurchasePlan
// and CompletePlanStep share the same scan logic.
func scanPurchasePlanRow(row pgx.Row) (*PurchasePlan, error) {
	var plan PurchasePlan
	var servicesJSON, rampScheduleJSON []byte
	var nextExecDate, lastExecDate, lastNotifSent sql.NullTime

	err := row.Scan(
		&plan.ID,
		&plan.Name,
		&plan.Enabled,
		&plan.AutoPurchase,
		&plan.NotificationDaysBefore,
		&servicesJSON,
		&rampScheduleJSON,
		&plan.CreatedAt,
		&plan.UpdatedAt,
		&nextExecDate,
		&lastExecDate,
		&lastNotifSent,
		&plan.MaxStepCommitment,
	)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(servicesJSON, &plan.Services); err != nil {
		return nil, fmt.Errorf("failed to unmarshal services: %w", err)
	}
	if err := json.Unmarshal(rampScheduleJSON, &plan.RampSchedule); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ramp_schedule: %w", err)
	}

	if nextExecDate.Valid {
		plan.NextExecutionDate = &nextExecDate.Time
	}
	if lastExecDate.Valid {
		plan.LastExecutionDate = &lastExecDate.Time
	}
	if lastNotifSent.Valid {
		plan.LastNotificationSent = &lastNotifSent.Time
	}
	return &plan, nil
}

// GetPurchasePlan retrieves a purchase plan by ID.
func (s *PostgresStore) GetPurchasePlan(ctx context.Context, planID string) (*PurchasePlan, error) {
	query := purchasePlanSelectCols + ` WHERE id = $1`
	plan, err := scanPurchasePlanRow(s.db.QueryRow(ctx, query, planID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: purchase plan %s", ErrNotFound, planID)
		}
		return nil, fmt.Errorf("failed to get purchase plan: %w", err)
	}
	return plan, nil
}

// StampPlanNotificationSent never writes a snapshot of the plan's ramp state.
func (s *PostgresStore) StampPlanNotificationSent(ctx context.Context, planID string, at time.Time) error {
	result, err := s.db.Exec(ctx, `UPDATE purchase_plans SET last_notification_sent = $2 WHERE id = $1`, planID, at)
	if err != nil {
		return fmt.Errorf("failed to stamp plan notification: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("%w: purchase plan %s", ErrNotFound, planID)
	}
	return nil
}

// DeletePurchasePlan deletes a purchase plan.
func (s *PostgresStore) DeletePurchasePlan(ctx context.Context, planID string) error {
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		plan, err := s.LockPurchasePlanTx(ctx, tx, planID)
		if err != nil {
			return err
		}
		if plan == nil {
			return fmt.Errorf("purchase plan not found: %s", planID)
		}
		if plan.RampSchedule.DatesManaged {
			if err := requireManagedDeleteTx(ctx, tx, planID); err != nil {
				return err
			}
		}
		if err := cancelOpenPlanExecutionsTx(ctx, tx, planID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM purchase_plans WHERE id = $1`, planID); err != nil {
			return fmt.Errorf("failed to delete purchase plan: %w", err)
		}
		return nil
	})
}

// cancelOpenPlanExecutionsTx cancels the plan's step rows that can still be
// approved or run (pending, notified, paused) in the delete's transaction.
// purchase_executions.plan_id is ON DELETE SET NULL (migration 000033), and
// token approval applies the plan's max_step_commitment and the approve-own
// plan-step rule only while plan_id is set (approveConstraintsForSession), so
// a row left open would become approvable past its plan's cap (issue #862).
// The rows' suppressions go with them, as on the other cancel paths.
func cancelOpenPlanExecutionsTx(ctx context.Context, tx pgx.Tx, planID string) error {
	const q = `
		WITH canceled AS (
			UPDATE purchase_executions
			   SET status = $2, updated_at = NOW()
			 WHERE plan_id = $1
			   AND status IN ('pending', 'notified', 'paused')
			RETURNING execution_id
		)
		DELETE FROM purchase_suppressions
		 WHERE execution_id IN (SELECT execution_id FROM canceled)`
	if _, err := tx.Exec(ctx, q, planID, StatusCanceled); err != nil {
		return fmt.Errorf("failed to cancel open executions of purchase plan: %w", err)
	}
	return nil
}
