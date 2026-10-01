//go:build integration

package purchase

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestNotificationStampPreservesCompletedRampAndNextExecution(t *testing.T) {
	ctx := context.Background()
	store, _ := rampStepStore(ctx, t)
	global, err := store.GetGlobalConfig(ctx)
	require.NoError(t, err)
	recipient := "fixture@example.invalid"
	global.NotificationEmail = &recipient
	require.NoError(t, store.SaveGlobalConfig(ctx, global))
	next := time.Now().Add(24 * time.Hour).Truncate(time.Microsecond)
	plan := &config.PurchasePlan{
		Name: "notification race", Enabled: true, AutoPurchase: true, NotificationDaysBefore: 30,
		Services: map[string]config.ServiceConfig{"ec2": {Provider: "aws", Service: "ec2"}},
		RampSchedule: config.RampSchedule{Type: "weekly", PercentPerStep: 25, StepIntervalDays: 7,
			CurrentStep: 1, TotalSteps: 4, StartDate: next.AddDate(0, 0, -7)},
		NextExecutionDate: &next,
	}
	require.NoError(t, plan.Validate())
	require.NoError(t, store.CreatePurchasePlan(ctx, plan))
	sender := new(MockEmailSender)
	manager := &Manager{config: store, email: sender}
	var completed *config.PurchasePlan
	sender.On("SendScheduledPurchaseNotification", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		data := args.Get(1).(email.NotificationData)
		execution, getErr := store.GetExecutionByID(ctx, data.ExecutionID)
		require.NoError(t, getErr)
		require.Equal(t, 2, execution.StepNumber)
		execution.Status = "completed"
		require.NoError(t, store.SavePurchaseExecution(ctx, execution))
		require.NoError(t, store.CompletePlanStep(ctx, plan.ID, 2))
		completed, getErr = store.GetPurchasePlan(ctx, plan.ID)
		require.NoError(t, getErr)
		require.Equal(t, 2, completed.RampSchedule.CurrentStep)
		require.NotNil(t, completed.LastExecutionDate)
		require.True(t, completed.NextExecutionDate.After(next))
	}).Return(nil).Once()
	result, err := manager.SendUpcomingPurchaseNotifications(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, result.Notified)
	sender.AssertExpectations(t)
	after, err := store.GetPurchasePlan(ctx, plan.ID)
	require.NoError(t, err)
	require.NotNil(t, after.LastNotificationSent)
	require.Equal(t, completed.RampSchedule.CurrentStep, after.RampSchedule.CurrentStep)
	require.Equal(t, completed.NextExecutionDate, after.NextExecutionDate)
	require.Equal(t, completed.LastExecutionDate, after.LastExecutionDate)

	execution, _, _, err := manager.getOrCreateExecution(ctx, after)
	require.NoError(t, err)
	require.Equal(t, 3, execution.StepNumber)
	require.Equal(t, *completed.NextExecutionDate, execution.ScheduledDate)
	execution.Status = "completed"
	require.NoError(t, store.SavePurchaseExecution(ctx, execution))
	require.NoError(t, store.CompletePlanStep(ctx, plan.ID, 3))
	continued, err := store.GetPurchasePlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, 3, continued.RampSchedule.CurrentStep)
	require.True(t, continued.NextExecutionDate.After(*after.NextExecutionDate))
	require.Equal(t, after.LastNotificationSent, continued.LastNotificationSent)
}
