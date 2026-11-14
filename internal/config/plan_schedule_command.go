package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrPlanScheduleCommandConflict means a command id was already used with a
// different request (hash or plan). The caller must pick a new id.
var ErrPlanScheduleCommandConflict = errors.New("plan schedule command id reused with a different request")

// ErrPlanScheduleRetryable means the command did not commit because of lock
// contention, a deadlock or a serialization failure. Nothing was written and
// the same command may be sent again.
var ErrPlanScheduleRetryable = errors.New("plan schedule command is busy, retry")

const (
	planScheduleMaxAttempts = 3
	pgSerializationFailure  = "40001"
	pgDeadlockDetected      = "40P01"
	pgLockNotAvailable      = "55P03"
)

// planScheduleLockTimeout bounds every lock wait inside a command so a busy
// plan cannot pin one of the few pooled connections. A var so tests can shrink it.
var planScheduleLockTimeout = 5 * time.Second

// PlanScheduleCommand is one schedule-editor Save. ExpectedUpdatedAt is the
// plan version the editor read. Name and NextExecutionDate change only when
// set. RequestHash is the caller's hash of the canonical request.
type PlanScheduleCommand struct {
	CommandID         string
	PlanID            string
	Actor             string
	RequestHash       string
	ExpectedUpdatedAt time.Time
	Name              *string
	NextExecutionDate *time.Time
	GroupTarget       *PlanGroupTarget
	// DatesManaged adopts or releases the editor's ownership of the dates.
	DatesManaged *bool
	// ReplaceRootDate cancels the open root of the next step and inserts a
	// new one on this date; it needs a managed plan. The plan's next date
	// follows it.
	ReplaceRootDate *time.Time
	// Accounts replaces the plan's account set in the same transaction.
	Accounts *[]string
}

// PlanScheduleOutcome is what a command returns and what its receipt stores.
// Replayed is set on a replay and is never stored.
type PlanScheduleOutcome struct {
	PlanID            string     `json:"plan_id"`
	Name              string     `json:"name"`
	UpdatedAt         time.Time  `json:"updated_at"`
	NextExecutionDate *time.Time `json:"next_execution_date,omitempty"`
	RootExecutionID   string     `json:"root_execution_id,omitempty"`
	Replayed          bool       `json:"-"`
}

func (c PlanScheduleCommand) validate() error {
	if _, err := uuid.Parse(c.CommandID); err != nil {
		return fmt.Errorf("plan schedule command id must be a UUID: %w", err)
	}
	if c.PlanID == "" || c.Actor == "" || c.RequestHash == "" || c.ExpectedUpdatedAt.IsZero() {
		return errors.New("plan schedule command needs plan id, actor, request hash and expected version")
	}
	if c.Accounts != nil && len(*c.Accounts) == 0 {
		return errors.New("plan schedule command cannot clear every account of a plan")
	}
	return nil
}

// ApplyPlanScheduleCommand applies cmd in one transaction. Lock order is the
// plan row first (later commits add executions after it). A command id seen
// before returns its stored outcome without writing; the same id with another
// request is ErrPlanScheduleCommandConflict; a stale ExpectedUpdatedAt is
// ErrPurchasePlanConflict. Lock waits, deadlocks and serialization failures
// are retried a bounded number of times, then reported as
// ErrPlanScheduleRetryable.
func (s *PostgresStore) ApplyPlanScheduleCommand(ctx context.Context, cmd PlanScheduleCommand) (*PlanScheduleOutcome, error) {
	if err := cmd.validate(); err != nil {
		return nil, err
	}
	var outcome *PlanScheduleOutcome
	err := retryPlanScheduleTx(func() error {
		return s.WithTx(ctx, func(tx pgx.Tx) error {
			var txErr error
			outcome, txErr = s.applyPlanScheduleCommandTx(ctx, tx, cmd)
			return txErr
		})
	})
	if err != nil {
		return nil, err
	}
	return outcome, nil
}

