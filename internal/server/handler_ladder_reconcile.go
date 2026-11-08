package server

import (
	"context"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
)

type ladderSPDescriberSource interface {
	LadderSPDescriber(ctx context.Context, cloudAccountID, region string) (purchase.SPDescriber, error)
}

// handleLadderReconcile settles fired ladder tranches from their executions.
// It is deliberately not gated by laddering_enabled or ladder_execution_enabled:
// tranches already fired must keep settling after a switch is turned off. With
// no fired tranche it touches no provider API.
func (app *Application) handleLadderReconcile(ctx context.Context) (purchase.LadderReconcileResult, error) {
	store, ok := app.Config.(purchase.LadderReconcileRunStore)
	if !ok {
		return purchase.LadderReconcileResult{}, fmt.Errorf("ladder_reconcile: config store %T cannot reconcile ladder tranches", app.Config)
	}
	sp, ok := app.Purchase.(ladderSPDescriberSource)
	if !ok {
		return purchase.LadderReconcileResult{}, fmt.Errorf("ladder_reconcile: purchase manager %T cannot describe Savings Plans", app.Purchase)
	}
	res, err := purchase.RunLadderReconcile(ctx, purchase.LadderReconcileDeps{Store: store, SP: sp.LadderSPDescriber, Now: time.Now})
	logging.Infof("ladder_reconcile: checked=%d completed=%d failed=%d unresolved=%d stale=%d lost_race=%d errored=%d",
		res.Checked, res.Completed, res.Failed, res.Unresolved, res.Stale, res.LostRace, res.Errored)
	return res, err
}
