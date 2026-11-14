package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// dbConn is the minimal interface used by PostgresStore.
// Both *database.Connection and pgxmock.PgxPoolIface satisfy this interface.
type dbConn interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// PostgresStore implements StoreInterface using PostgreSQL.
type PostgresStore struct {
	db dbConn
	// amountBasisReady latches true once purchase_history.amount_basis is
	// known to exist (amountBasisColumn).
	amountBasisReady atomic.Bool
}

// NewPostgresStore creates a new PostgreSQL-backed config store.
func NewPostgresStore(db *database.Connection) *PostgresStore {
	return &PostgresStore{db: db}
}

// Verify PostgresStore implements StoreInterface.
var _ StoreInterface = (*PostgresStore)(nil)

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

// LinkRetryExecutionAtomic stamps retry_execution_id on a failed execution
// row, conditional on status = 'failed' AND retry_execution_id IS NULL,
// without touching any other column. This replaces a full-row
// SavePurchaseExecutionTx upsert of a stale in-memory copy of the failed
// row (issue #220): that upsert both raced two concurrent retries of the
// same row past the in-memory already-retried guard (both would insert an
// approvable successor and send an approval email) and reverted any column
// another writer changed on the failed row in between.
//
// Returns (true, nil) when this call won the race and wrote the linkage,
// (false, nil) when zero rows were affected because a concurrent retry
// already claimed the row (or it is no longer 'failed', or an operator
// settled it: SettleFailedExecution, issue #999), and (false, err)
// on a real DB error. Must be called inside the same tx that inserts the
// successor row, after that insert, so the FK on retry_execution_id is
// satisfied.
func (s *PostgresStore) LinkRetryExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID, retryExecutionID string) (linked bool, err error) {
	return s.linkRetryExecution(ctx, tx, executionID, retryExecutionID, "")
}

// LinkUnsentRetryExecutionAtomic is LinkRetryExecutionAtomic for a retry the
// handler allowed only because the row is a proven-unsent first attempt: the
// same conditional UPDATE plus no_submission AND retry_attempt_n = 0, so a row
// that stopped being that proof between the gate and the link is not linked.
func (s *PostgresStore) LinkUnsentRetryExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID, retryExecutionID string) (linked bool, err error) {
	return s.linkRetryExecution(ctx, tx, executionID, retryExecutionID, "AND no_submission AND retry_attempt_n = 0")
}

