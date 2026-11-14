package config

import (
	"context"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// GetRIExchangeDailySpend returns total payment_due for completed and in-flight
// (processing) exchanges on a given date (UTC).
//
// M5 fix: including 'processing' rows prevents a TOCTOU race where two
// concurrent approvals both read the same daily-spend total (before either
// exchange's ledger row is committed) and together exceed the daily cap.
// For 'completed' rows the time anchor is completed_at; for 'processing' rows
// it is updated_at (the moment the record transitioned to processing, i.e.
// when it was approved).
func (s *PostgresStore) GetRIExchangeDailySpend(ctx context.Context, date time.Time) (string, error) {
	query := riExchangeDailySpendQuery

	var total string
	err := s.db.QueryRow(ctx, query, date, nil).Scan(&total)
	if err != nil {
		return "", fmt.Errorf("failed to get ri exchange daily spend: %w", err)
	}

	return total, nil
}

// CancelAllPendingExchanges cancels all pending RI exchange records regardless
// of origin. Kept for interface compatibility; new callers should prefer
// CancelPendingExchangesByOrigin to avoid cross-origin contamination.
func (s *PostgresStore) CancelAllPendingExchanges(ctx context.Context) (int64, error) {
	query := `
		UPDATE ri_exchange_history
		SET status = $1, updated_at = NOW()
		WHERE status = 'pending'
	`

	result, err := s.db.Exec(ctx, query, StatusCanceled)
	if err != nil {
		return 0, fmt.Errorf("failed to cancel pending exchanges: %w", err)
	}

	return result.RowsAffected(), nil
}

// CancelPendingExchangesByOrigin cancels only pending records that match the
// given origin (gap G10 / issue #1348):
//   - common.ExchangeOriginStandalone: cancels WHERE ladder_run_id IS NULL
//   - common.ExchangeOriginLadder:     cancels WHERE ladder_run_id IS NOT NULL
//
// The origin is validated at this boundary; an unknown value fails loud rather
// than silently canceling the wrong partition on a money path.
//
// DELIBERATE COARSE PARTITION: the ExchangeOriginLadder branch cancels EVERY
// ladder-linked pending record (ladder_run_id IS NOT NULL) across ALL ladder
// runs and configs, not just the current run's. This is acceptable today
// because the ladder never creates pending exchange records: buildRIExchangeConfig
// forces Mode=auto, which completes or fails immediately without leaving a
// pending row. Per-run / per-config scoping needs an additional selection key
// (e.g. the specific ladder_run_id or config_id) and is tracked in TODO(#1367).
func (s *PostgresStore) CancelPendingExchangesByOrigin(ctx context.Context, origin common.ExchangeOrigin) (int64, error) {
	if err := origin.Validate(); err != nil {
		return 0, fmt.Errorf("CancelPendingExchangesByOrigin: %w", err)
	}

	var query string
	switch origin {
	case common.ExchangeOriginLadder:
		query = `
			UPDATE ri_exchange_history
			SET status = $1, updated_at = NOW()
			WHERE status = 'pending'
			  AND ladder_run_id IS NOT NULL
		`
	default: // common.ExchangeOriginStandalone (validated non-unknown above)
		query = `
			UPDATE ri_exchange_history
			SET status = $1, updated_at = NOW()
			WHERE status = 'pending'
			  AND ladder_run_id IS NULL
		`
	}

	result, err := s.db.Exec(ctx, query, StatusCanceled)
	if err != nil {
		return 0, fmt.Errorf("failed to cancel pending exchanges by origin %q: %w", origin, err)
	}

	return result.RowsAffected(), nil
}

// GetStaleProcessingExchanges returns processing exchanges older than the given duration.
func (s *PostgresStore) GetStaleProcessingExchanges(ctx context.Context, olderThan time.Duration) ([]RIExchangeRecord, error) {
	query := `
		SELECT id, account_id, exchange_id, region, source_ri_ids,
		       source_instance_type, source_count, target_offering_id,
		       target_instance_type, target_count, payment_due::text,
		       status, approval_token_hash, error, mode,
		       created_at, updated_at, completed_at, expires_at,
		       created_by_user_id, approved_by, ladder_run_id
		FROM ri_exchange_history
		WHERE status = 'processing' AND updated_at < NOW() - $1::interval
	`

	return s.queryRIExchangeRecords(ctx, query, fmt.Sprintf("%d seconds", int(olderThan.Seconds())))
}
