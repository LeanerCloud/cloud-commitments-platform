package config

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

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
