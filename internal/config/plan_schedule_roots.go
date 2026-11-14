package config

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// replacePlanScheduleRootTx cancels the plan's open root for its next step and
// inserts a fresh one dated date, inside the command's transaction. The plan
// row lock is already held; the step's executions are locked by id order, the
// order ResolveManagedPlanRootTx uses. A row that is not a cancellable root
// (approved, executing, settled, failed, a retry or per-account row, or one
// that already acted, even a canceled one: it would block the new root)
// refuses the whole command, so the editor never cancels work that may be
// buying capacity.
func (s *PostgresStore) replacePlanScheduleRootTx(ctx context.Context, tx pgx.Tx, plan *PurchasePlan, actor string, date time.Time) (*PurchaseExecution, error) {
	if date.IsZero() {
		return nil, fmt.Errorf("plan %s: replacement root needs a date", plan.ID)
	}
	if plan.RampSchedule.IsComplete() {
		return nil, fmt.Errorf("%w: plan %s has no next step to schedule", ErrPurchasePlanConflict, plan.ID)
	}
	step := plan.RampSchedule.CurrentStep + 1
	if _, err := tx.Exec(ctx, `SELECT execution_id FROM purchase_executions
		WHERE plan_id=$1 AND step_number=$2 ORDER BY execution_id FOR UPDATE`, plan.ID, step); err != nil {
		return nil, fmt.Errorf("failed to lock step %d executions of plan %s: %w", step, plan.ID, err)
	}
	if err := s.cancelOpenScheduleRootsTx(ctx, tx, plan.ID, step, actor); err != nil {
		return nil, err
	}
	root := &PurchaseExecution{PlanID: plan.ID, Status: "pending", StepNumber: step, ScheduledDate: date}
	if err := s.SavePurchaseExecutionTx(ctx, tx, root); err != nil {
		return nil, fmt.Errorf("failed to insert replacement root: %w", err)
	}
	return root, nil
}

func cancellableScheduleRoot(row *PurchaseExecution) bool {
	if !managedRootIdentity(row) || managedRootPriorAction(row) {
		return false
	}
	switch row.Status {
	case "pending", "notified", "scheduled":
		return true
	}
	return false
}

// cancelScheduleRootTx cancels one locked row. The status guard repeats in the
// UPDATE so a row that moved on since the read is a conflict, not a lost cancel.
func (s *PostgresStore) cancelScheduleRootTx(ctx context.Context, tx pgx.Tx, executionID, actor string) error {
	tag, err := tx.Exec(ctx, `UPDATE purchase_executions SET status=$2, canceled_by=$3, updated_at=NOW()
		WHERE execution_id=$1 AND status IN ('pending','notified','scheduled')`, executionID, StatusCanceled, actor)
	if err != nil {
		return fmt.Errorf("failed to cancel root %s: %w", executionID, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: execution %s changed while replacing the root", ErrExecutionNotInExpectedStatus, executionID)
	}
	return s.DeleteSuppressionsByExecutionTx(ctx, tx, executionID)
}

// cancelOpenScheduleRootsTx cancels every cancellable root of the step and
// refuses when any other live or acted-on row exists.
func (s *PostgresStore) cancelOpenScheduleRootsTx(ctx context.Context, tx pgx.Tx, planID string, step int, actor string) error {
	rows, err := s.ListExecutionsForPlanStepTx(ctx, tx, planID, step)
	if err != nil {
		return err
	}
	for i := range rows {
		if rows[i].Status == StatusCanceled && !managedRootPriorAction(&rows[i]) {
			continue
		}
		if !cancellableScheduleRoot(&rows[i]) {
			return fmt.Errorf("%w: execution %s (%s) of plan %s step %d cannot be replaced",
				ErrExecutionNotInExpectedStatus, rows[i].ExecutionID, rows[i].Status, planID, step)
		}
		if err := s.cancelScheduleRootTx(ctx, tx, rows[i].ExecutionID, actor); err != nil {
			return err
		}
	}
	return nil
}
