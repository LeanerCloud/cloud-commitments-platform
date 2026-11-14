package config

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/jackc/pgx/v5"
)

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
