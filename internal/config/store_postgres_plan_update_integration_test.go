//go:build integration

package config

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestPurchasePlanUpdatesRejectStaleSnapshots(t *testing.T) {
	ctx := context.Background()
	store := setupRampStepStore(ctx, t)
	plan := &PurchasePlan{Name: "original", Services: map[string]ServiceConfig{}}
	require.NoError(t, store.CreatePurchasePlan(ctx, plan))
	createdVersion := plan.UpdatedAt
	plan.Name = "first edit"
	require.NoError(t, store.UpdatePurchasePlan(ctx, plan))
	require.True(t, plan.UpdatedAt.After(createdVersion))
	stale := *plan
	plan.Name = "second edit"
	require.NoError(t, store.UpdatePurchasePlan(ctx, plan))
	winner, err := store.GetPurchasePlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, plan.UpdatedAt, winner.UpdatedAt)
	require.Equal(t, "second edit", winner.Name)

	staleVersion := stale.UpdatedAt
	stale.Name = "stale edit"
	require.ErrorIs(t, store.UpdatePurchasePlan(ctx, &stale), ErrPurchasePlanConflict)
	require.Equal(t, staleVersion, stale.UpdatedAt)
	after, err := store.GetPurchasePlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, winner, after)
	stale.UpdatedAt = time.Time{}
	require.ErrorIs(t, store.UpdatePurchasePlan(ctx, &stale), ErrPurchasePlanConflict)
	stale.ID = uuid.NewString()
	require.ErrorIs(t, store.UpdatePurchasePlan(ctx, &stale), ErrNotFound)

	stamp := time.Now().Truncate(time.Microsecond)
	require.NoError(t, store.StampPlanNotificationSent(ctx, plan.ID, stamp))
	require.ErrorIs(t, store.UpdatePurchasePlan(ctx, plan), ErrPurchasePlanConflict)
	after, err = store.GetPurchasePlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, &stamp, after.LastNotificationSent)
	require.Equal(t, winner.Name, after.Name)
	require.True(t, after.UpdatedAt.After(winner.UpdatedAt))
	require.ErrorIs(t, store.StampPlanNotificationSent(ctx, uuid.NewString(), stamp), ErrNotFound)
}

func TestPurchasePlanUpdateTxRollsBackWithExecutions(t *testing.T) {
	ctx := context.Background()
	store := setupRampStepStore(ctx, t)
	plan := &PurchasePlan{Name: "original", Services: map[string]ServiceConfig{}}
	require.NoError(t, store.CreatePurchasePlan(ctx, plan))
	before, err := store.GetPurchasePlan(ctx, plan.ID)
	require.NoError(t, err)
	executionID := uuid.NewString()
	abort := errors.New("abort fixture transaction")
	err = store.WithTx(ctx, func(tx pgx.Tx) error {
		locked, lockErr := store.LockPurchasePlanTx(ctx, tx, plan.ID)
		require.NoError(t, lockErr)
		locked.Name = "rolled back"
		require.NoError(t, store.UpdatePurchasePlanTx(ctx, tx, locked))
		require.True(t, locked.UpdatedAt.After(before.UpdatedAt))
		locked.Name = "second transaction-local write"
		require.NoError(t, store.UpdatePurchasePlanTx(ctx, tx, locked))
		require.NoError(t, store.SavePurchaseExecutionTx(ctx, tx, &PurchaseExecution{
			ExecutionID: executionID, PlanID: plan.ID, Status: "pending", ScheduledDate: time.Now(),
		}))
		return abort
	})
	require.ErrorIs(t, err, abort)
	after, err := store.GetPurchasePlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = store.GetExecutionByID(ctx, executionID)
	require.ErrorIs(t, err, ErrNotFound)
}
