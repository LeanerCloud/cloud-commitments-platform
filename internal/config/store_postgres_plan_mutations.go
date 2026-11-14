package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// UpdatePurchasePlan publishes the new version only after the transaction commits.
func (s *PostgresStore) UpdatePurchasePlan(ctx context.Context, plan *PurchasePlan) error {
	return s.UpdatePurchasePlanGated(ctx, plan, nil)
}

// UpdatePurchasePlanGated is UpdatePurchasePlan with gate (may be nil) run after
// the plan row is locked and before the write. A missing plan skips the gate and
// reports ErrNotFound from the update.
func (s *PostgresStore) UpdatePurchasePlanGated(ctx context.Context, plan *PurchasePlan, gate PlanWriteGate) error {
	pending := *plan
	if err := s.WithTx(ctx, func(tx pgx.Tx) error {
		old, err := s.LockPurchasePlanTx(ctx, tx, pending.ID)
		if err != nil {
			return err
		}
		if gate != nil && old != nil {
			if gateErr := s.runPlanGateTx(ctx, tx, old, gate); gateErr != nil {
				return gateErr
			}
		}
		if updErr := s.updatePurchasePlanGuardedTx(ctx, tx, old, &pending); updErr != nil {
			return updErr
		}
		return clearStaleRampBaselineTx(ctx, tx, old, &pending)
	}); err != nil {
		return err
	}
	plan.UpdatedAt = pending.UpdatedAt
	return nil
}

// runPlanGateTx hands gate the locked plan and its sorted account ids, read on
// tx.
func (s *PostgresStore) runPlanGateTx(ctx context.Context, tx pgx.Tx, locked *PurchasePlan, gate PlanWriteGate) error {
	rows, err := tx.Query(ctx, `SELECT account_id::text FROM plan_accounts WHERE plan_id = $1 ORDER BY 1`, locked.ID)
	if err != nil {
		return fmt.Errorf("failed to read plan accounts: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("failed to read plan accounts: %w", err)
	}
	return gate(locked, ids)
}

// clearStaleRampBaselineTx drops the ramp baseline an update made stale. It runs
// after the update, never before: clearing bumps updated_at, which would fail
// the update's own optimistic-lock check.
func clearStaleRampBaselineTx(ctx context.Context, tx pgx.Tx, old, pending *PurchasePlan) error {
	if old == nil || !rampBaselineStale(old, pending) {
		return nil
	}
	err := tx.QueryRow(ctx,
		`UPDATE purchase_plans SET ramp_baseline = NULL WHERE id = $1 AND ramp_baseline IS NOT NULL RETURNING updated_at`,
		pending.ID).Scan(&pending.UpdatedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("failed to clear ramp baseline of plan %s: %w", pending.ID, err)
	}
	return nil
}

// UpdatePurchasePlanTx rejects snapshots that lost a race with another plan
// writer and refuses changes to what the editor owns on a managed plan. It
// re-locks the row, which every caller already holds, to judge the guard on
// the locked version.
func (s *PostgresStore) UpdatePurchasePlanTx(ctx context.Context, tx pgx.Tx, plan *PurchasePlan) error {
	old, err := s.LockPurchasePlanTx(ctx, tx, plan.ID)
	if err != nil {
		return err
	}
	return s.updatePurchasePlanGuardedTx(ctx, tx, old, plan)
}

// updatePurchasePlanGuardedTx runs the managed-plan guard against old, the row
// locked by the caller's transaction, and then the optimistic update.
func (s *PostgresStore) updatePurchasePlanGuardedTx(ctx context.Context, tx pgx.Tx, old, plan *PurchasePlan) error {
	if err := requireManagedPlanUpdateTx(old, plan); err != nil {
		return err
	}
	// Marshal services and ramp_schedule to JSONB
	servicesJSON, err := marshalPlanServices(plan.Services)
	if err != nil {
		return err
	}

	rampScheduleJSON, err := json.Marshal(plan.RampSchedule)
	if err != nil {
		return fmt.Errorf("failed to marshal ramp_schedule: %w", err)
	}

	query := `
		UPDATE purchase_plans SET
			name = $2,
			enabled = $3,
			auto_purchase = $4,
			notification_days_before = $5,
			services = $6,
			ramp_schedule = $7,
			next_execution_date = $9,
			last_execution_date = $10,
			last_notification_sent = $11,
			max_step_commitment = $12
		WHERE id = $1 AND updated_at = $8
		RETURNING updated_at
	`

	var updatedAt time.Time
	err = tx.QueryRow(ctx, query,
		plan.ID,
		plan.Name,
		plan.Enabled,
		plan.AutoPurchase,
		plan.NotificationDaysBefore,
		servicesJSON,
		rampScheduleJSON,
		plan.UpdatedAt,
		plan.NextExecutionDate,
		plan.LastExecutionDate,
		plan.LastNotificationSent,
		plan.MaxStepCommitment,
	).Scan(&updatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if probeErr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM purchase_plans WHERE id = $1)`, plan.ID).Scan(&exists); probeErr != nil {
			return fmt.Errorf("failed to check purchase plan after conflict: %w", probeErr)
		}
		if !exists {
			return fmt.Errorf("%w: purchase plan %s", ErrNotFound, plan.ID)
		}
		return ErrPurchasePlanConflict
	}
	if err != nil {
		return fmt.Errorf("failed to update purchase plan: %w", err)
	}
	plan.UpdatedAt = updatedAt
	return nil
}
