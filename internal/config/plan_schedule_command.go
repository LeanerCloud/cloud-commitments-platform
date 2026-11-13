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
}

// PlanScheduleOutcome is what a command returns and what its receipt stores.
// Replayed is set on a replay and is never stored.
type PlanScheduleOutcome struct {
	PlanID            string     `json:"plan_id"`
	Name              string     `json:"name"`
	UpdatedAt         time.Time  `json:"updated_at"`
	NextExecutionDate *time.Time `json:"next_execution_date,omitempty"`
	Replayed          bool       `json:"-"`
}

func (c PlanScheduleCommand) validate() error {
	if _, err := uuid.Parse(c.CommandID); err != nil {
		return fmt.Errorf("plan schedule command id must be a UUID: %w", err)
	}
	if c.PlanID == "" || c.Actor == "" || c.RequestHash == "" || c.ExpectedUpdatedAt.IsZero() {
		return errors.New("plan schedule command needs plan id, actor, request hash and expected version")
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

// writePlanScheduleCommandTx writes the plan change and its receipt.
func (s *PostgresStore) writePlanScheduleCommandTx(ctx context.Context, tx pgx.Tx, locked *PurchasePlan, cmd PlanScheduleCommand) (*PlanScheduleOutcome, error) {
	next := *locked
	next.UpdatedAt = cmd.ExpectedUpdatedAt
	if cmd.Name != nil {
		next.Name = *cmd.Name
	}
	if cmd.NextExecutionDate != nil {
		next.NextExecutionDate = cmd.NextExecutionDate
	}
	if err := s.updatePurchasePlanGuardedTx(ctx, tx, locked, &next); err != nil {
		return nil, err
	}
	outcome := &PlanScheduleOutcome{
		PlanID: next.ID, Name: next.Name, UpdatedAt: next.UpdatedAt, NextExecutionDate: next.NextExecutionDate,
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
