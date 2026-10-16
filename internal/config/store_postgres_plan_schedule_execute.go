package config

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// planScheduleCommandWait is the 5s statement_timeout every holder runs under, so a waiter outlives a stuck holder's
// statement by no more than that; the caller's own deadline wins when sooner.
const planScheduleCommandWait = 5 * time.Second

// ErrPlanScheduleBusy means no command slot freed up in time. Slots are max(1, poolMax-1): with poolMax >= 2
// one pool connection stays free for other work. Callbacks that read through the pool are bounded by the caller's ctx, not the tx timeouts.
var ErrPlanScheduleBusy = errors.New("plan schedule editor busy")

func (s *PostgresStore) ExecutePlanScheduleCommand(ctx context.Context, identity PlanScheduleCommandIdentity, build BuildAuthorizedPlanScheduleMutation, authorizeReplay AuthorizePlanScheduleReplay, project ProjectCommittedPlanSchedule) (*PlanScheduleCommandResult, error) {
	if err := validatePlanScheduleIdentity(identity); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		result, err := s.attemptPlanScheduleCommand(ctx, identity, build, authorizeReplay, project)
		if err == nil {
			return result, nil
		}
		if !retryPlanScheduleCommand(err) {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && (pgErr.Code == "55P03" || pgErr.Code == "57014") {
				return nil, fmt.Errorf("%w: command timed out", ErrPurchasePlanConflict)
			}
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w: command retry exhausted", ErrPurchasePlanConflict)
}

// attemptPlanScheduleCommand holds a slot for exactly one fresh transaction, so a replay frees it as soon as it commits.
func (s *PostgresStore) attemptPlanScheduleCommand(ctx context.Context, identity PlanScheduleCommandIdentity, build BuildAuthorizedPlanScheduleMutation, authorizeReplay AuthorizePlanScheduleReplay, project ProjectCommittedPlanSchedule) (*PlanScheduleCommandResult, error) {
	release, err := s.acquirePlanScheduleSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var result *PlanScheduleCommandResult
	err = s.WithTx(ctx, func(tx pgx.Tx) (txErr error) {
		result, txErr = s.executePlanScheduleCommandTx(ctx, tx, identity, build, authorizeReplay, project)
		return txErr
	})
	return result, err
}

func (s *PostgresStore) acquirePlanScheduleSlot(ctx context.Context) (release func(), err error) {
	if s.commandSlots == nil {
		return nil, ErrPlanScheduleBusy
	}
	timer := time.NewTimer(cmp.Or(s.commandWait, planScheduleCommandWait))
	defer timer.Stop()
	select {
	case s.commandSlots <- struct{}{}:
		return func() { <-s.commandSlots }, nil
	case <-timer.C:
		return nil, ErrPlanScheduleBusy
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrPlanScheduleBusy, ctx.Err())
	}
}

func retryPlanScheduleCommand(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code == "40001" || pgErr.Code == "40P01" {
		return true
	}
	return pgErr.Code == "23505" && (pgErr.ConstraintName == "plan_schedule_command_receipts_pkey" || pgErr.ConstraintName == "purchase_plans_pkey")
}

func (s *PostgresStore) executePlanScheduleCommandTx(ctx context.Context, tx pgx.Tx, identity PlanScheduleCommandIdentity, build BuildAuthorizedPlanScheduleMutation, authorizeReplay AuthorizePlanScheduleReplay, project ProjectCommittedPlanSchedule) (*PlanScheduleCommandResult, error) {
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL READ COMMITTED`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','2s',true), set_config('statement_timeout','5s',true)`); err != nil {
		return nil, err
	}
	if err := lockPlanScheduleIdentityTx(ctx, tx, "command:", identity.CommandID); err != nil {
		return nil, err
	}
	if replay, err := readPlanScheduleReceiptTx(ctx, tx, identity, authorizeReplay); replay != nil || err != nil {
		return replay, err
	}
	before, accounts, err := s.lockPlanScheduleStateTx(ctx, tx, identity)
	if err != nil {
		return nil, err
	}
	mutation, transactionTime, err := buildPlanScheduleMutationTx(ctx, tx, identity, before, accounts, build)
	if err != nil {
		return nil, err
	}
	return s.commitPlanScheduleMutationTx(ctx, tx, identity, capturePlanScheduleWindows(before), mutation, transactionTime, before, project)
}

// buildPlanScheduleMutationTx checks the revision before calling build, so a stale base never reaches the callback.
func buildPlanScheduleMutationTx(ctx context.Context, tx pgx.Tx, identity PlanScheduleCommandIdentity, before *PlanEditorSnapshot, accounts []PlanEditorAccount, build BuildAuthorizedPlanScheduleMutation) (*PlanScheduleMutation, time.Time, error) {
	var now, transactionTime time.Time
	if err := checkPlanScheduleRevision(identity, before); err != nil {
		return nil, transactionTime, err
	}
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp(),transaction_timestamp()`).Scan(&now, &transactionTime); err != nil {
		return nil, transactionTime, err
	}
	mutation, err := build(ctx, before, accounts, now)
	if err != nil {
		return nil, transactionTime, err
	}
	if mutation == nil || mutation.Plan == nil || mutation.Plan.ID != identity.PlanID {
		return nil, transactionTime, ErrPurchasePlanConflict
	}
	return mutation, transactionTime, validatePlanScheduleLockedTarget(accounts, mutation)
}

func (s *PostgresStore) commitPlanScheduleMutationTx(ctx context.Context, tx pgx.Tx, identity PlanScheduleCommandIdentity, windows planScheduleWindows, mutation *PlanScheduleMutation, transactionTime time.Time, before *PlanEditorSnapshot, project ProjectCommittedPlanSchedule) (*PlanScheduleCommandResult, error) {
	mapping, err := s.persistPlanScheduleMutationTx(ctx, tx, before, windows, mutation, identity.Actor.UserID, transactionTime)
	if err != nil {
		return nil, err
	}
	// The narrow next_execution_date UPDATE moved updated_at, so mutation.Plan is stale: project and receipt the stored row.
	after, err := s.readPlanEditorSnapshotTx(ctx, tx, identity.PlanID)
	if err != nil {
		return nil, err
	}
	body, err := project(ctx, after, mapping, mutation.Diff)
	if err != nil {
		return nil, err
	}
	result, err := insertPlanScheduleReceiptTx(ctx, tx, identity, after, mutation.Scope, body)
	if err != nil {
		return nil, err
	}
	return result, guardPlanScheduleClockTx(ctx, tx, windows, mutation.Plan.NotificationDaysBefore, mutation.Roots)
}

// checkPlanScheduleRevision runs before build: a stale base never reaches the callback.
func checkPlanScheduleRevision(identity PlanScheduleCommandIdentity, before *PlanEditorSnapshot) error {
	if (before == nil) != (identity.ExpectedRevision == nil) {
		return ErrPurchasePlanConflict
	}
	if before == nil {
		return nil
	}
	revision, err := before.Revision()
	if err != nil {
		return err
	}
	if revision != *identity.ExpectedRevision {
		return ErrPurchasePlanConflict
	}
	return nil
}
