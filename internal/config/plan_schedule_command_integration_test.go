//go:build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newScheduleCommandPlan(ctx context.Context, t *testing.T) (*PostgresStore, *PurchasePlan) {
	t.Helper()
	store := setupRampStepStore(ctx, t)
	plan := &PurchasePlan{Name: "original", Services: map[string]ServiceConfig{}}
	require.NoError(t, store.CreatePurchasePlan(ctx, plan))
	return store, plan
}

func scheduleCommand(plan *PurchasePlan, name string) PlanScheduleCommand {
	return PlanScheduleCommand{
		CommandID: uuid.NewString(), PlanID: plan.ID, Actor: "user-1", RequestHash: "hash-" + name,
		ExpectedUpdatedAt: plan.UpdatedAt, Name: &name,
	}
}

func receiptCount(ctx context.Context, t *testing.T, store *PostgresStore) int {
	t.Helper()
	var n int
	require.NoError(t, store.db.QueryRow(ctx, `SELECT count(*) FROM plan_schedule_command_receipts`).Scan(&n))
	return n
}

func TestApplyPlanScheduleCommandWritesPlanAndReceipt(t *testing.T) {
	ctx := context.Background()
	store, plan := newScheduleCommandPlan(ctx, t)
	next := time.Now().Add(48 * time.Hour).Truncate(time.Microsecond)
	cmd := scheduleCommand(plan, "renamed")
	cmd.NextExecutionDate = &next

	out, err := store.ApplyPlanScheduleCommand(ctx, cmd)
	require.NoError(t, err)
	require.False(t, out.Replayed)
	got, err := store.GetPurchasePlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed", got.Name)
	require.Equal(t, &next, got.NextExecutionDate)
	require.Equal(t, got.UpdatedAt, out.UpdatedAt)
	require.Equal(t, 1, receiptCount(ctx, t, store))
}

func TestApplyPlanScheduleCommandReplayWritesNothing(t *testing.T) {
	ctx := context.Background()
	store, plan := newScheduleCommandPlan(ctx, t)
	cmd := scheduleCommand(plan, "once")
	first, err := store.ApplyPlanScheduleCommand(ctx, cmd)
	require.NoError(t, err)

	replay, err := store.ApplyPlanScheduleCommand(ctx, cmd)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, first.UpdatedAt, replay.UpdatedAt)
	got, err := store.GetPurchasePlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, first.UpdatedAt, got.UpdatedAt, "a replay must not write the plan again")
	require.Equal(t, 1, receiptCount(ctx, t, store))

	other := cmd
	other.RequestHash = "different"
	_, err = store.ApplyPlanScheduleCommand(ctx, other)
	require.ErrorIs(t, err, ErrPlanScheduleCommandConflict)
}

func TestApplyPlanScheduleCommandStaleVersionConflicts(t *testing.T) {
	ctx := context.Background()
	store, plan := newScheduleCommandPlan(ctx, t)
	stale := scheduleCommand(plan, "stale")
	_, err := store.ApplyPlanScheduleCommand(ctx, scheduleCommand(plan, "winner"))
	require.NoError(t, err)

	_, err = store.ApplyPlanScheduleCommand(ctx, stale)
	require.ErrorIs(t, err, ErrPurchasePlanConflict)
	got, err := store.GetPurchasePlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, "winner", got.Name)
	require.Equal(t, 1, receiptCount(ctx, t, store), "a refused command leaves no receipt")

	missing := scheduleCommand(plan, "gone")
	missing.PlanID = uuid.NewString()
	_, err = store.ApplyPlanScheduleCommand(ctx, missing)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestApplyPlanScheduleCommandBusyPlanIsRetryable(t *testing.T) {
	ctx := context.Background()
	store, plan := newScheduleCommandPlan(ctx, t)
	prev := planScheduleLockTimeout
	planScheduleLockTimeout = 150 * time.Millisecond
	t.Cleanup(func() { planScheduleLockTimeout = prev })

	holder, err := store.db.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Rollback(ctx) })
	_, err = store.LockPurchasePlanTx(ctx, holder, plan.ID)
	require.NoError(t, err)

	start := time.Now()
	_, err = store.ApplyPlanScheduleCommand(ctx, scheduleCommand(plan, "blocked"))
	require.ErrorIs(t, err, ErrPlanScheduleRetryable)
	require.Less(t, time.Since(start), 5*time.Second)
	require.NoError(t, holder.Rollback(ctx))
	require.Equal(t, 0, receiptCount(ctx, t, store))
}

