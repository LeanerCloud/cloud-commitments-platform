package purchase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// LadderFiredStaleAfter is how long a fired tranche may stay unresolved before
// the reconciler logs an alert for it. It never changes the tranche: an
// unresolved fired tranche keeps netting, so the ladder cannot over-buy.
const LadderFiredStaleAfter = 6 * time.Hour

const ladderReconcileBatch = 200

// ladderExecutionScheduled is the purchase_executions status of a chain tail
// that waits for its fire time.
const ladderExecutionScheduled = "scheduled"

// Reasons stored in ladder_tranches.status_reason by the reconciler.
const (
	ladderReasonNoSubmission      = "no_submission"
	ladderReasonApprovalCanceled  = "approval_canceled"
	ladderReasonApprovalExpired   = "approval_expired"
	ladderReasonCommitmentVisible = "commitment_visible"
)

// CommitmentVisibility reports whether the commitment bought by a completed
// execution already shows up in the provider's layer listing. The production
// adapter lands with the executor; an error or false keeps the tranche fired.
type CommitmentVisibility interface {
	CommitmentVisible(ctx context.Context, tranche config.LadderTrancheDB, executionID string) (bool, error)
}

// LadderReconcileStore is the slice of the Postgres store the reconciler uses.
type LadderReconcileStore interface {
	ListFiredLadderTranches(ctx context.Context, after *config.LadderFiredCursor, limit int) ([]config.LadderTrancheDB, error)
	WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error
	LockLadderTrancheChainTx(ctx context.Context, tx pgx.Tx, trancheID string) ([]config.LadderChainNode, error)
	TransitionLadderTrancheTx(ctx context.Context, tx pgx.Tx, id string, from, to ladder.TrancheStatus, reason string) (bool, error)
}

// LadderReconcileResult counts what one sweep did.
type LadderReconcileResult struct {
	Checked, Completed, Failed, Unresolved, Stale, LostRace, Errored int
}

type ladderVerdict struct {
	to     ladder.TrancheStatus // empty: leave the tranche fired
	reason string
	note   string // why it stays fired (for the log)
	// waiting marks a tranche whose execution has not been due yet, so being
	// unresolved is expected and never counts as stale.
	waiting bool
}

// decideFiredTranche maps a locked retry chain (head first) to a verdict. The
// no_submission flag is read only from nodes whose status is failed. A tranche
// becomes failed only when nothing in the whole chain can have reached the
// provider; anything else stays fired, because fired keeps netting and failed
// does not.
func decideFiredTranche(chain []config.LadderChainNode, now time.Time, visible func(executionID string) (bool, error)) ladderVerdict {
	tail := chain[len(chain)-1]
	if tail.RetryExecutionID != nil {
		return ladderVerdict{note: fmt.Sprintf("retry chain unresolved after %d executions", len(chain))}
	}
	switch tail.Status {
	case config.StatusCompleted:
		return completedVerdict(tail.ExecutionID, visible)
	case config.StatusPartiallyCompleted:
		return ladderVerdict{note: "partially completed: some recommendations may have been bought"}
	case config.StatusFailed:
		if !tail.NoSubmission {
			return ladderVerdict{note: "failed without a no_submission proof (outcome unknown)"}
		}
		return failedIfPredecessorsProven(chain, ladderReasonNoSubmission)
	case config.StatusCanceled, config.LegacyStatusCanceled:
		return failedIfPredecessorsProven(chain, ladderReasonApprovalCanceled)
	case config.StatusExpired:
		return failedIfPredecessorsProven(chain, ladderReasonApprovalExpired)
	case ladderExecutionScheduled:
		if scheduledTailWaiting(tail, now) {
			return ladderVerdict{note: "execution is scheduled and not yet due", waiting: true}
		}
		return ladderVerdict{note: "execution is scheduled but overdue or has no due time"}
	default:
		return ladderVerdict{note: "execution is " + tail.Status}
	}
}

// scheduledTailWaiting reports whether a scheduled tail is still inside its
// own due time plus LadderFiredStaleAfter. Past that (the fire sweep is not
// running) or with no due time it is no longer waiting and can go stale.
func scheduledTailWaiting(tail config.LadderChainNode, now time.Time) bool {
	return tail.ScheduledExecutionAt != nil && now.Before(tail.ScheduledExecutionAt.Add(LadderFiredStaleAfter))
}

// completedVerdict completes the tranche only once its commitment is visible.
func completedVerdict(executionID string, visible func(executionID string) (bool, error)) ladderVerdict {
	ok, err := visible(executionID)
	if err != nil {
		return ladderVerdict{note: fmt.Sprintf("commitment visibility check failed: %v", err)}
	}
	if !ok {
		return ladderVerdict{note: "completed, commitment not yet visible"}
	}
	return ladderVerdict{to: ladder.TrancheStatusCompleted, reason: ladderReasonCommitmentVisible}
}

