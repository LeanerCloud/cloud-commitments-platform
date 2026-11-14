package config

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ReserveRIExchange atomically records the full execution ceiling before a provider call.
func (s *PostgresStore) ReserveRIExchange(ctx context.Context, record *RIExchangeRecord, dailyCapUSD, perExchangeCapUSD string) (string, error) {
	initial, dailyCap, perCap, err := riExchangeReservationMicros(record, dailyCapUSD, perExchangeCapUSD)
	if err != nil {
		return "", err
	}

	candidate := *record
	var ceiling string
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		var reserveErr error
		ceiling, reserveErr = reserveRIExchangeCeiling(ctx, tx, nil, record.PaymentDue, initial, dailyCap, perCap, dailyCapUSD)
		if reserveErr != nil {
			return reserveErr
		}
		if guardErr := refuseRIExchangeSourcesInFlight(ctx, tx, nil, record.SourceRIIDs); guardErr != nil {
			return guardErr
		}
		candidate.PaymentDue = ceiling
		return saveRIExchangeRecord(ctx, tx, &candidate)
	})
	if err != nil {
		return "", err
	}
	*record = candidate
	return ceiling, nil
}

// ReserveApprovedRIExchange reserves the full execution ceiling for an approved
// exchange whose row is already processing. It runs under the same global
// advisory lock as ReserveRIExchange, so manual approvals and AUTO exchanges
// share one headroom protocol, and it leaves the row's own amount out of the
// spend it checks against. The row's payment_due becomes the reserved ceiling
// until CompleteRIExchangeWithPayment settles the accepted amount.
func (s *PostgresStore) ReserveApprovedRIExchange(ctx context.Context, id, dailyCapUSD, perExchangeCapUSD string) (string, error) {
	dailyCap, err := riExchangeMicros(dailyCapUSD)
	if err != nil {
		return "", fmt.Errorf("%w: invalid daily RI exchange cap: %w", ErrRIExchangeReservationInvalid, err)
	}
	perCap, err := riExchangeMicros(perExchangeCapUSD)
	if err != nil {
		return "", fmt.Errorf("%w: invalid per-exchange RI cap: %w", ErrRIExchangeReservationInvalid, err)
	}

	var ceiling string
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		var initialText string
		var sources []string
		scanErr := tx.QueryRow(ctx,
			`SELECT payment_due::text, source_ri_ids FROM ri_exchange_history WHERE id = $1 AND status = 'processing' FOR UPDATE`, id,
		).Scan(&initialText, &sources)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return fmt.Errorf("%w: ri exchange %s is not processing", ErrRIExchangeReservationInvalid, id)
		}
		if scanErr != nil {
			return fmt.Errorf("failed to read RI exchange %s: %w", id, scanErr)
		}
		initial, parseErr := riExchangeMicros(initialText)
		if parseErr != nil {
			return fmt.Errorf("%w: invalid initial RI exchange payment: %w", ErrRIExchangeReservationInvalid, parseErr)
		}
		var reserveErr error
		ceiling, reserveErr = reserveRIExchangeCeiling(ctx, tx, &id, initialText, initial, dailyCap, perCap, dailyCapUSD)
		if reserveErr != nil {
			return reserveErr
		}
		if guardErr := refuseRIExchangeSourcesInFlight(ctx, tx, &id, sources); guardErr != nil {
			return guardErr
		}
		_, updErr := tx.Exec(ctx,
			`UPDATE ri_exchange_history SET payment_due = $2::numeric, updated_at = NOW() WHERE id = $1`, id, ceiling)
		if updErr != nil {
			return fmt.Errorf("failed to record RI exchange reservation: %w", updErr)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return ceiling, nil
}
