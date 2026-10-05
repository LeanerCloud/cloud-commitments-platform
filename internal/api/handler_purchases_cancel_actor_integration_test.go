//go:build integration

package api

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestDeletePlannedPurchase_RecordsCancelActor is the regression test for
// issue #119: canceling a planned purchase from the UI must leave the
// canceling user readable through the canceled_by projection every reader
// uses, not only in the unread transitioned_by column.
func TestDeletePlannedPurchase_RecordsCancelActor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	f := newCreateConcurrencyFixture(ctx, t)

	var creator string
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT id::text FROM users WHERE email='creator@example.com'").Scan(&creator))
	execID := uuid.NewString()
	require.NoError(t, f.store.SavePurchaseExecution(ctx, &config.PurchaseExecution{
		ExecutionID: execID, PlanID: f.planID, Status: "pending", StepNumber: 3,
		ScheduledDate: time.Now(), CreatedByUserID: &creator,
	}))

	req := &events.LambdaFunctionURLRequest{Headers: map[string]string{"Authorization": "Bearer admin-token"}}
	_, err := f.handler.deletePlannedPurchase(ctx, req, execID)
	require.NoError(t, err)

	row, err := f.store.GetExecutionByID(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, config.StatusCanceled, row.Status)
	require.NotNil(t, row.CancelledBy, "cancel actor must be readable after a UI cancel")
	require.Equal(t, "creator@example.com", *row.CancelledBy)

	// The record TransitionExecutionStatus returns must use the same projection.
	directID := uuid.NewString()
	require.NoError(t, f.store.SavePurchaseExecution(ctx, &config.PurchaseExecution{
		ExecutionID: directID, PlanID: f.planID, Status: "pending", StepNumber: 3,
		ScheduledDate: time.Now(), CreatedByUserID: &creator,
	}))
	returned, err := f.store.TransitionExecutionStatus(ctx, directID, []string{"pending"}, config.StatusCanceled, &creator)
	require.NoError(t, err)
	require.NotNil(t, returned.CancelledBy)
	require.Equal(t, "creator@example.com", *returned.CancelledBy)
}