func failedIfPredecessorsProven(chain []config.LadderChainNode, reason string) ladderVerdict {
	for _, n := range chain[:len(chain)-1] {
		if n.Status != config.StatusFailed || !n.NoSubmission {
			return ladderVerdict{note: fmt.Sprintf("earlier attempt %s (%s) has no no_submission proof", n.ExecutionID, n.Status)}
		}
	}
	return ladderVerdict{to: ladder.TrancheStatusFailed, reason: reason}
}

// ReconcileLadderTranches moves fired tranches to completed or failed from the
// state of their executions. Each tranche is decided inside one transaction
// that holds the tranche row and the retry-chain tail row, so a concurrent
// Retry cannot link a successor between the decision and the write. Tranches
// it cannot prove stay fired and keep netting; those older than
// LadderFiredStaleAfter are logged as errors. A tranche whose reconcile errors
// is counted in Errored and logged, and the sweep goes on; after the last
// tranche the run returns a summary error so the task shows as failed. All
// fired tranches are visited, page by page. The ladder_reconcile task runs it.
func ReconcileLadderTranches(ctx context.Context, store LadderReconcileStore, vis CommitmentVisibility, now time.Time) (LadderReconcileResult, error) {
	return reconcileFiredTranches(ctx, store, vis, now, ladderReconcileBatch)
}

func reconcileFiredTranches(ctx context.Context, store LadderReconcileStore, vis CommitmentVisibility, now time.Time, batch int) (LadderReconcileResult, error) {
	var res LadderReconcileResult
	err := forEachFiredPage(ctx, store, batch, func(page []config.LadderTrancheDB) error {
		for i := range page {
			// A canceled run stops here instead of counting every remaining
			// tranche as errored.
			if err := ctx.Err(); err != nil {
				return err
			}
			res.Checked++
			if err := reconcileOne(ctx, store, vis, now, page[i], &res); err != nil {
				res.Errored++
				logging.Errorf("ladder_reconcile: tranche %s: %v", page[i].ID, err)
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	if res.Errored > 0 {
		return res, fmt.Errorf("ladder_reconcile: %d tranche(s) errored", res.Errored)
	}
	return res, nil
}

// forEachFiredPage walks every fired tranche in (scheduled_date, id) order with
// keyset pagination, so unresolvable tranches at the front cannot starve the
// ones behind them. It stops at the first short page.
func forEachFiredPage(ctx context.Context, store LadderReconcileStore, batch int, fn func([]config.LadderTrancheDB) error) error {
	var after *config.LadderFiredCursor
	for {
		page, err := store.ListFiredLadderTranches(ctx, after, batch)
		if err != nil {
			return err
		}
		if len(page) > 0 {
			if err := fn(page); err != nil {
				return err
			}
		}
		if len(page) < batch {
			return nil
		}
		last := page[len(page)-1]
		after = &config.LadderFiredCursor{ScheduledDate: last.ScheduledDate, ID: last.ID}
	}
}

func reconcileOne(ctx context.Context, store LadderReconcileStore, vis CommitmentVisibility, now time.Time, tr config.LadderTrancheDB, res *LadderReconcileResult) error {
	var verdict ladderVerdict
	var moved bool
	var head time.Time
	err := store.WithTx(ctx, func(tx pgx.Tx) error {
		chain, err := store.LockLadderTrancheChainTx(ctx, tx, tr.ID)
		if err != nil {
			return err
		}
		head = chain[0].CreatedAt
		verdict = decideFiredTranche(chain, now, func(execID string) (bool, error) {
			return vis.CommitmentVisible(ctx, tr, execID)
		})
		if verdict.to == "" {
			return nil
		}
		moved, err = store.TransitionLadderTrancheTx(ctx, tx, tr.ID, ladder.TrancheStatusFired, verdict.to, verdict.reason)
		return err
	})
	switch {
	case errors.Is(err, config.ErrLadderTrancheNotFired):
		res.LostRace++
		return nil
	case err != nil:
		return fmt.Errorf("reconcile ladder_tranche %s: %w", tr.ID, err)
	case verdict.to == "":
		res.Unresolved++
		if !verdict.waiting && now.Sub(head) > LadderFiredStaleAfter {
			res.Stale++
			logging.Errorf("ladder tranche %s fired %s ago is unresolved: %s", tr.ID, now.Sub(head).Round(time.Minute), verdict.note)
		}
	case !moved:
		res.LostRace++
	case verdict.to == ladder.TrancheStatusCompleted:
		res.Completed++
	default:
		res.Failed++
	}
	return nil
}
