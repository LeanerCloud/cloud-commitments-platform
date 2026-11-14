package config

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// GetRIExchangeRecord retrieves an RI exchange record by ID. A missing record
// is a wrapped ErrNotFound, so callers can tell it from a failed query and
// answer it as a client error rather than a 500 (issue #601).
func (s *PostgresStore) GetRIExchangeRecord(ctx context.Context, id string) (*RIExchangeRecord, error) {
	query := `
		SELECT id, account_id, exchange_id, region, source_ri_ids,
		       source_instance_type, source_count, target_offering_id,
		       target_instance_type, target_count, payment_due::text,
		       status, approval_token_hash, error, mode,
		       created_at, updated_at, completed_at, expires_at,
		       created_by_user_id, approved_by, ladder_run_id
		FROM ri_exchange_history
		WHERE id = $1
	`

	records, err := s.queryRIExchangeRecords(ctx, query, id)
	if err != nil {
		return nil, err
	}

	if len(records) == 0 {
		return nil, fmt.Errorf("ri exchange record %s: %w", id, ErrNotFound)
	}

	return &records[0], nil
}

// GetRIExchangeRecordByToken retrieves an RI exchange record by approval
// token. token is the raw, caller-supplied value; only its hash is stored
// (approval_token_hash, issue #103), so the lookup hashes it first.
func (s *PostgresStore) GetRIExchangeRecordByToken(ctx context.Context, token string) (*RIExchangeRecord, error) {
	query := `
		SELECT id, account_id, exchange_id, region, source_ri_ids,
		       source_instance_type, source_count, target_offering_id,
		       target_instance_type, target_count, payment_due::text,
		       status, approval_token_hash, error, mode,
		       created_at, updated_at, completed_at, expires_at,
		       created_by_user_id, approved_by, ladder_run_id
		FROM ri_exchange_history
		WHERE approval_token_hash = $1
	`

	records, err := s.queryRIExchangeRecords(ctx, query, HashApprovalToken(token))
	if err != nil {
		return nil, err
	}

	if len(records) == 0 {
		return nil, fmt.Errorf("ri exchange record not found for token")
	}

	return &records[0], nil
}

// GetRIExchangeHistory retrieves RI exchange history records.
func (s *PostgresStore) GetRIExchangeHistory(ctx context.Context, since time.Time, limit int) ([]RIExchangeRecord, error) {
	query := `
		SELECT id, account_id, exchange_id, region, source_ri_ids,
		       source_instance_type, source_count, target_offering_id,
		       target_instance_type, target_count, payment_due::text,
		       status, approval_token_hash, error, mode,
		       created_at, updated_at, completed_at, expires_at,
		       created_by_user_id, approved_by, ladder_run_id
		FROM ri_exchange_history
		WHERE created_at >= $1
		ORDER BY created_at DESC
		LIMIT $2
	`

	return s.queryRIExchangeRecords(ctx, query, since, limit)
}

// GetRIExchangeHistoryForAccounts retrieves RI exchange history records for
// the given account ids only. The account predicate runs in SQL, before the
// LIMIT, so a scoped caller gets up to limit of their own records rather
// than whatever survives a filter applied to the newest limit records of
// every account (issue #308). An empty or nil accountIDs matches no records:
// "= ANY('{}')" is false and "= ANY(NULL)" is NULL for every row.
func (s *PostgresStore) GetRIExchangeHistoryForAccounts(ctx context.Context, since time.Time, limit int, accountIDs []string) ([]RIExchangeRecord, error) {
	query := `
		SELECT id, account_id, exchange_id, region, source_ri_ids,
		       source_instance_type, source_count, target_offering_id,
		       target_instance_type, target_count, payment_due::text,
		       status, approval_token_hash, error, mode,
		       created_at, updated_at, completed_at, expires_at,
		       created_by_user_id, approved_by, ladder_run_id
		FROM ri_exchange_history
		WHERE created_at >= $1 AND account_id = ANY($3::text[])
		ORDER BY created_at DESC
		LIMIT $2
	`

	return s.queryRIExchangeRecords(ctx, query, since, limit, accountIDs)
}

// queryRIExchangeRecords is a helper to query and scan RI exchange records.
func (s *PostgresStore) queryRIExchangeRecords(ctx context.Context, query string, args ...any) ([]RIExchangeRecord, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query ri exchange records: %w", err)
	}
	defer rows.Close()

	records := make([]RIExchangeRecord, 0)
	for rows.Next() {
		var record RIExchangeRecord
		var approvalToken, errStr sql.NullString
		var completedAt, expiresAt sql.NullTime
		var createdByUserID, approvedBy, ladderRunID sql.NullString

		err := rows.Scan(
			&record.ID,
			&record.AccountID,
			&record.ExchangeID,
			&record.Region,
			&record.SourceRIIDs,
			&record.SourceInstanceType,
			&record.SourceCount,
			&record.TargetOfferingID,
			&record.TargetInstanceType,
			&record.TargetCount,
			&record.PaymentDue,
			&record.Status,
			&approvalToken,
			&errStr,
			&record.Mode,
			&record.CreatedAt,
			&record.UpdatedAt,
			&completedAt,
			&expiresAt,
			&createdByUserID,
			&approvedBy,
			&ladderRunID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan ri exchange record: %w", err)
		}

		if approvalToken.Valid {
			record.ApprovalToken = approvalToken.String
		}
		if errStr.Valid {
			record.Error = errStr.String
		}
		if completedAt.Valid {
			record.CompletedAt = &completedAt.Time
		}
		if expiresAt.Valid {
			record.ExpiresAt = &expiresAt.Time
		}
		record.CreatedByUserID = nullPtrFromNullString(createdByUserID)
		record.ApprovedBy = nullPtrFromNullString(approvedBy)
		record.LadderRunID = nullPtrFromNullString(ladderRunID)

		records = append(records, record)
	}

	return records, rows.Err()
}
