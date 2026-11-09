package config

import (
	"context"
	"fmt"
	"time"
)

// readoptRootPredicate is the row-level shape of an expired plan-step root that
// may be re-armed once (#1785), over purchase_executions aliased e. Shared by
// the list and the CAS so they cannot drift. $1 now, $2 ReadoptMaxExpiredAge in
// seconds, $3 requireDifferentApprover.
//
// The step must hold exactly this one row, so a canceled, failed or
// partially_completed root (which leaves a sibling or a purchase) is never
// adopted and the step cannot be bought twice. updated_at is the expiry time:
// ExpireExecutionAtomic stamps it and no production writer touches an expired
// row afterwards. With 4-eyes on, a root with no creator and no system flag has
// nobody who may approve it, so it does not burn the one shot.
const readoptRootPredicate = `
	e.plan_id IS NOT NULL
	AND e.status = 'expired'
	AND e.cloud_account_id IS NULL
	AND e.retry_attempt_n = 0
	AND e.retry_execution_id IS NULL
	AND e.ladder_run_id IS NULL
	AND e.approved_by IS NULL
	AND e.executed_at IS NULL
	AND e.completed_at IS NULL
	AND e.scheduled_execution_at IS NULL
	AND e.pre_approval_skip_reason IS NULL
	AND e.approval_readopted_at IS NULL
	AND e.updated_at >= $1::timestamptz - make_interval(secs => $2::double precision)
	AND (NOT $3::boolean OR e.created_by_user_id IS NOT NULL OR e.created_by_system)
	AND NOT EXISTS (
		SELECT 1 FROM purchase_executions o
		 WHERE o.plan_id = e.plan_id AND o.step_number = e.step_number
		   AND o.execution_id <> e.execution_id)`

// readoptableExpiredRootQuery lists the plans whose next ramp step holds a
// re-armable expired root. An auto-purchase plan qualifies only with 4-eyes on
// (defense in depth for purchase.planNeedsApproval): without it the re-armed
// row would auto-buy fresh recommendations with the email as the only veto.
// dates_managed plans are out of scope: their roots follow the schedule clock.
const readoptableExpiredRootQuery = `
	SELECT DISTINCT p.id
	  FROM purchase_plans p
	  JOIN purchase_executions e
	    ON e.plan_id = p.id
	   AND e.step_number = COALESCE((p.ramp_schedule ->> 'current_step')::int, 0) + 1
	 WHERE p.enabled
	   AND COALESCE(p.ramp_schedule ->> 'dates_managed', 'false') <> 'true'
	   AND (NOT p.auto_purchase OR $3::boolean)
	   AND ` + readoptRootPredicate

// ListPlansWithReadoptableExpiredRoot returns the IDs of the plans that may
// have an expired root to re-arm. It is only a trigger for the notification
// tick: the claim re-derives every condition under the plan and provider
// locks. It returns an empty set until migration 164 is applied.
func (s *PostgresStore) ListPlansWithReadoptableExpiredRoot(ctx context.Context, now time.Time, requireDifferentApprover bool) (map[string]struct{}, error) {
	ready, err := readoptSchemaReady(ctx, s.db)
	if err != nil {
		return nil, fmt.Errorf("failed to check the re-adoption schema: %w", err)
	}
	if !ready {
		return map[string]struct{}{}, nil
	}
	rows, err := s.db.Query(ctx, readoptableExpiredRootQuery, now, ReadoptMaxExpiredAge.Seconds(), requireDifferentApprover)
	if err != nil {
		return nil, fmt.Errorf("failed to query readoptable expired roots: %w", err)
	}
	defer rows.Close()

	plans := make(map[string]struct{})
	for rows.Next() {
		var planID string
		if err := rows.Scan(&planID); err != nil {
			return nil, fmt.Errorf("failed to scan readoptable expired root: %w", err)
		}
		plans[planID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read readoptable expired roots: %w", err)
	}
	return plans, nil
}
