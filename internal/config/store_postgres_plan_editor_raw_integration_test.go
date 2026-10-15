//go:build integration

package config

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestPlanEditorSnapshotUsesCallerTransaction(t *testing.T) {
	ctx := context.Background()
	f := newRampFixture(ctx, t, 1, 0, 4)
	before, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	abort := errors.New("fixture rollback")
	err = f.store.WithTx(ctx, func(tx pgx.Tx) error {
		_, lockErr := f.store.LockPurchasePlanTx(ctx, tx, f.planID)
		require.NoError(t, lockErr)
		_, writeErr := tx.Exec(ctx, `UPDATE purchase_plans SET name='transaction-only',services='{"future":{"nested":[null,1]}}' WHERE id=$1`, f.planID)
		require.NoError(t, writeErr)
		captured, readErr := f.store.readPlanEditorSnapshotTx(ctx, tx, f.planID)
		require.NoError(t, readErr)
		require.Equal(t, "transaction-only", captured.Plan.Name)
		require.JSONEq(t, `{"future":{"nested":[null,1]}}`, string(captured.servicesJSON))
		var ramp string
		require.NoError(t, tx.QueryRow(ctx, `SELECT ramp_schedule::text FROM purchase_plans WHERE id=$1`, f.planID).Scan(&ramp))
		require.JSONEq(t, ramp, string(captured.rampScheduleJSON))
		return abort
	})
	require.ErrorIs(t, err, abort)
	after, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
