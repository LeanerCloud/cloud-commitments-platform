//go:build integration
// +build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_GetExecutionsByStatuses_RowClasses runs the real query over
// each row class the History view cares about. A clean completed execution
// (status completed, empty error) must be excluded; a completed execution with
// an error (the audit-gap case) and every other requested status must be
// returned, newest first and bounded by the limit.
func TestIntegration_GetExecutionsByStatuses_RowClasses(t *testing.T) {
	ctx := context.Background()
	store := setupRampStepStore(ctx, t)

	base := time.Now().Add(-time.Hour)
	save := func(offset int, status, execErr string) string {
		exec := &PurchaseExecution{
			ExecutionID:    uuid.New().String(),
			IdempotencyKey: uuid.New().String(),
			Status:         status,
			Error:          execErr,
			ScheduledDate:  base.Add(time.Duration(offset) * time.Minute),
		}
		require.NoError(t, store.SavePurchaseExecution(ctx, exec))
		return exec.ExecutionID
	}

	save(1, "completed", "")
	auditGap := save(2, "completed", "history write failed")
	failed := save(3, "failed", "boom")
	pending := save(4, "pending", "")

	got, err := store.GetExecutionsByStatuses(ctx, []string{"pending", "completed", "failed"}, 100)
	require.NoError(t, err)
	ids := make([]string, len(got))
	for i := range got {
		ids[i] = got[i].ExecutionID
	}
	assert.Equal(t, []string{pending, failed, auditGap}, ids, "clean completed excluded; newest first")

	limited, err := store.GetExecutionsByStatuses(ctx, []string{"pending", "completed", "failed"}, 2)
	require.NoError(t, err)
	require.Len(t, limited, 2)
	assert.Equal(t, pending, limited[0].ExecutionID)
	assert.Equal(t, failed, limited[1].ExecutionID)
}
