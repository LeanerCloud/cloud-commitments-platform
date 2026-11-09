//go:build integration
// +build integration

package config

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// linkUnsent inserts a pending successor and tries the proven-unsent link for
// predecessor in the same tx, as the Retry handler does.
func (f *rampFixture) linkUnsent(ctx context.Context, t *testing.T, predecessor *PurchaseExecution) bool {
	t.Helper()
	successor := &PurchaseExecution{ExecutionID: uuid.NewString(), PlanID: f.planID, Status: "pending", StepNumber: predecessor.StepNumber}
	var linked bool
	require.NoError(t, f.store.WithTx(ctx, func(tx pgx.Tx) error {
		require.NoError(t, f.store.SavePurchaseExecutionTx(ctx, tx, successor))
		var err error
		linked, err = f.store.LinkUnsentRetryExecutionAtomic(ctx, tx, predecessor.ExecutionID, successor.ExecutionID)
		return err
	}))
	return linked
}

func TestLinkUnsentRetryExecutionAtomic(t *testing.T) {
	ctx := context.Background()

	t.Run("links a no_submission attempt-0 failed row", func(t *testing.T) {
		f := managedSettleFixture(ctx, t)
		row := f.rowWithRecs(ctx, t, 1, &f.accounts[0], StatusFailed, true)
		require.True(t, f.linkUnsent(ctx, t, row))
		got, err := f.store.GetExecutionByID(ctx, row.ExecutionID)
		require.NoError(t, err)
		require.NotNil(t, got.RetryExecutionID)
	})

	t.Run("refuses a row that is not no_submission", func(t *testing.T) {
		f := managedSettleFixture(ctx, t)
		row := f.rowWithRecs(ctx, t, 1, &f.accounts[0], StatusFailed, false)
		require.False(t, f.linkUnsent(ctx, t, row))
	})

	t.Run("refuses a no_submission row past attempt 0", func(t *testing.T) {
		f := managedSettleFixture(ctx, t)
		row := f.rowWithRecs(ctx, t, 1, &f.accounts[0], StatusFailed, true)
		_, err := f.store.db.Exec(ctx, `UPDATE purchase_executions SET retry_attempt_n = 1 WHERE execution_id = $1`, row.ExecutionID)
		require.NoError(t, err)
		require.False(t, f.linkUnsent(ctx, t, row))
	})

	t.Run("refuses an already linked row", func(t *testing.T) {
		f := managedSettleFixture(ctx, t)
		row := f.rowWithRecs(ctx, t, 1, &f.accounts[0], StatusFailed, true)
		require.True(t, f.linkUnsent(ctx, t, row))
		require.False(t, f.linkUnsent(ctx, t, row))
	})

	t.Run("refuses a settled row", func(t *testing.T) {
		f := managedSettleFixture(ctx, t)
		row := f.rowWithRecs(ctx, t, 1, &f.accounts[0], StatusFailed, true)
		_, err := f.settle(ctx, row.ExecutionID, "operator checked the account")
		require.NoError(t, err)
		require.False(t, f.linkUnsent(ctx, t, row))
	})
}