func (s *PostgresStore) applyPlanScheduleCommandTx(ctx context.Context, tx pgx.Tx, cmd PlanScheduleCommand) (*PlanScheduleOutcome, error) {
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`,
		fmt.Sprintf("%dms", planScheduleLockTimeout.Milliseconds())); err != nil {
		return nil, fmt.Errorf("failed to bound plan schedule lock wait: %w", err)
	}
	locked, err := s.LockPurchasePlanTx(ctx, tx, cmd.PlanID)
	if err != nil {
		return nil, err
	}
	if locked == nil {
		return nil, fmt.Errorf("%w: purchase plan %s", ErrNotFound, cmd.PlanID)
	}
	receipt, err := s.GetPlanScheduleReceiptTx(ctx, tx, cmd.CommandID)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		return replayPlanScheduleReceipt(receipt, cmd)
	}

	return s.writePlanScheduleCommandTx(ctx, tx, locked, cmd)
}

// buildScheduleNext applies the command's plan fields to a copy of locked and
// judges the legacy managed-plan guard on it. The command owns the marker and
// the dates; every other editor-owned field of a managed plan still goes
// through the guard. On a managed plan the root is the date source, so a bare
// NextExecutionDate is refused: it would diverge from the root that fires.
func buildScheduleNext(locked *PurchasePlan, cmd PlanScheduleCommand) (*PurchasePlan, error) {
	next := *locked
	if cmd.GroupTarget != nil {
		merged, err := MergePlanGroupTarget(locked, *cmd.GroupTarget)
		if err != nil {
			return nil, err
		}
		next = *merged
	}
	next.UpdatedAt = cmd.ExpectedUpdatedAt
	if cmd.Name != nil {
		next.Name = *cmd.Name
	}
	if cmd.DatesManaged != nil {
		next.RampSchedule.DatesManaged = *cmd.DatesManaged
	}
	if cmd.NextExecutionDate != nil {
		if next.RampSchedule.DatesManaged {
			return nil, fmt.Errorf("%w: plan %s is date-managed, set the date through the root", ErrPurchasePlanConflict, locked.ID)
		}
		next.NextExecutionDate = cmd.NextExecutionDate
	}
	guarded := next
	guarded.RampSchedule.DatesManaged = locked.RampSchedule.DatesManaged
	guarded.NextExecutionDate = locked.NextExecutionDate
	if err := requireManagedPlanUpdateTx(locked, &guarded); err != nil {
		return nil, err
	}
	return &next, nil
}

// writePlanScheduleCommandTx writes the plan change, roots, accounts and receipt.
func (s *PostgresStore) writePlanScheduleCommandTx(ctx context.Context, tx pgx.Tx, locked *PurchasePlan, cmd PlanScheduleCommand) (*PlanScheduleOutcome, error) {
	next, err := buildScheduleNext(locked, cmd)
	if err != nil {
		return nil, err
	}
	rootID, err := s.writeScheduleChangesTx(ctx, tx, next, cmd)
	if err != nil {
		return nil, err
	}
	outcome := &PlanScheduleOutcome{
		PlanID: next.ID, Name: next.Name, UpdatedAt: next.UpdatedAt, NextExecutionDate: next.NextExecutionDate,
		RootExecutionID: rootID,
	}
	stored, err := json.Marshal(outcome)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal plan schedule outcome: %w", err)
	}
	if err := s.InsertPlanScheduleReceiptTx(ctx, tx, &PlanScheduleReceipt{
		CommandID: cmd.CommandID, PlanID: cmd.PlanID, Actor: cmd.Actor,
		RequestHash: cmd.RequestHash, Outcome: stored,
	}); err != nil {
		return nil, err
	}
	return outcome, nil
}

func replayPlanScheduleReceipt(r *PlanScheduleReceipt, cmd PlanScheduleCommand) (*PlanScheduleOutcome, error) {
	if r.RequestHash != cmd.RequestHash || r.PlanID != cmd.PlanID {
		return nil, fmt.Errorf("%w: %s", ErrPlanScheduleCommandConflict, cmd.CommandID)
	}
	var outcome PlanScheduleOutcome
	if err := json.Unmarshal(r.Outcome, &outcome); err != nil {
		return nil, fmt.Errorf("failed to read stored plan schedule outcome %s: %w", cmd.CommandID, err)
	}
	outcome.Replayed = true
	return &outcome, nil
}

// retryPlanScheduleTx runs attempt up to planScheduleMaxAttempts times while it fails with
// a transient Postgres code, then wraps the last failure in ErrPlanScheduleRetryable.
func retryPlanScheduleTx(attempt func() error) error {
	var err error
	for i := 0; i < planScheduleMaxAttempts; i++ {
		if err = attempt(); !isTransientPlanScheduleErr(err) {
			return err
		}
	}
	return fmt.Errorf("%w: %w", ErrPlanScheduleRetryable, err)
}

func isTransientPlanScheduleErr(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case pgSerializationFailure, pgDeadlockDetected, pgLockNotAvailable:
		return true
	}
	return false
}

// replaceScheduleRootStepTx runs the command's root replacement, if any, and
// points next at the new root's date. It returns the new root's id.
func (s *PostgresStore) replaceScheduleRootStepTx(ctx context.Context, tx pgx.Tx, next *PurchasePlan, cmd PlanScheduleCommand) (string, error) {
	if cmd.ReplaceRootDate == nil {
		return "", nil
	}
	if !next.RampSchedule.DatesManaged {
		return "", fmt.Errorf("%w: plan %s is not date-managed, roots cannot be replaced", ErrPurchasePlanConflict, next.ID)
	}
	root, err := s.replacePlanScheduleRootTx(ctx, tx, next, cmd.Actor, *cmd.ReplaceRootDate)
	if err != nil {
		return "", err
	}
	next.NextExecutionDate = &root.ScheduledDate
	return root.ExecutionID, nil
}

// writeScheduleChangesTx writes the root replacement, the plan row and the
// accounts, in that order, and returns the new root's id.
func (s *PostgresStore) writeScheduleChangesTx(ctx context.Context, tx pgx.Tx, next *PurchasePlan, cmd PlanScheduleCommand) (string, error) {
	rootID, err := s.replaceScheduleRootStepTx(ctx, tx, next, cmd)
	if err != nil {
		return "", err
	}
	if err := s.updatePurchasePlanGuardedTx(ctx, tx, nil, next); err != nil {
		return "", err
	}
	if cmd.Accounts == nil {
		return rootID, nil
	}
	return rootID, s.setPlanAccountsTx(ctx, tx, next.ID, *cmd.Accounts)
}
