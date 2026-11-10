package config

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// retryFamilyLockKey is the advisory lock key of one fan-out key family.
func retryFamilyLockKey(family string) string {
	return "retry-family:" + family
}

// LockRetryFamilyTx takes a transaction-scoped advisory lock on a fan-out key
// family, so a retry of the root and a retry of one of its account rows
// serialize: the second one then sees the first one's committed link. It is
// taken after the submit and ladder tranche locks and before any row write of
// the retry, and nothing else takes it, so it cannot deadlock. The caller's
// transaction must already be READ COMMITTED (the default) so the claim read
// after the wait sees the winner's commit.
func (s *PostgresStore) LockRetryFamilyTx(ctx context.Context, tx pgx.Tx, family string) error {
	if family == "" {
		return fmt.Errorf("lock retry family: empty family")
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL lock_timeout = %d", ladderLockTimeout.Milliseconds())); err != nil {
		return fmt.Errorf("lock retry family: set lock_timeout: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, retryFamilyLockKey(family)); err != nil {
		return fmt.Errorf("lock retry family %q: %w", family, err)
	}
	// The timeout was for the advisory wait only; do not leak it into later statements.
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout TO DEFAULT"); err != nil {
		return fmt.Errorf("lock retry family: reset lock_timeout: %w", err)
	}
	return nil
}

// FamilyRetryClaimedTx reports whether a row of the opposite kind in the same
// plan and key family already has a retry successor: an account row when
// selfIsRoot, the root when not. Same-kind siblings never count, and neither
// does selfID. A fan-out root and its account rows are linked only by the key
// family (the root key K and the account keys K:<account>), and each has its
// own retry link, so the per-row CAS alone lets both kinds be retried. Call it
// under LockRetryFamilyTx.
func (s *PostgresStore) FamilyRetryClaimedTx(ctx context.Context, tx pgx.Tx, family, planID, selfID string, selfIsRoot bool) (bool, error) {
	counterpart := "cloud_account_id IS NULL"
	if selfIsRoot {
		counterpart = "cloud_account_id IS NOT NULL"
	}
	q := `
		SELECT EXISTS (
			SELECT 1 FROM purchase_executions
			 WHERE split_part(COALESCE(NULLIF(idempotency_key, ''), execution_id::text), ':', 1) = $1
			   AND plan_id = $2::uuid
			   AND execution_id <> $3::uuid
			   AND retry_execution_id IS NOT NULL
			   AND ` + counterpart + `
		)`
	var claimed bool
	if err := tx.QueryRow(ctx, q, family, planID, selfID).Scan(&claimed); err != nil {
		return false, fmt.Errorf("check retry family %q: %w", family, err)
	}
	return claimed, nil
}
