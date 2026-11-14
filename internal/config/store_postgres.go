package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

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
