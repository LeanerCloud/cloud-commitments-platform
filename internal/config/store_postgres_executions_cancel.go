package config

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

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
	// StatusCanceled is the canonical US-spelling value; see types_executions.go.
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
	// StatusCanceled is the canonical US-spelling value; see types_executions.go.
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
