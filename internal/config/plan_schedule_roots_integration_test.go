//go:build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func managedRootFixture(ctx context.Context, t *testing.T, status string) (*rampFixture, *PurchaseExecution, *PurchasePlan) {
	t.Helper()
	f := newRampFixture(ctx, t, 1, 1, 3)
	_, err := f.store.db.Exec(ctx, `UPDATE purchase_plans SET ramp_schedule = ramp_schedule || '{"dates_managed":true}' WHERE id = $1`, f.planID)
	require.NoError(t, err)
	root := f.row(ctx, t, 2, nil, status)
	plan, err := f.store.GetPurchasePlan(ctx, f.planID)
	require.NoError(t, err)
	return f, root, plan
}

func rootCommand(plan *PurchasePlan, date time.Time) PlanScheduleCommand {
	return PlanScheduleCommand{
		CommandID: uuid.NewString(), PlanID: plan.ID, Actor: "user-1", RequestHash: "h-" + date.String(),
		ExpectedUpdatedAt: plan.UpdatedAt, ReplaceRootDate: &date,
	}
}

func stepRows(ctx context.Context, t *testing.T, f *rampFixture) []PurchaseExecution {
	t.Helper()
	var rows []PurchaseExecution
	require.NoError(t, f.store.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		rows, err = f.store.ListExecutionsForPlanStepTx(ctx, tx, f.planID, 2)
		return err
	}))
	return rows
}

func TestApplyPlanScheduleCommandReplacesRoot(t *testing.T) {
	ctx := context.Background()
	f, old, plan := managedRootFixture(ctx, t, "pending")
	date := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 30)

	out, err := f.store.ApplyPlanScheduleCommand(ctx, rootCommand(plan, date))
	require.NoError(t, err)

	rows := stepRows(ctx, t, f)
	require.Len(t, rows, 2)
	byID := map[string]PurchaseExecution{}
	for _, r := range rows {
		byID[r.ExecutionID] = r
	}
	require.Equal(t, StatusCanceled, byID[old.ExecutionID].Status)
	require.NotEmpty(t, out.RootExecutionID)
	fresh := byID[out.RootExecutionID]
	require.Equal(t, "pending", fresh.Status)
	require.True(t, date.Equal(fresh.ScheduledDate))
	got, err := f.store.GetPurchasePlan(ctx, f.planID)
	require.NoError(t, err)
	require.True(t, date.Equal(*got.NextExecutionDate))
	var resolved *PurchaseExecution
	require.NoError(t, f.store.WithTx(ctx, func(tx pgx.Tx) error {
		var e error
		resolved, e = f.store.ResolveManagedPlanRootTx(ctx, tx, got)
		return e
	}))
	require.Equal(t, out.RootExecutionID, resolved.ExecutionID)
}

func TestApplyPlanScheduleCommandRefusesUncancellableRoot(t *testing.T) {
	for _, status := range []string{"approved", "running", StatusCompleted, StatusFailed, "expired"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			f, old, plan := managedRootFixture(ctx, t, status)

			_, err := f.store.ApplyPlanScheduleCommand(ctx, rootCommand(plan, time.Now().AddDate(0, 0, 9)))
			require.ErrorIs(t, err, ErrExecutionNotInExpectedStatus)

			rows := stepRows(ctx, t, f)
			require.Len(t, rows, 1)
			require.Equal(t, old.ExecutionID, rows[0].ExecutionID)
			require.Equal(t, status, rows[0].Status)
			require.Equal(t, 0, receiptCount(ctx, t, f.store))
		})
	}
}

func TestApplyPlanScheduleCommandRootReplaceIsAtomicWithAccounts(t *testing.T) {
	ctx := context.Background()
	f, old, plan := managedRootFixture(ctx, t, "pending")
	cmd := rootCommand(plan, time.Now().AddDate(0, 0, 9))
	name := "renamed"
	cmd.Name = &name
	missing := []string{uuid.NewString()}
	cmd.Accounts = &missing

	_, err := f.store.ApplyPlanScheduleCommand(ctx, cmd)
	require.Error(t, err)

	rows := stepRows(ctx, t, f)
	require.Len(t, rows, 1)
	require.Equal(t, old.ExecutionID, rows[0].ExecutionID)
	require.Equal(t, "pending", rows[0].Status)
	got, err := f.store.GetPurchasePlan(ctx, f.planID)
	require.NoError(t, err)
	require.NotEqual(t, name, got.Name)
	accounts, err := f.store.GetPlanAccounts(ctx, f.planID)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, 0, receiptCount(ctx, t, f.store))
}

func TestApplyPlanScheduleCommandAdoptsManagementAndSetsAccounts(t *testing.T) {
	ctx := context.Background()
	f := newRampFixture(ctx, t, 2, 1, 3)
	f.row(ctx, t, 2, nil, "pending")
	plan, err := f.store.GetPurchasePlan(ctx, f.planID)
	require.NoError(t, err)
	managed := true
	keep := f.accounts[:1]
	date := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 21)
	cmd := rootCommand(plan, date)
	cmd.DatesManaged, cmd.Accounts = &managed, &keep

	_, err = f.store.ApplyPlanScheduleCommand(ctx, cmd)
	require.NoError(t, err)

	got, err := f.store.GetPurchasePlan(ctx, f.planID)
	require.NoError(t, err)
	require.True(t, got.RampSchedule.DatesManaged)
	accounts, err := f.store.GetPlanAccounts(ctx, f.planID)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, keep[0], accounts[0].ID)

	// Once managed, the next command moves the root and the accounts again
	// although the legacy guard refuses both.
	back := f.accounts
	again := rootCommand(got, date.AddDate(0, 0, 7))
	again.Accounts = &back
	_, err = f.store.ApplyPlanScheduleCommand(ctx, again)
	require.NoError(t, err)
	accounts, err = f.store.GetPlanAccounts(ctx, f.planID)
	require.NoError(t, err)
	require.Len(t, accounts, 2)
}

func TestApplyPlanScheduleCommandRefusesRootOnUnmanagedPlan(t *testing.T) {
	ctx := context.Background()
	f := newRampFixture(ctx, t, 0, 1, 3)
	old := f.row(ctx, t, 2, nil, "pending")
	plan, err := f.store.GetPurchasePlan(ctx, f.planID)
	require.NoError(t, err)

	_, err = f.store.ApplyPlanScheduleCommand(ctx, rootCommand(plan, time.Now().AddDate(0, 0, 9)))
	require.ErrorIs(t, err, ErrPurchasePlanConflict)
	rows := stepRows(ctx, t, f)
	require.Len(t, rows, 1)
	require.Equal(t, old.ExecutionID, rows[0].ExecutionID)
	require.Equal(t, "pending", rows[0].Status)
}

func TestApplyPlanScheduleCommandRefusesPendingPerAccountRow(t *testing.T) {
	ctx := context.Background()
	f, root, plan := managedRootFixture(ctx, t, "pending")
	fanOut := f.row(ctx, t, 2, &f.accounts[0], "pending")

	_, err := f.store.ApplyPlanScheduleCommand(ctx, rootCommand(plan, time.Now().AddDate(0, 0, 9)))
	require.ErrorIs(t, err, ErrExecutionNotInExpectedStatus)

	rows := stepRows(ctx, t, f)
	require.Len(t, rows, 2)
	for _, r := range rows {
		require.Equal(t, "pending", r.Status, "%s must stay untouched", r.ExecutionID)
	}
	require.ElementsMatch(t, []string{root.ExecutionID, fanOut.ExecutionID}, []string{rows[0].ExecutionID, rows[1].ExecutionID})
}
