package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CompletePlanStep records that ramp step stepNumber of planID completed and
// advances the schedule to it, inside a transaction. The row is locked with
// SELECT FOR UPDATE so concurrent callers (overlapping Lambda invocations,
// multi-tick cron) cannot both read the same CurrentStep value and both write
// CurrentStep+1, skipping a step (issue #1071).
//
// The operation is idempotent in stepNumber rather than a blind increment: a
// multi-account plan produces one execution per account per ramp step, and
// retrying two separately-failed accounts of the same step used to advance the
// ramp twice, so the plan reported itself a step further along than the
// commitment it had actually bought (issue #1669). Completing a step at or
// below CurrentStep therefore writes nothing and reports
// ErrRampStepAlreadyCounted.
//
// Completing a step more than one beyond CurrentStep is refused for the same
// reason in the other direction: the steps in between never completed, and
// advancing over them would silently overstate how much the plan has bought.
//
// Granularity: a step is complete when EVERY cloud account it fanned out to has
// bought, not when any one of them has (issue #1861) -- see
// requireRampStepBought. Until then the advance reports ErrRampStepIncomplete
// and the plan row is left untouched.
//
// Returns nil when the plan no longer exists (deleted between execution and
// progress update) so the caller is not penalized for a race it cannot control.
func (s *PostgresStore) CompletePlanStep(ctx context.Context, planID string, stepNumber int) error {
	if stepNumber <= 0 {
		return fmt.Errorf("cannot complete ramp step %d of plan %s: step numbers are 1-based", stepNumber, planID)
	}
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		plan, err := s.LockPurchasePlanTx(ctx, tx, planID)
		if err != nil {
			return err
		}
		if plan == nil {
			return nil
		}

		// A finished ramp cannot be extended by a trailing execution, but it
		// still falls through to the write so next_execution_date always ends
		// up cleared (the invariant issue #1071 established).
		if !plan.RampSchedule.IsComplete() {
			if err := advanceRampStep(ctx, tx, plan, stepNumber); err != nil {
				return err
			}
		}
		return s.persistRampPosition(ctx, tx, plan)
	})
}

// LockPurchasePlanTx reads a purchase plan under a row lock held for the rest
// of tx. It is the single definition of "the per-plan ramp lock": every path
// that reads a plan's ramp position in order to write against it calls THIS
// method, so completions and creations serialize against each other rather than
// each racing on its own read. CompletePlanStep below is one caller; the
// create-planned-purchases handler is the other.
//
// Returns (nil, nil) when the plan does not exist, which callers must handle
// explicitly: CompletePlanStep treats it as a benign race it cannot control,
// the create path turns it into a 404.
func (s *PostgresStore) LockPurchasePlanTx(ctx context.Context, tx pgx.Tx, planID string) (*PurchasePlan, error) {
	plan, err := scanPurchasePlanRow(
		tx.QueryRow(ctx, purchasePlanSelectCols+` WHERE id = $1 FOR UPDATE`, planID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to lock purchase plan %s: %w", planID, err)
	}
	return plan, nil
}

// advanceRampStep moves plan's in-memory ramp position to stepNumber, or
// returns the sentinel naming why it may not. The plan row is already locked by
// the caller's transaction, so the read-modify-write cannot lose an update.
func advanceRampStep(ctx context.Context, tx pgx.Tx, plan *PurchasePlan, stepNumber int) error {
	switch {
	case plan.RampSchedule.CurrentStep == stepNumber:
		// A sibling of this very step advanced the ramp first. The step is
		// counted and nothing is wrong, so this stays distinguishable from the
		// case below: only that one is worth recording against the execution.
		return fmt.Errorf("%w: plan %s is already on ramp step %d",
			ErrRampStepCountedBySibling, plan.ID, stepNumber)
	case plan.RampSchedule.CurrentStep > stepNumber:
		return fmt.Errorf("%w: plan %s is on ramp step %d, past the completing step %d",
			ErrRampStepAlreadyCounted, plan.ID, plan.RampSchedule.CurrentStep, stepNumber)
	case plan.RampSchedule.CurrentStep != stepNumber-1:
		return fmt.Errorf("refusing to advance plan %s from ramp step %d to %d: step(s) %d-%d never completed",
			plan.ID, plan.RampSchedule.CurrentStep, stepNumber,
			plan.RampSchedule.CurrentStep+1, stepNumber-1)
	}
	if err := requireRampStepBought(ctx, tx, plan.ID, stepNumber); err != nil {
		return err
	}
	plan.RampSchedule.CurrentStep = stepNumber
	return nil
}

// persistRampPosition writes plan's ramp position back, repointing
// next_execution_date at the step after it (or clearing it on a finished ramp)
// and stamping the execution timestamps.
func (s *PostgresStore) persistRampPosition(ctx context.Context, tx pgx.Tx, plan *PurchasePlan) error {
	if plan.RampSchedule.DatesManaged {
		return s.persistManagedRampPosition(ctx, tx, plan)
	}
	if plan.RampSchedule.IsComplete() {
		plan.NextExecutionDate = nil
	} else {
		nextDate := plan.RampSchedule.GetNextPurchaseDate()
		plan.NextExecutionDate = &nextDate
	}

	now := time.Now()
	plan.LastExecutionDate = &now

	return s.UpdatePurchasePlanTx(ctx, tx, plan)
}
