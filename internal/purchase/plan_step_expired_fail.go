package purchase

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// errExpiredAutoRootNeedsRearm is the reason recorded on a step root that
// lapsed on an auto-purchase plan without four-eyes (#1904). Re-arming such a
// root (#1785) would auto-buy freshly resolved recommendations after an
// arbitrary pause, so it is failed instead of left expired and silent.
var errExpiredAutoRootNeedsRearm = errors.New("expired approval root on an auto-purchase plan without four-eyes: operator must re-arm the plan")

// expiredAutoRootFailable reports whether the plan is one whose lone expired
// step root is failed visibly: enabled, kill switch on, not dates-managed (those
// follow the schedule clock) and no approval needed. planNeedsApproval is false
// only for an auto plan with four-eyes off (a nil global reads as "needs
// approval"), so manual plans, which tryReadoptExpired re-arms, and four-eyes
// plans, which keep the one-shot re-arm, are never touched.
func expiredAutoRootFailable(plan *config.PurchasePlan, global *config.GlobalConfig, autoPurchaseEnabled bool) bool {
	return plan.Enabled && autoPurchaseEnabled && !plan.RampSchedule.DatesManaged && !planNeedsApproval(*plan, global)
}

// A root that was already re-armed once (#1785) and expired again is failed
// too: the struct does not carry approval_readopted_at or ladder_run_id, and a
// second expiry on a plan that may not be re-armed is the same stall. Expired
// rows never purchased, so failing one is always safe.
//
// failExpiredAutoRoot moves the plan's lone expired, never-acted-on step root
// to failed with errExpiredAutoRootNeedsRearm, once. The failed row is final
// (adoptableRootRow never adopts it), so later ticks find nothing to do and
// stay silent. It never purchases and never creates a row. The failure is
// logged and notified only after the transaction committed the row.
func (m *Manager) failExpiredAutoRoot(ctx context.Context, plan *config.PurchasePlan, global *config.GlobalConfig, autoPurchaseEnabled bool) error {
	if !expiredAutoRootFailable(plan, global, autoPurchaseEnabled) {
		return nil
	}
	if ok, err := m.hasExpiredLoneRoot(ctx, plan); err != nil || !ok {
		return err
	}
	var failed *config.PurchaseExecution
	if err := m.config.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		failed, err = m.failExpiredAutoRootTx(ctx, tx, plan.ID)
		return err
	}); err != nil {
		return fmt.Errorf("failed to inspect expired auto plan step: %w", err)
	}
	if failed == nil {
		return nil
	}
	logging.Warnf("purchase[%s]: plan %s step %d root expired and was marked failed: %v", failed.ExecutionID, failed.PlanID, failed.StepNumber, errExpiredAutoRootNeedsRearm)
	m.notifyExecutionFailure(ctx, failed)
	return nil
}

// hasExpiredLoneRoot is the cheap pre-check: one unlocked read of the step the
// listed plan is on, so a plan with nothing to fail (the usual case, and every
// tick after the root is failed) never takes the plan row lock. The locked
// re-check in failExpiredAutoRootTx stays authoritative.
func (m *Manager) hasExpiredLoneRoot(ctx context.Context, plan *config.PurchasePlan) (bool, error) {
	found := false
	err := m.config.WithTx(ctx, func(tx pgx.Tx) error {
		rows, err := m.config.ListExecutionsForPlanStepTx(ctx, tx, plan.ID, plan.RampSchedule.CurrentStep+1)
		found = err == nil && readoptableExpiredRoot(rows) != nil
		return err
	})
	if err != nil {
		return false, fmt.Errorf("failed to inspect expired auto plan step: %w", err)
	}
	return found, nil
}

// failExpiredAutoRootTx re-derives every gate from the locked plan and the
// step's rows, then fails the root with a compare-and-set pinned to the status
// and recommendations it saw. It returns nil for anything else, including a
// lost race.
func (m *Manager) failExpiredAutoRootTx(ctx context.Context, tx pgx.Tx, planID string) (*config.PurchaseExecution, error) {
	locked, err := m.config.LockPurchasePlanTx(ctx, tx, planID)
	if err != nil {
		return nil, err
	}
	if locked == nil || !locked.Enabled || !locked.AutoPurchase || locked.RampSchedule.DatesManaged {
		return nil, nil
	}
	rows, err := m.config.ListExecutionsForPlanStepTx(ctx, tx, planID, locked.RampSchedule.CurrentStep+1)
	if err != nil {
		return nil, err
	}
	root := readoptableExpiredRoot(rows)
	if root == nil {
		return nil, nil
	}
	return m.failExpiredRootTx(ctx, tx, root)
}

// failExpiredRootTx is the pinned expired-to-failed CAS plus the stamp and the
// suppression release, as failPlanStepTx does for a pending row.
func (m *Manager) failExpiredRootTx(ctx context.Context, tx pgx.Tx, root *config.PurchaseExecution) (*config.PurchaseExecution, error) {
	failed, err := m.config.TransitionExecutionStatusPinnedTx(ctx, tx, root.ExecutionID, []string{config.StatusExpired}, config.StatusFailed, nil, len(root.Recommendations) == 0)
	if errors.Is(err, config.ErrNotFound) || errors.Is(err, config.ErrExecutionNotInExpectedStatus) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	markFailedPlanStep(failed, errExpiredAutoRootNeedsRearm.Error())
	if err := m.config.SaveExecutionIfStatusTx(ctx, tx, failed, []string{config.StatusFailed}); err != nil {
		return nil, fmt.Errorf("failed to record plan step %d of plan %s as failed: %w", failed.StepNumber, root.PlanID, err)
	}
	if err := m.config.DeleteSuppressionsByExecutionTx(ctx, tx, failed.ExecutionID); err != nil {
		return nil, err
	}
	return failed, nil
}
