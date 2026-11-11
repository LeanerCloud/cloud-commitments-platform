package purchase

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// ladderReasonNoticeSendFailed is the status_reason of a tranche whose notice
// could not be sent.
const ladderReasonNoticeSendFailed = "notice_send_failed"

// settleNoticeFailed ends a claimed tranche whose notice could not be sent: in
// ONE transaction it cancels the unannounced execution and fails the tranche
// with ladderReasonNoticeSendFailed. One transaction means the reconciler can
// never observe a canceled execution under a still-fired tranche, which it
// would read as a revoke (approval_canceled) and disable the config for. A
// notice failure is not a revoke, so the config stays enabled. The locks are
// taken in the reconciler's order (tranche row, then the chain tail).
//
// Anything but the plain case, a one-node chain whose tail is this execution,
// still scheduled and unretried, is left to the reconciler: an operator revoked
// it first (that IS a revoke) or something else moved it.
func settleNoticeFailed(ctx context.Context, store LadderFireStore, tr *config.LadderTrancheDB, exec *config.PurchaseExecution) error {
	return store.WithTx(ctx, func(tx pgx.Tx) error {
		chain, err := store.LockLadderTrancheChainTx(ctx, tx, tr.ID)
		if errors.Is(err, config.ErrLadderTrancheNotFired) {
			logging.Infof("ladder_fire: tranche %s is no longer fired; nothing to settle", tr.ID)
			return nil
		}
		if err != nil {
			return err
		}
		if !plainUnannouncedChain(chain, exec.ExecutionID) {
			logging.Infof("ladder_fire: tranche %s execution %s is no longer the plain scheduled tail; the reconciler decides", tr.ID, exec.ExecutionID)
			return nil
		}
		canceled, status, err := store.CancelScheduledExecutionAtomic(ctx, tx, exec.ExecutionID, nil)
		if err != nil {
			return err
		}
		if !canceled {
			return fmt.Errorf("execution %s was locked as scheduled but could not be canceled (status %s)", exec.ExecutionID, status)
		}
		moved, err := store.TransitionLadderTrancheTx(ctx, tx, tr.ID, ladder.TrancheStatusFired, ladder.TrancheStatusFailed, ladderReasonNoticeSendFailed)
		if err != nil {
			return err
		}
		if !moved {
			return fmt.Errorf("tranche %s was locked as fired but could not be failed", tr.ID)
		}
		return nil
	})
}

func plainUnannouncedChain(chain []config.LadderChainNode, executionID string) bool {
	return len(chain) == 1 && chain[0].ExecutionID == executionID &&
		chain[0].Status == ladderExecutionScheduled && chain[0].RetryExecutionID == nil
}
