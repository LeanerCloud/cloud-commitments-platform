package purchase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
)

// The notice loop runs after the claim has committed, on a context detached
// from the sweep's, so a dead sweep context cannot leave a claimed execution
// half announced. The loop as a whole gets ladderNoticeLoopCap; a tranche only
// starts its send when ladderNoticeBudget is left, and its send and stamp share
// one budget of that size. Variables so a test can shrink them.
var (
	ladderNoticeLoopCap = 120 * time.Second
	ladderNoticeBudget  = 45 * time.Second
)

// announceClaimed sends the revoke-window notice for each claimed tranche, in
// claim order, and stamps the execution as announced only after a send
// succeeded: the stamp is the one thing that makes a ladder execution fireable.
// A send that fails, a stamp that fails and a tranche deferred for lack of time
// all leave the execution unannounced, which the execute-time recheck refuses:
// the amount nets against the ceiling for a day, the reconciler then fails the
// tranche and disables the config, and an operator re-enables it. No spend.
func announceClaimed(ctx context.Context, deps LadderFireDeps, plan *ladderFirePlan, claimed []config.LadderTrancheDB,
	execs map[string]*config.PurchaseExecution, res *LadderFireResult,
) {
	loopCtx, cancel := ctxutil.DetachedWithTimeout(ctx, ladderNoticeLoopCap)
	defer cancel()
	for i := range claimed {
		tr := &claimed[i]
		exec := execs[tr.ID]
		switch {
		case exec == nil:
			res.NoticeFailed++
			logging.Errorf("ladder_fire: tranche %s was claimed but its execution is unknown; it stays unannounced", tr.ID)
		case !enoughTimeFor(loopCtx, ladderNoticeBudget):
			res.NoticeDeferred++
			logging.Errorf("ladder_fire: tranche %s execution %s not announced: not enough time left; it stays unannounced and the recheck will refuse it", tr.ID, exec.ExecutionID)
		default:
			announceOne(ctx, deps, plan, tr, exec, res)
		}
	}
}

func announceOne(ctx context.Context, deps LadderFireDeps, plan *ladderFirePlan, tr *config.LadderTrancheDB, exec *config.PurchaseExecution, res *LadderFireResult) {
	sendCtx, cancel := ctxutil.DetachedWithTimeout(ctx, ladderNoticeBudget)
	defer cancel()
	if err := sendLadderNotice(sendCtx, deps, plan, tr, exec); err != nil {
		res.NoticeFailed++
		logging.Errorf("ladder_fire: tranche %s execution %s notice failed: %v", tr.ID, exec.ExecutionID, err)
		return
	}
	now := deps.Now()
	exec.NotificationSent = &now
	err := deps.Store.SaveExecutionIfStatus(sendCtx, exec, []string{ladderExecutionScheduled})
	switch {
	case err == nil:
		res.Announced++
	case errors.Is(err, config.ErrExecutionNotInExpectedStatus):
		logging.Infof("ladder_fire: tranche %s execution %s was revoked before it could be stamped", tr.ID, exec.ExecutionID)
	default:
		res.StampFailed++
		logging.Errorf("ladder_fire: tranche %s execution %s was announced but not stamped, so it will not fire: %v", tr.ID, exec.ExecutionID, err)
	}
}

func sendLadderNotice(ctx context.Context, deps LadderFireDeps, plan *ladderFirePlan, tr *config.LadderTrancheDB, exec *config.PurchaseExecution) error {
	facts, err := ladderNoticeFactsFor(tr, exec, plan.acct)
	if err != nil {
		return err
	}
	return SendLadderNotice(ctx, deps.Notifier, plan.global, deps.DashboardURL, exec, facts)
}

// ladderNoticeFactsFor reads what the notice states from the execution's own
// record, the amount actually bought after the builder's flooring, not from the
// raw tranche amount.
func ladderNoticeFactsFor(tr *config.LadderTrancheDB, exec *config.PurchaseExecution, acct *config.CloudAccount) (LadderNoticeFacts, error) {
	if len(exec.Recommendations) != 1 {
		return LadderNoticeFacts{}, fmt.Errorf("expected one recommendation, got %d", len(exec.Recommendations))
	}
	rec := exec.Recommendations[0]
	decoded, err := common.DecodeServiceDetailsFor(rec.Service, rec.Details)
	if err != nil {
		return LadderNoticeFacts{}, fmt.Errorf("decode recommendation details: %w", err)
	}
	sp, ok := decoded.(*common.SavingsPlanDetails)
	if !ok {
		return LadderNoticeFacts{}, fmt.Errorf("recommendation is not a Savings Plan (%T)", decoded)
	}
	label := acct.ExternalID
	if acct.Name != "" {
		label = acct.Name + " (" + acct.ExternalID + ")"
	}
	return LadderNoticeFacts{HourlyCommitment: sp.HourlyCommitment, TermYears: rec.Term, AccountLabel: label, TrancheID: tr.ID}, nil
}