func TestApplyPlanScheduleCommandIDReusedOnOtherPlanConflicts(t *testing.T) {
	ctx := context.Background()
	store, plan := newScheduleCommandPlan(ctx, t)
	otherPlan := &PurchasePlan{Name: "other", Services: map[string]ServiceConfig{}}
	require.NoError(t, store.CreatePurchasePlan(ctx, otherPlan))
	cmd := scheduleCommand(plan, "first")
	_, err := store.ApplyPlanScheduleCommand(ctx, cmd)
	require.NoError(t, err)

	reused := scheduleCommand(otherPlan, "first")
	reused.CommandID = cmd.CommandID
	_, err = store.ApplyPlanScheduleCommand(ctx, reused)
	require.ErrorIs(t, err, ErrPlanScheduleCommandConflict)
	got, err := store.GetPurchasePlan(ctx, otherPlan.ID)
	require.NoError(t, err)
	require.Equal(t, "other", got.Name)
}

func TestApplyPlanScheduleCommandGroupTargetKeepsOtherGroup(t *testing.T) {
	ctx := context.Background()
	store := setupRampStepStore(ctx, t)
	plan := boundTwoGroupPlan()
	plan.Name = "bound"
	require.NoError(t, store.CreatePurchasePlan(ctx, plan))
	cmd := scheduleCommand(plan, "bound")
	cmd.GroupTarget = &PlanGroupTarget{GroupID: "b", MaxStepCommitment: fp(150)}

	_, err := store.ApplyPlanScheduleCommand(ctx, cmd)
	require.NoError(t, err)
	got, err := store.GetPurchasePlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, 150.0, *got.Services["aws/rds#b"].MaxStepCommitment)
	require.Equal(t, 100.0, *got.Services["aws/ec2#a"].MaxStepCommitment)
	require.Equal(t, RampAllocationBoundGroups, got.RampSchedule.AllocationMode)

	raise := scheduleCommand(got, "raise")
	raise.GroupTarget = &PlanGroupTarget{GroupID: "b", MaxStepCommitment: fp(151)}
	_, err = store.ApplyPlanScheduleCommand(ctx, raise)
	require.ErrorIs(t, err, ErrGroupTargetRefused)
	require.Equal(t, 1, receiptCount(ctx, t, store), "a refused edit leaves no receipt")
}

func TestApplyPlanScheduleCommandGroupTargetDuplicateWritesNothing(t *testing.T) {
	ctx := context.Background()
	store := setupRampStepStore(ctx, t)
	plan := boundTwoGroupPlan()
	plan.Name = "dup"
	plan.Services["aws:ec2#a"] = ServiceConfig{Provider: "aws", Service: "ec2", GroupID: "a", Enabled: true, MaxStepCommitment: fp(100)}
	require.NoError(t, store.CreatePurchasePlan(ctx, plan))
	cmd := scheduleCommand(plan, "dup")
	cmd.GroupTarget = &PlanGroupTarget{GroupID: "a", MaxStepCommitment: fp(50)}

	_, err := store.ApplyPlanScheduleCommand(ctx, cmd)
	require.ErrorIs(t, err, ErrGroupTargetRefused)
	got, err := store.GetPurchasePlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, 100.0, *got.Services["aws/ec2#a"].MaxStepCommitment)
	require.Equal(t, 0, receiptCount(ctx, t, store))
}
