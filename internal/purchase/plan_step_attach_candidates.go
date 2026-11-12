package purchase

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// attachableRootStore is the optional store capability that lets the tick
// select a plan by its empty root's own date (#1755); a store without it (a
// mock) keeps selecting by the plan's next date only.
type attachableRootStore interface {
	ListPlansWithAttachableEmptyRoot(ctx context.Context, now time.Time) (map[string]struct{}, error)
}

// attachCandidates reads the plans whose next empty root is inside the attach
// window. It is only a tick trigger (the claim re-derives every gate under the
// locks), and a read error is logged and skipped so it can never block normal
// notifications.
func (m *Manager) attachCandidates(ctx context.Context, now time.Time) map[string]struct{} {
	store, ok := m.config.(attachableRootStore)
	if !ok {
		return nil
	}
	plans, err := store.ListPlansWithAttachableEmptyRoot(ctx, now)
	if err != nil {
		logging.Errorf("Failed to list plans with an attachable empty root: %v", err)
		return nil
	}
	return plans
}

// shouldAttachEmptyRoot admits a plan listed for its empty root's own date, so
// a missed tick cannot strand the root whatever the plan's next date says. The
// enabled, kill-switch and 24h-throttle rules are the ones shouldNotifyPlan
// applies.
func shouldAttachEmptyRoot(plan config.PurchasePlan, autoPurchaseEnabled, listed bool, now time.Time) bool {
	if !listed || !plan.Enabled || (plan.AutoPurchase && !autoPurchaseEnabled) || plan.NextExecutionDate == nil {
		return false
	}
	return plan.LastNotificationSent == nil || now.Sub(*plan.LastNotificationSent).Hours() >= config.MinHoursBetweenNotifications
}

// rootCandidates holds the plans the store listed for a root-driven rule: an
// expired root to re-arm, or an empty root inside the attach window.
type rootCandidates struct {
	readopt, attachable map[string]struct{}
}

// due reports whether either root-driven rule admits the plan to this tick.
func (c rootCandidates) due(plan config.PurchasePlan, global *config.GlobalConfig, autoPurchaseEnabled bool, now time.Time) bool {
	_, readopt := c.readopt[plan.ID]
	_, attach := c.attachable[plan.ID]
	return shouldReadoptExpiredPlan(plan, global, autoPurchaseEnabled, readopt, now) ||
		shouldAttachEmptyRoot(plan, autoPurchaseEnabled, attach, now)
}
