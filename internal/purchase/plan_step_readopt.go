package purchase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// expiredRootStore is the optional store capability behind re-arming an expired
// plan-step root once (#1785); the managedPlanRootStore pattern, so a store
// without it (a mock) simply never re-arms.
type expiredRootStore interface {
	ListPlansWithReadoptableExpiredRoot(ctx context.Context, now time.Time, requireDifferentApprover bool) (map[string]struct{}, error)
	ReadoptExpiredRootTx(ctx context.Context, tx pgx.Tx, executionID string, scheduledDate, now time.Time, requireDifferentApprover bool) (*config.PurchaseExecution, error)
}

// readoptCandidates reads the plans that may have an expired root to re-arm. It
// is only a tick trigger (the claim re-derives every gate under the locks), and
// a read error is logged and skipped so it can never block normal notifications.
func (m *Manager) readoptCandidates(ctx context.Context, global *config.GlobalConfig, now time.Time) map[string]struct{} {
	store, ok := m.config.(expiredRootStore)
	if !ok || global == nil {
		return nil
	}
	plans, err := store.ListPlansWithReadoptableExpiredRoot(ctx, now, global.RequireDifferentApprover)
	if err != nil {
		logging.Errorf("Failed to list expired plan-step roots to re-arm: %v", err)
		return nil
	}
	return plans
}

// shouldReadoptExpiredPlan admits a plan whose step holds a re-armable expired
// root. Only plans that wait for a human approval qualify: re-arming an
// auto-purchase plan with 4-eyes off would auto-buy fresh recommendations with
// the email as the only veto. A nil global fails closed (planNeedsApproval
// itself reads nil as "needs approval"). Dates-managed plans follow the
// schedule clock and are out of scope.
func shouldReadoptExpiredPlan(plan config.PurchasePlan, global *config.GlobalConfig, autoPurchaseEnabled, listed bool, now time.Time) bool {
	if !listed || global == nil || !readoptPlanEligible(plan) {
		return false
	}
	if (plan.AutoPurchase && !autoPurchaseEnabled) || !planNeedsApproval(plan, global) {
		return false
	}
	return plan.LastNotificationSent == nil || now.Sub(*plan.LastNotificationSent).Hours() >= config.MinHoursBetweenNotifications
}

func readoptPlanEligible(plan config.PurchasePlan) bool {
	return plan.Enabled && !plan.RampSchedule.DatesManaged && plan.NextExecutionDate != nil && plan.RequireVetoWindow() == nil
}

// readoptableExpiredRoot returns the step's only row when it is an expired root
// that was never approved, executed or bought. Any sibling (a failed child, a
// canceled root, a retry) means the step may already have been bought, so it is
// not adopted. Recency and ladder links are checked by the store CAS, which is
// the backstop: the struct does not carry them.
func readoptableExpiredRoot(rows []config.PurchaseExecution) *config.PurchaseExecution {
	if len(rows) != 1 {
		return nil
	}
	r := rows[0]
	if r.Status != config.StatusExpired || !isRootShape(&r) || wasActedOn(&r) || hasPurchasedRec(&r) {
		return nil
	}
	return &r
}

func isRootShape(r *config.PurchaseExecution) bool {
	return r.CloudAccountID == nil && r.RetryAttemptN == 0 && r.RetryExecutionID == nil
}

func wasActedOn(r *config.PurchaseExecution) bool {
	return r.ApprovedBy != nil || r.ExecutedAt != nil || r.CompletedAt != nil || r.ScheduledExecutionAt != nil || r.PreApprovalSkipReason != nil
}

func hasPurchasedRec(r *config.PurchaseExecution) bool {
	for i := range r.Recommendations {
		if r.Recommendations[i].Purchased || r.Recommendations[i].PurchaseID != "" {
			return true
		}
	}
	return false
}

// tryReadoptExpired runs inside the claim transaction, under the provider and
// plan locks. For a step whose only row is a re-armable expired root it re-arms
// the root with a compare-and-set, then attaches fresh recommendations through
// attachStep directly (the date-before-veto-window skip in claimFromRows would
// trip on the new date). handled is false when the step stays blocked as
// before. Every gate is re-derived here from the locked plan and rows: the
// tick's list is only a trigger. A lost CAS, an unmigrated schema or a no-match
// before the grace deadline is the quiet skip, and the rollback leaves the root
// expired and unmarked for the next tick.
func (m *Manager) tryReadoptExpired(ctx context.Context, tx pgx.Tx, locked *config.PurchasePlan, claim stepClaim, rows []config.PurchaseExecution) (stepClaim, bool, error) {
	store, ok := m.config.(expiredRootStore)
	root := readoptableExpiredRoot(rows)
	if !ok || root == nil {
		return claim, false, nil
	}
	global, err := m.config.GetGlobalConfig(ctx)
	if err != nil {
		return stepClaim{}, true, fmt.Errorf("failed to read global config to re-arm step %d of plan %s: %w", claim.number, locked.ID, err)
	}
	now := time.Now()
	if global == nil || !shouldReadoptExpiredPlan(*locked, global, global.AutoPurchaseEnabled, true, now) {
		return claim, false, nil
	}
	adopted, err := store.ReadoptExpiredRootTx(ctx, tx, root.ExecutionID, attachScheduledDate(locked, now), now, global.RequireDifferentApprover)
	if errors.Is(err, config.ErrReadoptNotReady) || errors.Is(err, config.ErrExecutionNotInExpectedStatus) {
		return stepClaim{}, true, fmt.Errorf("%w: expired root %s of plan %s is not re-armable: %w", errPlanStepSkipped, root.ExecutionID, locked.ID, err)
	}
	if err != nil {
		return stepClaim{}, true, fmt.Errorf("failed to re-arm expired root %s of plan %s: %w", root.ExecutionID, locked.ID, err)
	}
	claim, err = m.attachStep(ctx, tx, locked, claim, adopted)
	return claim, true, err
}
