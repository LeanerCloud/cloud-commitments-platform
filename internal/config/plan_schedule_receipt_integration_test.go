//go:build integration

package config

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestPlanScheduleReceipts(t *testing.T) {
	ctx := context.Background()
	store := setupRampStepStore(ctx, t)
	plan := &PurchasePlan{Name: "receipts", Services: map[string]ServiceConfig{}}
	require.NoError(t, store.CreatePurchasePlan(ctx, plan))
	receipt := &PlanScheduleReceipt{
		CommandID: uuid.NewString(), PlanID: plan.ID, Actor: "user-1",
		RequestHash: "h1", Outcome: []byte(`{"ok":true}`),
	}

	require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
		missing, err := store.GetPlanScheduleReceiptTx(ctx, tx, receipt.CommandID)
		require.NoError(t, err)
		require.Nil(t, missing)
		return store.InsertPlanScheduleReceiptTx(ctx, tx, receipt)
	}))

	require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
		got, err := store.GetPlanScheduleReceiptTx(ctx, tx, receipt.CommandID)
		require.NoError(t, err)
		require.Equal(t, "h1", got.RequestHash)
		require.JSONEq(t, `{"ok":true}`, string(got.Outcome))
		require.Equal(t, plan.ID, got.PlanID)
		require.False(t, got.CreatedAt.IsZero())
		return nil
	}))

	dup := *receipt
	require.ErrorIs(t, store.WithTx(ctx, func(tx pgx.Tx) error {
		return store.InsertPlanScheduleReceiptTx(ctx, tx, &dup)
	}), ErrPlanScheduleReceiptExists)

	_, err := store.db.Exec(ctx, `UPDATE plan_schedule_command_receipts SET actor = 'x'`)
	require.ErrorContains(t, err, "immutable")
	_, err = store.db.Exec(ctx, `DELETE FROM plan_schedule_command_receipts`)
	require.ErrorContains(t, err, "immutable")

	require.NoError(t, store.DeletePurchasePlan(ctx, plan.ID))
	var n int
	require.NoError(t, store.db.QueryRow(ctx, `SELECT count(*) FROM plan_schedule_command_receipts`).Scan(&n))
	require.Zero(t, n, "deleting the plan cascades its receipts")
}
