//go:build integration

package purchase

// Real PostgreSQL plus the ramp-step fixture's counting fake provider (labeled
// fixture; nothing here reaches a cloud API). Issue #1048: purchase_executions
// .plan_id is ON DELETE SET NULL, so deleting a plan leaves its scheduled rows
// plan-less, and #1046 cancels only the open ones. A row of a deleted plan must
// end failed and unbought, whichever way it reaches the executor, while an
// ad-hoc direct-execute row and a live plan's step still buy.

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func planStepRow1048(ctx context.Context, t *testing.T, f *rampStepFixture, planID, source, status string) *config.PurchaseExecution {
	t.Helper()
	stepNo := 3
	if planID == "" {
		stepNo = 0 // a direct-execute row stores no step
	}
	due := time.Now().Add(-time.Minute)
	row := &config.PurchaseExecution{
		PlanID: planID, ExecutionID: uuid.New().String(), Status: status, StepNumber: stepNo, Source: source,
		ScheduledDate: due, CloudAccountID: &f.accounts[0].ID, CreatedBySystem: source == "",
		Recommendations: []config.RecommendationRecord{withAccount(rampStepRecommendation()[0], f.accounts[0].ID)},
	}
	if status == "scheduled" {
		row.ScheduledDate = time.Now()
		row.ScheduledExecutionAt = &due
	}
	require.NoError(t, f.store.SavePurchaseExecution(ctx, row))
	return row
}

func requirePlanGoneUnbought(ctx context.Context, t *testing.T, f *rampStepFixture, id string) {
	t.Helper()
	got := rowStatus(ctx, t, f, id)
	assert.Equal(t, config.StatusFailed, got.Status)
	assert.True(t, got.NoSubmission)
	assert.Contains(t, got.Error, PlanGoneRefusal)
}

// Case 2: a scheduled row survives its plan's deletion and must not fire.
func TestOrphanedPlanStep_ScheduledRowOfDeletedPlanDoesNotFire(t *testing.T) {
	ctx := context.Background()
	f := newRampStepFixture(ctx, t)
	f.repairAccounts(ctx, t)
	emails := countFailureEmails(f)
	orphan := planStepRow1048(ctx, t, f, f.plan.ID, "", "scheduled")
	adHoc := planStepRow1048(ctx, t, f, "", common.PurchaseSourceWeb, "scheduled")

	require.NoError(t, f.store.DeletePurchasePlan(ctx, f.plan.ID))
	require.Empty(t, rowStatus(ctx, t, f, orphan.ExecutionID).PlanID, "the delete must have nulled plan_id")

	res, err := f.manager.FireScheduledDelayedPurchases(ctx)

	require.NoError(t, err)
	assert.Equal(t, 1, res.Fired)
	assert.Equal(t, 1, f.purchaseCount(), "only the ad-hoc row buys")
	requirePlanGoneUnbought(ctx, t, f, orphan.ExecutionID)
	assert.Equal(t, 1, emails()[orphan.ExecutionID], "operators hear about the refusal")
	assert.Equal(t, "completed", rowStatus(ctx, t, f, adHoc.ExecutionID).Status)
}

// Case 1: an open row orphaned before #1046 (plan_id nulled by the FK with the
// row left open), approved from the approval queue.
func TestOrphanedPlanStep_PreFix1046OpenRowCannotBeApproved(t *testing.T) {
	ctx := context.Background()
	f := newRampStepFixture(ctx, t)
	f.repairAccounts(ctx, t)
	countFailureEmails(f)
	orphan := planStepRow1048(ctx, t, f, f.plan.ID, "", "notified")
	_, err := f.pool.Exec(ctx, `UPDATE purchase_executions SET plan_id = NULL WHERE execution_id = $1`, orphan.ExecutionID)
	require.NoError(t, err)

	_, _, err = f.manager.ApproveAndExecute(ctx, orphan.ExecutionID, "approver@example.invalid", nil, nil)

	require.Error(t, err)
	assert.Zero(t, f.purchaseCount())
	requirePlanGoneUnbought(ctx, t, f, orphan.ExecutionID)
}

func TestOrphanedPlanStep_LivePlanStepStillFires(t *testing.T) {
	ctx := context.Background()
	f := newRampStepFixture(ctx, t)
	f.repairAccounts(ctx, t)
	live := planStepRow1048(ctx, t, f, f.plan.ID, "", "scheduled")

	res, err := f.manager.FireScheduledDelayedPurchases(ctx)

	require.NoError(t, err)
	assert.Equal(t, 1, res.Fired)
	assert.Equal(t, 1, f.purchaseCount())
	assert.Equal(t, "completed", rowStatus(ctx, t, f, live.ExecutionID).Status)
}
