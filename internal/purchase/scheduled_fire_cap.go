package purchase

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// scheduledRowCapRefusal re-applies the plan's step cap to a scheduled
// (delayed-approval) row the sweep has just claimed (#1933). Pending and
// notified rows get this check after their claim (claimedRowRefusal); a
// scheduled row was approved by a human but may fire long after, so a cap the
// plan owner lowered in between must still stop it. It applies only where
// StepCapApplies does, so a manual plan with no cap keeps firing at the
// approved amount, and a row without recommendations or a plan (a direct
// execute) is not this check's concern. A plan that cannot be read refuses,
// as recheckStepCap does.
func (m *Manager) scheduledRowCapRefusal(ctx context.Context, exec *config.PurchaseExecution) error {
	if exec.PlanID == "" || len(exec.Recommendations) == 0 {
		return nil
	}
	plan, err := m.config.GetPurchasePlan(ctx, exec.PlanID)
	if err != nil {
		return fmt.Errorf("failed to re-read plan %s for the scheduled-fire cap check: %w", exec.PlanID, err)
	}
	if !StepCapApplies(plan) {
		return nil
	}
	return CheckStepCommitmentCap(plan, exec.Recommendations)
}
