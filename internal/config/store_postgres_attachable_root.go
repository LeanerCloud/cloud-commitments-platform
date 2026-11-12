package config

import (
	"context"
	"fmt"
	"time"
)

// EmptyRootAttachSlackDays is how many days past the notice window a plan's
// empty root is already offered to the notification tick: a root dated D is
// visited from D-(N+slack) days, so one missed daily tick no longer strands it.
const EmptyRootAttachSlackDays = 2

// attachableEmptyRootQuery finds, per plan, the next step's empty root (the
// row purchase.adoptableRootRow picks) whose OWN date is inside the attach
// window [$1 + N x 24h, $1 + (N + slack) x 24h). The lower bound is the attach
// gate (a root nearer than N days is left to the scheduler); the plan's
// next_execution_date is deliberately not read. The empty test and the UTC
// fixed-hours arithmetic match doomedNextRootQuery.
const attachableEmptyRootQuery = `
	SELECT DISTINCT p.id
	  FROM purchase_plans p
	  JOIN purchase_executions e
	    ON e.plan_id = p.id
	   AND e.step_number = COALESCE((p.ramp_schedule ->> 'current_step')::int, 0) + 1
	 WHERE e.cloud_account_id IS NULL
	   AND e.retry_attempt_n = 0
	   AND e.status IN ('pending', 'notified')
	   AND e.recommendations IN ('[]'::jsonb, 'null'::jsonb)
	   AND COALESCE(p.ramp_schedule ->> 'dates_managed', 'false') <> 'true'
	   AND p.notification_days_before >= 1
	   AND e.scheduled_date >= $1::timestamptz + make_interval(hours => 24 * p.notification_days_before)
	   AND e.scheduled_date <  $1::timestamptz + make_interval(hours => 24 * (p.notification_days_before + $2::int))`

// ListPlansWithAttachableEmptyRoot returns the IDs of plans whose next empty
// root is inside the attach window at now. It is only a trigger for the
// notification tick: the claim re-derives every gate under the locks.
func (s *PostgresStore) ListPlansWithAttachableEmptyRoot(ctx context.Context, now time.Time) (map[string]struct{}, error) {
	rows, err := s.db.Query(ctx, attachableEmptyRootQuery, now, EmptyRootAttachSlackDays)
	if err != nil {
		return nil, fmt.Errorf("failed to query attachable empty roots: %w", err)
	}
	defer rows.Close()

	plans := make(map[string]struct{})
	for rows.Next() {
		var planID string
		if err := rows.Scan(&planID); err != nil {
			return nil, fmt.Errorf("failed to scan attachable empty root: %w", err)
		}
		plans[planID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read attachable empty roots: %w", err)
	}
	return plans, nil
}
