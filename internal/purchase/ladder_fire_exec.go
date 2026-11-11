package purchase

import (
	"context"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ladderExecSaver is the one store call the claim callback makes.
type ladderExecSaver interface {
	SavePurchaseExecutionTx(ctx context.Context, tx pgx.Tx, execution *config.PurchaseExecution) error
}

// newLadderExecution builds the scheduled execution for one claimed tranche.
// It has no plan (NULL plan_id) and no approver: the only things between it and
// a purchase are the revoke window (ScheduledExecutionAt is the only fire gate)
// and the notice stamp, which the sweep sets after a proven send. Upfront cost
// and savings are 0 because the commitment is no-upfront and no savings
// estimate is invented. IdempotencyKey is the tranche id, the link the
// execute-time recheck uses to find the tranche.
func newLadderExecution(tr config.LadderTrancheDB, recs []config.RecommendationRecord, cfg *config.LadderConfigDB, delay time.Duration, now time.Time) *config.PurchaseExecution {
	fireAt := now.Add(delay)
	acct := cfg.CloudAccountID
	return &config.PurchaseExecution{
		ExecutionID:          uuid.New().String(),
		Status:               ladderExecutionScheduled,
		ScheduledDate:        now,
		ScheduledExecutionAt: &fireAt,
		Recommendations:      recs,
		CloudAccountID:       &acct,
		CreatedBySystem:      true,
		IdempotencyKey:       tr.ID,
	}
}

// ladderClaimCreate returns the claim callback. It runs under the config's
// advisory lock inside the claim transaction, so it only builds and saves; it
// re-runs the instrument builder on the LOCKED row because an amend between the
// resolve step and the claim can change the amount. A build or save error rolls
// the whole config's claim back. Each saved execution is also recorded in execs
// by tranche id for the notice step.
func ladderClaimCreate(store ladderExecSaver, registry *LadderInstrumentRegistry, cfg *config.LadderConfigDB, region string,
	delay time.Duration, now time.Time, execs map[string]*config.PurchaseExecution,
) func(ctx context.Context, tx pgx.Tx, tr config.LadderTrancheDB) (string, error) {
	return func(ctx context.Context, tx pgx.Tx, tr config.LadderTrancheDB) (string, error) {
		recs, err := registry.Build(LadderBuildInput{Tranche: tr, CloudAccountID: cfg.CloudAccountID, Region: region})
		if err != nil {
			return "", fmt.Errorf("build ladder tranche %s: %w", tr.ID, err)
		}
		exec := newLadderExecution(tr, recs, cfg, delay, now)
		if err = store.SavePurchaseExecutionTx(ctx, tx, exec); err != nil {
			return "", fmt.Errorf("save execution for ladder tranche %s: %w", tr.ID, err)
		}
		execs[tr.ID] = exec
		return exec.ExecutionID, nil
	}
}
