package config

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TransitionRIExchangeStatus atomically transitions an RI exchange record status.
// Uses a single UPDATE...WHERE...RETURNING for atomicity, then diagnoses failure
// only if zero rows are returned (ErrRIExchangeExpired or
// ErrRIExchangeNotInExpectedStatus, wrapped, for an existing record).
// actor is the UUID of the user performing the transition (nil for system-initiated paths).
func (s *PostgresStore) TransitionRIExchangeStatus(ctx context.Context, id, fromStatus, toStatus string, actor *string) (*RIExchangeRecord, error) {
	query := `
		UPDATE ri_exchange_history
		SET status = $3, updated_at = NOW(),
		    transitioned_by = $4, transitioned_at = NOW()
		WHERE id = $1 AND status = $2 AND (expires_at IS NULL OR expires_at > NOW())
		RETURNING id, account_id, exchange_id, region, source_ri_ids,
		          source_instance_type, source_count, target_offering_id,
		          target_instance_type, target_count, payment_due::text,
		          status, approval_token_hash, error, mode,
		          created_at, updated_at, completed_at, expires_at,
		          created_by_user_id, approved_by, ladder_run_id
	`

	records, err := s.queryRIExchangeRecords(ctx, query, id, fromStatus, toStatus, actor)
	if err != nil {
		return nil, err
	}

	if len(records) == 0 {
		// Diagnose: not found vs wrong status vs expired.
		return nil, s.diagnoseTransitionFailure(ctx, id, fromStatus)
	}

	return &records[0], nil
}

// diagnoseTransitionFailure determines why a status transition returned zero rows.
func (s *PostgresStore) diagnoseTransitionFailure(ctx context.Context, id, fromStatus string) error {
	var currentStatus string
	var expired bool
	err := s.db.QueryRow(ctx,
		`SELECT status, (expires_at IS NOT NULL AND expires_at <= NOW()) FROM ri_exchange_history WHERE id = $1`, id,
	).Scan(&currentStatus, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("ri exchange record not found: %s", id)
	}
	if err != nil {
		return fmt.Errorf("failed to diagnose transition failure: %w", err)
	}
	if expired {
		return fmt.Errorf("%w: %s", ErrRIExchangeExpired, id)
	}
	return fmt.Errorf("%w: expected status %q but current status is %q", ErrRIExchangeNotInExpectedStatus, fromStatus, currentStatus)
}

// CompleteRIExchange marks an RI exchange as completed.
func (s *PostgresStore) CompleteRIExchange(ctx context.Context, id, exchangeID string) error {
	query := `
		UPDATE ri_exchange_history
		SET status = 'completed', exchange_id = $2, completed_at = NOW()
		WHERE id = $1
	`

	result, err := s.db.Exec(ctx, query, id, exchangeID)
	if err != nil {
		return fmt.Errorf("failed to complete ri exchange: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("ri exchange record not found: %s", id)
	}

	return nil
}

// CompleteRIExchangeWithPayment marks an RI exchange as completed and updates
// payment_due to the amount AWS actually accepted. Use this on the
// manual-approval path instead of CompleteRIExchange so the daily-spend ledger
// (GetRIExchangeDailySpend) reflects the accepted amount, not the stale
// pre-execution quote (H3 fix).
func (s *PostgresStore) CompleteRIExchangeWithPayment(ctx context.Context, id, exchangeID, acceptedPaymentDue string) error {
	query := `
		UPDATE ri_exchange_history
		SET status = 'completed', exchange_id = $2, completed_at = NOW(), payment_due = $3
		WHERE id = $1
	`

	result, err := s.db.Exec(ctx, query, id, exchangeID, acceptedPaymentDue)
	if err != nil {
		return fmt.Errorf("failed to complete ri exchange with payment: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("ri exchange record not found: %s", id)
	}

	return nil
}

// StampRIExchangeApprovedBy sets the approved_by column on an RI exchange row
// (issue #300). Called after CompleteRIExchangeWithPayment when approval came
// from a session-authed user. The stamping is best-effort (log + continue on
// failure so the exchange itself isn't rolled back just because the audit stamp
// failed).
func (s *PostgresStore) StampRIExchangeApprovedBy(ctx context.Context, id, approverEmail string) error {
	query := `
		UPDATE ri_exchange_history
		SET approved_by = $2
		WHERE id = $1
	`

	result, err := s.db.Exec(ctx, query, id, approverEmail)
	if err != nil {
		return fmt.Errorf("failed to stamp approved_by on ri exchange %s: %w", id, err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("ri exchange record not found when stamping approved_by: %s", id)
	}
	return nil
}

// FailRIExchange marks an RI exchange as failed.
func (s *PostgresStore) FailRIExchange(ctx context.Context, id, errorMsg string) error {
	query := `
		UPDATE ri_exchange_history
		SET status = 'failed', error = $2
		WHERE id = $1
	`

	result, err := s.db.Exec(ctx, query, id, errorMsg)
	if err != nil {
		return fmt.Errorf("failed to fail ri exchange: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("ri exchange record not found: %s", id)
	}

	return nil
}

// NoteRIExchangeOutcomeUnknown records why a processing exchange was left
// processing: AWS may have accepted it (#1050). It touches only a row that is
// still processing, so it never rewrites a settled one.
func (s *PostgresStore) NoteRIExchangeOutcomeUnknown(ctx context.Context, id, note string) error {
	result, err := s.db.Exec(ctx,
		`UPDATE ri_exchange_history SET error = $2, updated_at = NOW() WHERE id = $1 AND status = 'processing'`, id, note)
	if err != nil {
		return fmt.Errorf("failed to note ri exchange outcome unknown: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("ri exchange %s is not processing", id)
	}
	return nil
}
