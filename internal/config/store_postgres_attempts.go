package config

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// uniquePurchasedReservationIndex is created by migration 000162: one
// purchased row per reservation_id across every token and execution.
const uniquePurchasedReservationIndex = "uq_purchase_attempts_purchased_reservation"

// PostgresStore is the platform's common.AttemptJournal (cloud-commitments-go#107).
var _ common.AttemptJournal = (*PostgresStore)(nil)

// PostgresStore also reads other lineages' purchase claims (go#700). Deleting
// ClaimedByOthers breaks the build instead of silently disabling the exclusion.
var _ common.ClaimReader = (*PostgresStore)(nil)

// recordAttemptSQL is one autocommit upsert. The WHERE clause is the
// transition table and mirrors validTransition in cloud-commitments-go's
// providers/aws/services/redshift journal test file (a cross-language mirror;
// change both together):
//
//	none        -> sending | purchased      (the INSERT path; the SELECT's
//	                                         WHERE refuses none -> outcome_unknown)
//	sending     -> purchased | outcome_unknown
//	outcome_unknown -> purchased
//	purchased   -> purchased, same reservation_id only
//
// Anything else (including identical sending or outcome_unknown repeats)
// updates no row and RecordAttempt reports an error. offering_id is
// write-once: the first non-NULL value survives every later write.
const recordAttemptSQL = `
INSERT INTO purchase_attempts (idempotency_token, execution_id, state, reservation_id, offering_id)
SELECT $1::text, $2::text, $3::text, NULLIF($4::text, ''), NULLIF($5::text, '')
WHERE $3::text <> 'outcome_unknown'
   OR EXISTS (SELECT 1 FROM purchase_attempts p WHERE p.idempotency_token = $1::text AND p.execution_id = $2::text)
ON CONFLICT (idempotency_token, execution_id) DO UPDATE SET
    state = EXCLUDED.state,
    reservation_id = EXCLUDED.reservation_id,
    offering_id = COALESCE(purchase_attempts.offering_id, EXCLUDED.offering_id),
    updated_at = now()
WHERE (purchase_attempts.state = 'sending' AND EXCLUDED.state IN ('purchased', 'outcome_unknown'))
   OR (purchase_attempts.state = 'outcome_unknown' AND EXCLUDED.state = 'purchased')
   OR (purchase_attempts.state = 'purchased' AND EXCLUDED.state = 'purchased'
       AND purchase_attempts.reservation_id = EXCLUDED.reservation_id)`

// RecordAttempt durably records one purchase attempt. It validates the record,
// then upserts in its own autocommit statement. It must not be called inside a
// caller's transaction: the record has to be committed before it returns.
func (s *PostgresStore) RecordAttempt(ctx context.Context, rec common.AttemptRecord) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, recordAttemptSQL,
		rec.IdempotencyToken, rec.ExecutionID, string(rec.State), rec.ReservationID, rec.OfferingID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == uniquePurchasedReservationIndex {
			return fmt.Errorf("record purchase attempt %s: reservation already recorded as purchased by another attempt",
				common.MaskToken(rec.IdempotencyToken))
		}
		return fmt.Errorf("record purchase attempt %s: %w", common.MaskToken(rec.IdempotencyToken), err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("record purchase attempt %s: transition to %q refused by the existing row",
			common.MaskToken(rec.IdempotencyToken), rec.State)
	}
	return nil
}

// PriorAttempts returns every attempt of the token across executions. An empty
// result is not an error. Like RecordAttempt it runs on its own connection.
func (s *PostgresStore) PriorAttempts(ctx context.Context, token string) ([]common.AttemptRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT idempotency_token, execution_id, state, COALESCE(reservation_id, ''), COALESCE(offering_id, '')
		FROM purchase_attempts
		WHERE idempotency_token = $1
		ORDER BY created_at, execution_id`, token)
	if err != nil {
		return nil, fmt.Errorf("list purchase attempts %s: %w", common.MaskToken(token), err)
	}
	recs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (common.AttemptRecord, error) {
		var r common.AttemptRecord
		var state string
		if scanErr := row.Scan(&r.IdempotencyToken, &r.ExecutionID, &state, &r.ReservationID, &r.OfferingID); scanErr != nil {
			return r, scanErr
		}
		r.State = common.AttemptState(state)
		return r, nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan purchase attempts %s: %w", common.MaskToken(token), err)
	}
	return recs, nil
}

// ClaimedByOthers returns the ids that an attempt under a token other than
// token recorded as purchased (go#700). It runs on its own pool connection,
// like RecordAttempt, and must not be called inside a caller's transaction.
func (s *PostgresStore) ClaimedByOthers(ctx context.Context, token string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT reservation_id
		FROM purchase_attempts
		-- state lets Postgres use the partial unique index; the table CHECK already limits reservation_id to purchased rows.
		WHERE state = 'purchased'
		  AND reservation_id = ANY($2)
		  AND idempotency_token <> $1`, token, ids)
	if err != nil {
		return nil, fmt.Errorf("list claimed reservations %s: %w", common.MaskToken(token), err)
	}
	claimed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scan claimed reservations %s: %w", common.MaskToken(token), err)
	}
	return claimed, nil
}
