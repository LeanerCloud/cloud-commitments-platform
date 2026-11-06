package server

import (
	"context"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/jackc/pgx/v5"
)

// ladderReconcileStore is the slice of the Postgres store RunLadderReconcile
// needs beyond config.StoreInterface.
type ladderReconcileStore interface {
	ListFiredLadderTranches(ctx context.Context, limit int) ([]config.LadderTrancheDB, error)
	WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error
	LockLadderTrancheChainTx(ctx context.Context, tx pgx.Tx, trancheID string) ([]config.LadderChainNode, error)
	TransitionLadderTrancheTx(ctx context.Context, tx pgx.Tx, id string, from, to ladder.TrancheStatus, reason string) (bool, error)
	GetExecutionByID(ctx context.Context, id string) (*config.PurchaseExecution, error)
}

type ladderSPDescriberSource interface {
	LadderSPDescriber(ctx context.Context, cloudAccountID, region string) (purchase.SPDescriber, error)
}

// handleLadderReconcile settles fired ladder tranches from their executions.
// It is deliberately not gated by laddering_enabled or ladder_execution_enabled:
// tranches already fired must keep settling after a switch is turned off. With
// no fired tranche it touches no provider API.
func (app *Application) handleLadderReconcile(ctx context.Context) (purchase.LadderReconcileResult, error) {
	store, ok := app.Config.(ladderReconcileStore)
	if !ok {
		return purchase.LadderReconcileResult{}, fmt.Errorf("ladder_reconcile: config store %T cannot reconcile ladder tranches", app.Config)
	}
	sp, ok := app.Purchase.(ladderSPDescriberSource)
	if !ok {
		return purchase.LadderReconcileResult{}, fmt.Errorf("ladder_reconcile: purchase manager %T cannot describe Savings Plans", app.Purchase)
	}
	res, err := purchase.RunLadderReconcile(ctx, purchase.LadderReconcileDeps{Store: store, SP: sp.LadderSPDescriber, Now: time.Now})
	logging.Infof("ladder_reconcile: checked=%d completed=%d failed=%d unresolved=%d stale=%d lost_race=%d",
		res.Checked, res.Completed, res.Failed, res.Unresolved, res.Stale, res.LostRace)
	return res, err
}
