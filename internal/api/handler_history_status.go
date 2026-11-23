package api

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// actionableExecutionStatuses are the PurchaseExecution statuses a user can still
// act on or that are in flight. They are fetched separately from the terminal
// class so newer terminal rows cannot push them out of the capped listing, and
// they are not paged: the first page lists them all (issue #674).
//
// Issue #372 assumed approval executes synchronously (approved -> completed/failed inside one
// HTTP request) and so excluded "approved"; issue #621 showed that
// assumption breaks under interruption: a Lambda timeout or a crash mid-
// execution leaves the row stuck in "approved"/"running"/"paused", in
// neither purchase_history nor this list, and it silently vanishes — the
// worst failure mode for a financial action (the user can't tell whether
// the purchase fired and may re-approve). So we now load those in-flight
// states too, rendered with a clear "in progress" badge rather than as a
// (misleading) completed row.
//
// "scheduled" is included so Gmail-style pre-fire delayed executions (issue #291
// wave-2) appear in the History view with a Revoke button before the cloud SDK
// call fires. Without this entry the row is invisible to the History UI, making
// the Revoke button unreachable (issue #290, second-wave CR Finding E).
var actionableExecutionStatuses = []string{"pending", "notified", "scheduled", "approved", "running", "paused"}

// terminalExecutionStatuses are the finished PurchaseExecution statuses the
// History view pages through alongside completed purchases (fetchHistoryPage).
//
// "completed" is also loaded, but the stream synthesizes a
// row for it ONLY when the execution carries a non-empty Error — the
// audit-gap case where the purchase succeeded but its purchase_history
// write failed (issue #621 secondary path). A normal completed execution
// has Error=="" and is skipped here so it surfaces exactly once via its
// purchase_history rows (no duplicate). The execution row's PurchaseID is
// the ExecutionID while a purchase_history row's is the CommitmentID, so
// the keys never collide even when both happen to render.
//
// "partially_completed" (issue #642) is loaded and ALWAYS synthesized: a
// partial run committed some recs to purchase_history (those render from the
// DB rows) and failed others. The synthesized execution row carries the
// partial-failure marker and is flagged IsAuditGap so its execution-level
// dollars are excluded from the dashboard totals — the committed dollars are
// already counted via the per-rec purchase_history rows that succeeded.
var terminalExecutionStatuses = []string{"completed", "partially_completed", "failed", "expired", config.StatusCanceled}

// approvalExpiryWindow is how long a pending approval stays actionable
// before the History view flips it to "expired". Aligns with the
// cleanup-executions TTL and the user expectation that a week-old
// approval link is stale.
const approvalExpiryWindow = 7 * 24 * time.Hour

// isStaleExecution reports whether the execution is a pending/notified
// approval older than approvalExpiryWindow that should be transitioned to
// "expired". Extracted so both fetchExecutionsAsHistory and the expire
// sweep (sync on Lambda, async on servers) share one staleness check.
func isStaleExecution(exec config.PurchaseExecution) bool {
	if exec.Status != "pending" && exec.Status != "notified" {
		return false
	}
	return time.Since(exec.ScheduledDate) >= approvalExpiryWindow
}

// expireStaleExecutions fires TransitionExecutionStatus for each stale
// execution. On long-running servers this happens in a best-effort goroutine
// that outlives the request context: context.Background() ensures the
// transitions are not canceled when the HTTP handler returns. On Lambda that
// guarantee does not hold: the execution environment freezes as soon as the
// response is returned, so a background goroutine would be suspended
// mid-sweep and stale rows could stay "pending" indefinitely (issue #1170).
// There the sweep runs synchronously before the handler returns, mirroring
// the SWR cache's isLambda gate (ri_utilization_cache.go) and reading the same
// h.isLambda value (HandlerConfig.IsLambda, the Application's single captured
// answer, issue #110) rather than the live environment. The sweep is a handful
// of cheap UPDATEs, so the synchronous cost on Lambda is negligible. Errors are
// logged and skipped — a missed transition leaves the row "pending" until the
// next History load, which is better than failing the read response.
//
// The sweep is idempotent per execution ID: TransitionExecutionStatus is
// guarded by the FROM-status list ("pending","notified"), so a concurrent
// caller that wins the race causes the loser's update to affect 0 rows and
// return an error, which is already handled by the Warnf below. Two
// simultaneous GET requests can both sweep the same stale row; only one
// transition commits — this is safe and expected.
func (h *Handler) expireStaleExecutions(staleExecs []config.PurchaseExecution) {
	if len(staleExecs) == 0 {
		return
	}
	if len(staleExecs) > config.DefaultListLimit {
		logging.Warnf("history: sweeping %d of %d stale executions", config.DefaultListLimit, len(staleExecs))
		staleExecs = staleExecs[:config.DefaultListLimit]
	}
	if h.isLambda {
		h.expireStaleExecutionsSweep(staleExecs)
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Warnf("history: expireStaleExecutionsAsync goroutine panic: %v", r)
			}
		}()
		h.expireStaleExecutionsSweep(staleExecs)
	}()
}

// expireStaleExecutionsSweep is the shared sweep body for both branches of
// expireStaleExecutions. It deliberately uses context.Background()
// rather than the request context: on servers the goroutine outlives the
// request, and on Lambda the request context may carry a deadline that
// should not abort the best-effort transitions.
func (h *Handler) expireStaleExecutionsSweep(staleExecs []config.PurchaseExecution) {
	ctx := context.Background()
	for i := range staleExecs {
		// The suppressions go in the same transaction as the status flip: an
		// expired step bought nothing, and leaving its rows would keep hiding
		// recommendations nobody purchased (platform#631).
		err := h.config.WithTx(ctx, func(tx pgx.Tx) error {
			expired, expErr := h.config.ExpireExecutionAtomic(ctx, tx, staleExecs[i].ExecutionID, staleExecs[i].ScheduledDate)
			if expErr != nil || !expired {
				return expErr
			}
			return h.config.DeleteSuppressionsByExecutionTx(ctx, tx, staleExecs[i].ExecutionID)
		})
		if err != nil {
			logging.Warnf("history: expire sweep for execution %s failed: %v", staleExecs[i].ExecutionID, err)
		}
	}
}
