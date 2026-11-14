package config

import (
	"context"
	"fmt"
)

// CleanupOldExecutions deletes purchase executions older than retentionDays.
//
// Three independent cleanup branches, each with its own retention window so
// that a row far in one dimension doesn't block cleanup in the other:
//
//  1. Completed-state cleanup: `status = 'completed' AND scheduled_date <
//     NOW() - retention`. Keeps recent completions visible in the UI for at
//     least `retention` days before purging. `scheduled_date` is the right
//     clock here because a completed row executed on (or very near) the day
//     it was scheduled for.
//
//  2. Canceled-state cleanup: `status = 'canceled' AND
//     updated_at < NOW() - retention`.
//
//     This branch retains on `updated_at`, NOT `scheduled_date`, and must
//     stay that way: CountExecutionsByPlanAndStatus windows the plan health
//     score's canceled count on `updated_at` over exactly this retention
//     period (see internal/api/plan_health.go planHealthLookbackDays), and
//     CancelExecutionAtomic stamps `updated_at` while leaving
//     `scheduled_date` untouched. Retaining on `scheduled_date` therefore
//     purged rows the score was still counting: cancel a purchase whose
//     scheduled_date is already past the horizon (a plan's rows are created
//     up front, and parseCreatePurchasesRequest accepts a past start date)
//     and the next sweep deleted it the same day, silently raising that
//     plan's health score by up to 20 points overnight with no operator
//     action. Nothing accumulates indefinitely: a genuinely old canceled
//     row still has an old `updated_at`.
//
//     This cuts the other way too, and the change is user-visible: a
//     canceled row with a FUTURE `scheduled_date` used to be retained
//     forever, because `scheduled_date < NOW() - retention` was never true
//     for it. It now leaves History `retention` days after the
//     cancellation rather than `retention` days after the date it was
//     scheduled for, so a purchase canceled today for a 2-year-out date
//     disappears in a month instead of in two years. That closes an
//     unbounded-retention leak and is the behavior the health window
//     already assumed.
//
//  3. Expired-row cleanup: `expires_at IS NOT NULL AND expires_at < NOW() -
//     retention`, for a settled row the health score does not count. This
//     is the row's own TTL column (SavePurchaseExecution writes it from
//     PurchaseExecution.TTL), NOT the approval-token deadline -- that is a
//     separate column, `approval_token_expires_at`, added by migration
//
//  000051. A settled row whose TTL lapsed longer than `retention` ago is
//     dead, so without this branch it would accumulate indefinitely.
//
//     The status gate is an ALLOWLIST: status = ANY(TerminalExecutionStatuses)
//     AND status <> ALL(HealthScoredExecutionStatuses) (#998, #1173). This
//     is the only expires_at retention path since cmd/cleanup-lambda, which
//     ran its own copy of the predicate, was deleted (#357). A lapsed
//     TTL says nothing about whether an executor, scheduler or reaper still
//     needs the row: `expires_at` is set at insert and never rewritten, so an
//     `approved` or `running` row stranded by a dead executor (the rows
//     GetStaleApprovedExecutions and ListStuckExecutions exist to find) used
//     to be deleted here before the reaper could resolve it (#120). Pending
//     and notified rows are kept too: ExpireExecutionAtomic moves a lapsed
//     approval to `expired`, which this branch then reaps. A status added to
//     the schema later is kept until someone classifies it in
//     TerminalExecutionStatuses, the fail-closed direction.
//
//     Every status in HealthScoredExecutionStatuses is excluded here, which
//     is the whole reason that slice is exported rather than spelled out
//     twice. `expires_at` is written once at insert and never rewritten
//     when a row later reaches a terminal status, so it can be arbitrarily
//     older than the terminal transition. Without the exclusion, a row that
//     failed or was canceled TODAY but carries a lapsed `expires_at` is
//     purged by this branch the same day, while the score is still counting
//     it -- reintroducing through this branch the exact overnight score
//     jump branch 2 exists to prevent, and at up to -40 for `failed`, twice
//     canceled's worst case. Their own terminal-status branches govern them.
//
//     `failed` deliberately has no cleanup branch of its own: the health
//     score's lookback comment (internal/api/plan_health.go
//     planHealthLookbackDays) documents that this sweep "never deletes
//     failed ones", and the score's window depends on that being true.
//
//     Note on reachability: `expires_at` currently has exactly one writer,
//     SavePurchaseExecution via timeFromTTL(execution.TTL), and no
//     production path assigns PurchaseExecution.TTL, so rows created by
//     current code leave it NULL and this branch is inert for them. It can
//     still hold non-NULL values on legacy imported rows, which is why the
//     exclusion is written defensively rather than omitted.
//
// The branches are OR'd: a row that qualifies under ANY is deleted,
// regardless of the other columns. An earlier revision of this function
// incorrectly AND'd the `scheduled_date` gate with both branches, which
// meant pending rows with a far-future `scheduled_date` but a long-past
// `expires_at` never got cleaned up (a 2-year-out purchase whose row TTL
// lapsed would leave a dead row accumulating for 1.9 years). Keep each
// branch fully parenthesized so a future edit can't change the predicate's
// meaning through AND/OR precedence.
//
// NULL `expires_at` is excluded from branch 3 so rows that never had a TTL
// are safe from expiry-based cleanup -- which, per the reachability note
// above, is every row current code writes.
func (s *PostgresStore) CleanupOldExecutions(ctx context.Context, retentionDays int) (int64, error) {
	// Branch 3's allowlist and exclusion are parameterized from
	// TerminalExecutionStatuses and HealthScoredExecutionStatuses instead of
	// repeating literal lists, so this branch and the plan health score
	// cannot drift. `status` is NOT NULL (migration
	// 000001), so neither array test is exposed to three-valued logic.
	//
	// Under READ COMMITTED a row another transaction is updating is
	// rechecked against the whole predicate once that transaction commits,
	// so a row that leaves the allowlist while the DELETE waits survives.
	query := `
		DELETE FROM purchase_executions
		WHERE (
		        status = 'completed'
		    AND scheduled_date < NOW() - INTERVAL '1 day' * $1
		      )
		   OR (
		        status = 'canceled'
		    AND updated_at     < NOW() - INTERVAL '1 day' * $1
		      )
		   OR (
		        status = ANY($2)
		    AND status <> ALL($3)
		    AND expires_at IS NOT NULL
		    AND expires_at     < NOW() - INTERVAL '1 day' * $1
		      )
	`

	result, err := s.db.Exec(ctx, query, retentionDays, TerminalExecutionStatuses, HealthScoredExecutionStatuses)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup old executions: %w", err)
	}

	return result.RowsAffected(), nil
}