// linkRetryExecution is the shared link CAS; extraPredicate narrows it.
func (s *PostgresStore) linkRetryExecution(ctx context.Context, tx pgx.Tx, executionID, retryExecutionID, extraPredicate string) (bool, error) {
	// Before the settlement migration no row can be settled (#999).
	notSettled := ""
	if ready, readyErr := settlementSchemaReady(ctx, tx); readyErr != nil {
		return false, readyErr
	} else if ready {
		notSettled = "AND outcome_settled_at IS NULL"
	}
	q := `
		UPDATE purchase_executions
		   SET retry_execution_id = $2,
		       updated_at = NOW()
		 WHERE execution_id = $1
		   AND status = 'failed'
		   AND retry_execution_id IS NULL
		   ` + notSettled + `
		   ` + extraPredicate
	tag, err := tx.Exec(ctx, q, executionID, retryExecutionID)
	if err != nil {
		return false, fmt.Errorf("failed to link retry execution: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RotatePendingApprovalToken implements StoreInterface. The same statement
// that stores the emailed token promotes the row to 'notified' (a no-op for a
// row already there), so the status never claims a notice that was not sent.
func (s *PostgresStore) RotatePendingApprovalToken(ctx context.Context, executionID, tokenHash string, expiresAt time.Time) (bool, error) {
	q := `
		UPDATE purchase_executions
		   SET approval_token_hash = $2,
		       approval_token_expires_at = $3,
		       status = 'notified',
		       updated_at = NOW()
		 WHERE execution_id = $1
		   AND status IN ('pending', 'notified')
	`
	tag, err := s.db.Exec(ctx, q, executionID, tokenHash, expiresAt)
	if err != nil {
		return false, fmt.Errorf("failed to rotate approval token: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// TransitionExecutionStatus atomically transitions an execution from one of the
// allowed statuses to a new status. Returns the updated record, or an error if
// the execution was not found or not in an allowed status.
// actor is the UUID of the user performing the transition (nil for system-initiated paths);
// it is stamped onto transitioned_by and transitioned_at is always set to NOW().
// A transition to StatusCanceled also stamps the actor's email onto canceled_by
// (issue #119), matching what the other cancel paths record and what History
// reads.
func (s *PostgresStore) TransitionExecutionStatus(ctx context.Context, executionID string, fromStatuses []string, toStatus string, actor *string) (*PurchaseExecution, error) {
	return s.transitionExecutionStatus(ctx, s.db, executionID, fromStatuses, toStatus, actor, nil, nil)
}

// TransitionExecutionStatusPinned is TransitionExecutionStatus that also
// requires the row's recommendations to still be empty (or not empty), as the
// caller saw them. A recommendation attach landing between a gate check and the
// claim makes the pin fail with ErrExecutionNotInExpectedStatus, so a row is
// never run on a snapshot different from the one that was gated.
func (s *PostgresStore) TransitionExecutionStatusPinned(ctx context.Context, executionID string, fromStatuses []string, toStatus string, actor *string, sawEmptyRecommendations bool) (*PurchaseExecution, error) {
	return s.transitionExecutionStatus(ctx, s.db, executionID, fromStatuses, toStatus, actor, &sawEmptyRecommendations, nil)
}

// TransitionExecutionStatusPinnedTx is TransitionExecutionStatusPinned inside
// the caller's transaction. The UPDATE's row lock is held until that
// transaction ends, so the status change commits or rolls back together with
// the caller's other writes and no other writer can touch the row in between.
func (s *PostgresStore) TransitionExecutionStatusPinnedTx(ctx context.Context, tx pgx.Tx, executionID string, fromStatuses []string, toStatus string, actor *string, sawEmptyRecommendations bool) (*PurchaseExecution, error) {
	return s.transitionExecutionStatus(ctx, tx, executionID, fromStatuses, toStatus, actor, &sawEmptyRecommendations, nil)
}

// TransitionExecutionStatusTokenPinned is TransitionExecutionStatus that also
// requires approval_token_hash to equal the hash of rawToken and the token to
// be unexpired, checked in the same UPDATE as the status change. The caller's
// earlier token check ran against a snapshot; a re-notification that rotates
// the token between that read and this write would otherwise still let the
// old token win the transition (issue #590). An empty rawToken never matches.
func (s *PostgresStore) TransitionExecutionStatusTokenPinned(ctx context.Context, executionID string, fromStatuses []string, toStatus string, actor *string, rawToken string) (*PurchaseExecution, error) {
	if rawToken == "" {
		return nil, fmt.Errorf("%w: execution %s: no approval token supplied", ErrApprovalTokenChanged, executionID)
	}
	tokenHash := HashApprovalToken(rawToken)
	return s.transitionExecutionStatus(ctx, s.db, executionID, fromStatuses, toStatus, actor, nil, &tokenHash)
}

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

// CancelExecutionAtomic atomically transitions an execution from
// pending or notified to 'canceled' (canonical US spelling), setting
// canceled_by to the supplied actor (NULL when actor is nil). The UPDATE
// is conditional on
// status IN ('pending','notified') so a concurrent approve that has
// already transitioned the row to 'approved' causes zero rows to be
// affected and the method returns (false, currentStatus, nil) with the
// live status fetched via a follow-up SELECT. Returns (true, "canceled",
// nil) on success and (false, "", err) on a real DB error.
//
// The 'scheduled' status is intentionally NOT accepted here -- the
// Gmail-style pre-fire delay revoke flow has its own
// CancelScheduledExecutionAtomic so the two paths surface distinct CAS
// race outcomes (cancel returns 409 on already-approved; scheduled-revoke
// returns 410 "window closed" on already-fired).
//
// Callers must run the suppression cleanup in the same transaction; use
// the WithTx + DeleteSuppressionsByExecutionTx pairing at the call site
// exactly as the old SavePurchaseExecutionTx path did, except now the
// status guard is inside the UPDATE rather than checked optimistically
// before entering the tx.
func (s *PostgresStore) CancelExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (canceled bool, currentStatus string, err error) {
	return s.cancelExecutionAtomic(ctx, tx, executionID, cancelledBy, nil)
}

// CancelExecutionAtomicTokenPinned is CancelExecutionAtomic that also requires
// approval_token_hash to equal the hash of rawToken and the token to be
// unexpired, checked in the same UPDATE as the status change. The email-link
// cancel validated the token against a snapshot; a re-notification that
// rotates the token between that read and this write would otherwise still
// let the old, revoked token cancel the row (issue #1190, the cancel side of
// #590). A lost token pin on a row still pending/notified returns an error
// wrapping ErrApprovalTokenChanged; a lost status CAS returns (false,
// currentStatus, nil) as CancelExecutionAtomic does. An empty rawToken never
// matches.
func (s *PostgresStore) CancelExecutionAtomicTokenPinned(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string, rawToken string) (canceled bool, currentStatus string, err error) {
	if rawToken == "" {
		return false, "", fmt.Errorf("%w: execution %s: no approval token supplied", ErrApprovalTokenChanged, executionID)
	}
	tokenHash := HashApprovalToken(rawToken)
	return s.cancelExecutionAtomic(ctx, tx, executionID, cancelledBy, &tokenHash)
}

// cancelExecutionAtomic is the shared body of CancelExecutionAtomic and
// CancelExecutionAtomicTokenPinned; a nil tokenHash skips the token pin.
func (s *PostgresStore) cancelExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy, tokenHash *string) (canceled bool, currentStatus string, err error) {
	// StatusCanceled is the canonical US-spelling value; see types.go.
	q := `
		UPDATE purchase_executions
		   SET status       = $3,
		       canceled_by  = $2,
		       updated_at   = NOW()
		 WHERE execution_id = $1
		   AND status IN ('pending', 'notified')
		   AND ($4::text IS NULL OR (approval_token_hash = $4
		        AND (approval_token_expires_at IS NULL OR approval_token_expires_at > NOW())))
		RETURNING status
	`
	rows, err := tx.Query(ctx, q, executionID, cancelledBy, StatusCanceled, tokenHash)
	if err != nil {
		return false, "", fmt.Errorf("failed to cancel execution: %w", err)
	}
	defer rows.Close()

	if rows.Next() {
		var st string
		if scanErr := rows.Scan(&st); scanErr != nil {
			return false, "", fmt.Errorf("failed to scan cancel result: %w", scanErr)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return false, "", fmt.Errorf("failed to iterate cancel result: %w", rowsErr)
		}
		return true, st, nil
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return false, "", fmt.Errorf("failed to iterate cancel result: %w", rowsErr)
	}

	// Zero rows affected: execution either does not exist, has already
	// transitioned out of pending/notified, or (pinned) no longer carries the
	// caller's token. Surface the current status so callers can return a
	// meaningful 409 body.
	currentStatus, err = s.cancelRejection(ctx, executionID, tokenHash != nil)
	return false, currentStatus, err
}

// cancelRejection explains a zero-row cancelExecutionAtomic by probing the
// row's current status. A row still pending/notified under a token pin lost
// the pin, not the status race, so that case returns ErrApprovalTokenChanged.
// Extracted to keep the caller under the gocyclo threshold.
func (s *PostgresStore) cancelRejection(ctx context.Context, executionID string, tokenPinned bool) (currentStatus string, err error) {
	existing, existErr := s.GetExecutionByID(ctx, executionID)
	if existErr != nil {
		return "", fmt.Errorf("execution not found or db error: %w", existErr)
	}
	if tokenPinned && (existing.Status == "pending" || existing.Status == "notified") {
		return existing.Status, fmt.Errorf("%w: execution %s", ErrApprovalTokenChanged, executionID)
	}
	return existing.Status, nil
}

// CancelScheduledExecutionAtomic atomically transitions an execution from
// 'scheduled' to 'canceled' (canonical US spelling), setting canceled_by to
// the supplied actor (NULL when actor is nil). Used by the Gmail-style
// pre-fire delay revoke path (issue #290 / #291 wave-2): an
// approved-but-not-yet-fired execution can be revoked at $0 by flipping it
// to canceled before the scheduler fires the cloud SDK call.
//
// The 'scheduled' status is the only accepted source. A concurrent
// scheduler tick that already transitioned the row to 'approved' or
// 'running' causes zero rows to be affected and the method returns
// (false, currentStatus, nil) -- the caller maps that to a 410
// ("revocation window has closed") so the frontend can fall through to
// the post-execution Azure direct-cancel API path.
//
// Returns (true, StatusCanceled, nil) on success and (false, "", err) on a
// real DB error. Must be called inside a WithTx block so the suppression
// cleanup commits atomically with the status flip.
func (s *PostgresStore) CancelScheduledExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (canceled bool, currentStatus string, err error) {
	// StatusCanceled is the canonical US-spelling value; see types.go.
	q := `
		UPDATE purchase_executions
		   SET status       = $3,
		       canceled_by  = $2,
		       updated_at   = NOW()
		 WHERE execution_id = $1
		   AND status = 'scheduled'
		RETURNING status
	`
	rows, err := tx.Query(ctx, q, executionID, cancelledBy, StatusCanceled)
	if err != nil {
		return false, "", fmt.Errorf("failed to cancel scheduled execution: %w", err)
	}
	defer rows.Close()

	if rows.Next() {
		var st string
		if scanErr := rows.Scan(&st); scanErr != nil {
			return false, "", fmt.Errorf("failed to scan cancel-scheduled result: %w", scanErr)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return false, "", fmt.Errorf("failed to iterate cancel-scheduled result: %w", rowsErr)
		}
		return true, st, nil
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return false, "", fmt.Errorf("failed to iterate cancel-scheduled result: %w", rowsErr)
	}

	// Zero rows affected: execution either does not exist or the scheduler
	// has already transitioned it out of 'scheduled'. Surface the current
	// status so the caller can return a meaningful 410 body.
	existing, existErr := s.GetExecutionByID(ctx, executionID)
	if existErr != nil {
		return false, "", fmt.Errorf("execution not found or db error: %w", existErr)
	}
	return false, existing.Status, nil
}

// GetExecutionsByStatuses returns executions whose Status is any of the
// supplied values, newest-first, capped at `limit`. Used by the History
// handler to merge pending/failed/expired rows alongside completed purchases
// without changing the narrower GetPendingExecutions contract the scheduler
// depends on.
//
// Pending rows dated in the future are the one exception to newest-first
// (issue #680): plan creation writes every ramp step up front as a pending
// row, up to 52 per plan, so about 20 plans fill MaxListLimit with steps
// months ahead and a plain scheduled_date DESC would cut off a pending
// approval due today. Those future pending steps sort after every other row,
// and among themselves soonest first, so the cap drops the farthest steps.
// No other status is affected: a notified, scheduled or in-flight row is
// actionable whatever its date, and terminal statuses never match the
// pending predicate, so their order is unchanged.
//
// Executions with status 'completed' and an empty error are excluded: they are
// already represented by their purchase_history rows, so returning them would
// only consume the cap (the History handler skips them anyway).
func (s *PostgresStore) GetExecutionsByStatuses(ctx context.Context, statuses []string, limit int) ([]PurchaseExecution, error) {
	return s.GetExecutionsByStatusesFiltered(ctx, ExecutionsByStatusesQuery{Statuses: statuses, Limit: limit})
}

// GetExecutionsByStatusesFiltered is GetExecutionsByStatuses with the History
// filters and account scope in q applied in SQL, before the limit (issue
// #632). Order and the clean-completed exclusion are GetExecutionsByStatuses'.
func (s *PostgresStore) GetExecutionsByStatusesFiltered(ctx context.Context, q ExecutionsByStatusesQuery) ([]PurchaseExecution, error) {
	if len(q.Statuses) == 0 {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	limit = min(limit, MaxListLimit)
	conds, args := executionsByStatusesWhere(&q)
	args = append(args, limit)
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
		WHERE ` + strings.Join(conds, "\n\t\t  AND ") + `
		ORDER BY (status = 'pending' AND scheduled_date > now()) ASC,
		         CASE WHEN status = 'pending' AND scheduled_date > now() THEN scheduled_date END ASC,
		         scheduled_date DESC
		LIMIT $` + fmt.Sprint(len(args))
	return s.queryExecutions(ctx, query, args...)
}

// CountExecutionsByPlanAndStatus returns, keyed by plan ID, the exact number
// of executions in each of the supplied statuses that last changed state at
// or after `since`.
//
// Aggregated with GROUP BY rather than counted from a GetExecutionsByStatuses
// page: that method's DESC + LIMIT truncation would silently understate any
// plan whose executions fall outside the newest `limit` rows, which for the
// plan health score means an unhealthy plan quietly renders as healthy once
// newer rows from other plans push its failures out of the window. The result
// set here is bounded by plans x statuses, not by execution volume, so it
// needs no limit of its own.
//
// The window is on updated_at, NOT scheduled_date, because for the terminal
// statuses this method exists to count those are different dates pointing in
// opposite directions. A plan's executions are created up front for the whole
// ramp, so a pending row carries a scheduled_date months in the FUTURE;
// canceling it (CancelExecutionAtomic) leaves that future date untouched.
// Windowing on scheduled_date would therefore count a purchase cancelled
// today under a date next year, and answer "how many rows are scheduled
// recently-or-later" instead of "how many changed state recently".
// updated_at is stamped by CancelExecutionAtomic and
// TransitionExecutionStatus, and backstopped by the
// update_purchase_executions_updated_at trigger, so it is when the row
// actually entered the status being counted.
//
// plan_id has been nullable since migration 000033 (direct-execute purchases
// from the Recommendations page have no originating plan, and deleting a plan
// SET NULLs its executions). Those rows belong to no plan and are excluded in
// SQL; the scan still goes through sql.NullString so a NULL can never turn a
// per-plan count into a scan error that blanks the score for every plan.
func (s *PostgresStore) CountExecutionsByPlanAndStatus(ctx context.Context, statuses []string, since time.Time) (map[string]ExecutionStatusCounts, error) {
	counts := make(map[string]ExecutionStatusCounts)
	if len(statuses) == 0 {
		return counts, nil
	}

	rows, err := s.db.Query(ctx, `
		SELECT plan_id, status, COUNT(*)
		FROM purchase_executions
		WHERE status = ANY($1)
		  AND plan_id IS NOT NULL
		  AND updated_at >= $2
		GROUP BY plan_id, status
	`, statuses, since)
	if err != nil {
		return nil, fmt.Errorf("failed to count executions by plan and status: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var planID sql.NullString
		var status string
		var n int
		if scanErr := rows.Scan(&planID, &status, &n); scanErr != nil {
			return nil, fmt.Errorf("failed to scan execution status count: %w", scanErr)
		}
		if !planID.Valid {
			continue
		}
		if counts[planID.String] == nil {
			counts[planID.String] = ExecutionStatusCounts{}
		}
		counts[planID.String][status] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate execution status counts: %w", err)
	}
	return counts, nil
}

// GetPlannedExecutions returns executions whose Status is any of the supplied
// values, ordered by scheduled_date ASC (soonest first), capped at `limit`.
// Used by the Planned Purchases handler where the user expects to act on
// imminent rows first.
//
// Distinct from GetExecutionsByStatuses (DESC + LIMIT for History's
// newest-first semantics): when total rows exceed `limit`, a DESC truncation
// drops the soonest rows, exactly the ones this list must surface. Sorting
// the already-truncated subset in-memory cannot recover them.
//
// Secondary sort by id ASC keeps ordering stable when multiple rows share a
// scheduled_date. NULLS LAST is defensive: the schema makes scheduled_date
// NOT NULL today, but the clause guards against a future relaxation silently
// hiding rows at the top of the list.
func (s *PostgresStore) GetPlannedExecutions(ctx context.Context, statuses []string, limit int) ([]PurchaseExecution, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
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
		WHERE status = ANY($1)
		ORDER BY scheduled_date ASC NULLS LAST, id ASC
		LIMIT $2
	`
	return s.queryExecutions(ctx, query, statuses, limit)
}

// GetStaleApprovedExecutions returns executions stuck in the "approved" status
// whose last update is older than olderThan. These are executions that were
// flipped to "approved" by ApproveAndExecute but whose synchronous purchase run
// never finalized (Lambda timeout, cold-start eviction, panic) — issue #632.
// updated_at is stamped to NOW() at the moment of the approved transition (see
// TransitionExecutionStatus) and is not touched again unless the run finalizes,
// so it is the age of the strand. Mirrors GetStaleProcessingExchanges.
func (s *PostgresStore) GetStaleApprovedExecutions(ctx context.Context, olderThan time.Duration) ([]PurchaseExecution, error) {
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
		WHERE status = 'approved' AND updated_at < NOW() - $1::interval
	`
	return s.queryExecutions(ctx, query, fmt.Sprintf("%d seconds", int(olderThan.Seconds())))
}

// ListStuckExecutions returns purchase executions whose Status is any of the
// supplied values and whose updated_at is older than the given duration. Used
// by the reaper sweep (issue #678) to find executions stuck in
// approved/running long enough that the synchronous executor has clearly
// failed without flipping the row to a terminal state.
//
// Returns rows oldest-first (ORDER BY updated_at ASC) so the longest-stuck
// rows are processed first within a single sweep, capped at MaxListLimit so
// an unbounded backlog doesn't blow up the Lambda's memory budget. The reaper
// invokes the sweep periodically; a backlog larger than MaxListLimit just
// gets drained across successive invocations.
//
// olderThan must be > 0; a zero/negative value would invert the WHERE clause
// into "updated_at < NOW() + |olderThan|" and reap fresh rows. Defense-in-
// depth: the caller (ParseReapAfterFromEnv) also rejects non-positive env
// values.
//
// olderThan is passed as a Postgres interval (seconds) so the comparison
// happens server-side against NOW() — keeping the cutoff in the DB clock
// avoids any drift between the API process and the database.
func (s *PostgresStore) ListStuckExecutions(ctx context.Context, statuses []string, olderThan time.Duration) ([]PurchaseExecution, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	if olderThan <= 0 {
		return nil, fmt.Errorf("ListStuckExecutions: olderThan must be > 0, got %s", olderThan)
	}
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
		WHERE status = ANY($1)
		  AND updated_at < NOW() - $2::interval
		ORDER BY updated_at ASC
		LIMIT $3
	`
	intervalArg := fmt.Sprintf("%d seconds", int(olderThan.Seconds()))
	return s.queryExecutions(ctx, query, statuses, intervalArg, MaxListLimit)
}

// GetPendingExecutions retrieves all pending purchase executions.
func (s *PostgresStore) GetPendingExecutions(ctx context.Context) ([]PurchaseExecution, error) {
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
		WHERE status IN ('pending', 'notified')
		  AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY scheduled_date ASC
		LIMIT 1000
	`

	return s.queryExecutions(ctx, query)
}

// planStepProviderLockPrefix namespaces the per-provider plan-step advisory lock.
const planStepProviderLockPrefix = "cudly:planstep:"

// planStepLockKeys returns the advisory-lock keys for providers: validated,
// de-duplicated and sorted. Sorted acquisition keeps two callers with
// overlapping provider sets from deadlocking on each other.
func planStepLockKeys(providers []string) ([]string, error) {
	if len(providers) == 0 {
		return nil, errors.New("lock plan providers: no providers given")
	}
	keys := make([]string, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, p := range providers {
		if p == "" {
			return nil, errors.New("lock plan providers: empty provider name")
		}
		if _, dup := seen[p]; !dup {
			seen[p] = struct{}{}
			keys = append(keys, planStepProviderLockPrefix+p)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// LockPlanProvidersTx implements StoreInterface.LockPlanProvidersTx.
func (s *PostgresStore) LockPlanProvidersTx(ctx context.Context, tx pgx.Tx, providers []string) error {
	keys, err := planStepLockKeys(providers)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", key); err != nil {
			return fmt.Errorf("lock plan step key %s: %w", key, err)
		}
	}
	return nil
}

// submitDedupeStatuses are the execution states that still stand for a
// commitment the submitter asked for: awaiting approval, being bought, or
// bought. A failed, expired or canceled row bought nothing, so a resubmit
// after one of those must create a fresh execution.
const submitDedupeStatuses = "'pending', 'notified', 'approved', 'running', 'completed', 'partially_completed'"

// ListRecentSubmitsTx serializes concurrent submits by creatorID and returns
// that creator's web-submitted executions scheduled at or after since, in
// every state in submitDedupeStatuses. The transaction-scoped advisory lock is
// held until tx ends, so a second submit by the same creator waits here, then
// reads the first one's committed row and can collapse onto it. SELECT ...
// FOR UPDATE could not do this: it locks only rows that already exist, so two
// concurrent submits both saw nothing and both inserted. The status set is
// wider than pending/notified because a direct execute moves its row to
// approved/running/completed within seconds, and a retry arriving then must
// still be recognized. An empty creatorID matches rows with no creator.
func (s *PostgresStore) ListRecentSubmitsTx(ctx context.Context, tx pgx.Tx, creatorID string, since time.Time) ([]PurchaseExecution, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "purchase-submit:"+creatorID); err != nil {
		return nil, fmt.Errorf("failed to lock purchase submits: %w", err)
	}
	var creator *string
	if creatorID != "" {
		creator = &creatorID
	}
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
		WHERE source = $3
		  AND status IN (` + submitDedupeStatuses + `)
		  AND scheduled_date >= $1
		  AND created_by_user_id IS NOT DISTINCT FROM $2::uuid
		ORDER BY scheduled_date ASC
	`
	rows, err := tx.Query(ctx, query, since, creator, common.PurchaseSourceWeb)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent submits in tx: %w", err)
	}
	defer rows.Close()
	return scanExecutionRows(rows)
}

// GetExecutionByID retrieves a purchase execution by execution ID.
// Returns an error wrapping ErrNotFound when no row matches executionID so
// callers can cleanly distinguish "not found" (errors.Is(err, ErrNotFound))
// from a real DB failure (any other non-nil error). A nil error guarantees
// a non-nil execution (fail-loud contract; issues #976, #1339).
func (s *PostgresStore) GetExecutionByID(ctx context.Context, executionID string) (*PurchaseExecution, error) {
	return getExecutionByIDOn(ctx, s.db, executionID)
}

// getExecutionByIDOn is GetExecutionByID on q, the pool or a transaction.
func getExecutionByIDOn(ctx context.Context, q executionQuerier, executionID string) (*PurchaseExecution, error) {
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
		WHERE execution_id = $1
	`

	executions, err := queryExecutionsOn(ctx, q, query, executionID)
	if isInvalidTextRepresentation(err) {
		// execution_id is a UUID column, and Postgres rejects an ID it cannot
		// cast instead of matching no row. No execution can have such an ID,
		// so it is not found. Callers such as the purchase revoke route look up
		// IDs that may be an Azure ARM path and fall through on ErrNotFound.
		return nil, fmt.Errorf("%w: execution %s", ErrNotFound, executionID)
	}
	if err != nil {
		return nil, err
	}

	if len(executions) == 0 {
		return nil, fmt.Errorf("%w: execution %s", ErrNotFound, executionID)
	}

	return &executions[0], nil
}

// pgInvalidTextRepresentation is the SQLSTATE Postgres raises when a text
// value cannot be cast to the column type, for example a non-UUID string
// compared with a uuid column.
const pgInvalidTextRepresentation = "22P02"

// isInvalidTextRepresentation reports whether err is Postgres refusing a text value
// for a typed column (SQLSTATE 22P02).
func isInvalidTextRepresentation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgInvalidTextRepresentation
}

// GetExecutionByPlanAndDate retrieves execution for a specific plan and date.
func (s *PostgresStore) GetExecutionByPlanAndDate(ctx context.Context, planID string, scheduledDate time.Time) (*PurchaseExecution, error) {
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
		WHERE plan_id = $1 AND scheduled_date = $2
	`

	executions, err := s.queryExecutions(ctx, query, planID, scheduledDate)
	if err != nil {
		return nil, err
	}

	if len(executions) == 0 {
		return nil, fmt.Errorf("%w: plan %s at %v", ErrNotFound, planID, scheduledDate)
	}

	return &executions[0], nil
}

// GetUserEmailByID resolves the email address of the auth user identified by
// userID. The `users` table belongs to internal/auth's schema, but internal/
// auth already imports internal/config (service_mfa.go), so internal/config
// cannot import internal/auth back without a cycle. This method queries the
// shared database directly by table/column name instead, returning a plain
// string so no auth.User type crosses the package boundary. Returns ("", nil)
// when userID does not resolve to a row -- callers must treat that as
// "identity unresolved," not as a legitimately blank email.
func (s *PostgresStore) GetUserEmailByID(ctx context.Context, userID string) (string, error) {
	var email string
	err := s.db.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("failed to get user email: %w", err)
	}
	return email, nil
}

// CountPendingExecutionsForAccount returns the number of pending/notified
// purchase executions still referencing this cloud account. The deleteAccount
// handler calls this before issuing DELETE FROM cloud_accounts so it can
// short-circuit with a 409 instead of letting migration 000053's ON DELETE
// RESTRICT bubble up as an opaque FK-violation error. See issue #606.
func (s *PostgresStore) CountPendingExecutionsForAccount(ctx context.Context, accountID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM purchase_executions
		WHERE cloud_account_id = $1
		  AND status IN ('pending', 'notified')
	`, accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count pending executions for account: %w", err)
	}
	return n, nil
}

// CountLiveExecutionsForAccount returns the number of purchase executions
// referencing this cloud account whose status is not in
// TerminalExecutionStatuses: rows an executor, scheduler or reaper may still
// act on, which need the account to exist. DeleteCloudAccount detaches every
// other row, so these are exactly the rows that block the delete (issue #265).
// It includes the pending/notified rows CountPendingExecutionsForAccount
// counts.
func (s *PostgresStore) CountLiveExecutionsForAccount(ctx context.Context, accountID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM purchase_executions
		WHERE cloud_account_id = $1
		  AND status <> ALL($2)
	`, accountID, TerminalExecutionStatuses).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count live executions for account: %w", err)
	}
	return n, nil
}

// ListPendingExecutionIDsForAccount returns the execution IDs that the
// frontend's Cancel-All-Then-Delete flow needs to POST cancel for. Capped
// at 1000 rows — a single account with more pending executions than that
// is an unusual operator-cleanup task rather than a button-click flow.
func (s *PostgresStore) ListPendingExecutionIDsForAccount(ctx context.Context, accountID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT execution_id FROM purchase_executions
		WHERE cloud_account_id = $1
		  AND status IN ('pending', 'notified')
		ORDER BY scheduled_date ASC
		LIMIT 1000
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pending execution ids for account: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan pending execution id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate pending execution ids: %w", err)
	}
	return ids, nil
}

// executionQuerier is the query method shared by the pool (dbConn) and a
// pgx.Tx, so the execution read and transition helpers can run on either.
type executionQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// queryExecutions is a helper to query and scan purchase executions.
func (s *PostgresStore) queryExecutions(ctx context.Context, query string, args ...any) ([]PurchaseExecution, error) {
	return queryExecutionsOn(ctx, s.db, query, args...)
}

// queryExecutionsOn is queryExecutions on q, the pool or a transaction.
func queryExecutionsOn(ctx context.Context, q executionQuerier, query string, args ...any) ([]PurchaseExecution, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query executions: %w", err)
	}
	defer rows.Close()
	return scanExecutionRows(rows)
}

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

// ==========================================
// HELPER FUNCTIONS
// ==========================================

// timeFromTTL converts a Unix timestamp (TTL) to a nullable time.Time.
func timeFromTTL(ttl int64) any {
	if ttl == 0 {
		return nil
	}
	t := time.Unix(ttl, 0)
	return &t
}

// ttlFromTime converts a time.Time to Unix timestamp.
func ttlFromTime(t time.Time) int64 {
	return t.Unix()
}

// nullStringFromString converts a string to sql.NullString.
func nullStringFromString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// nullPtrFromNullString converts a scanned sql.NullString to a *string for
// optional pointer fields. Returns nil when the DB value is NULL.
func nullPtrFromNullString(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	s := ns.String
	return &s
}

func nullPtrFromNullFloat64(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	return &n.Float64
}
