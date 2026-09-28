package purchase

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestManager_SendUpcomingPurchaseNotifications_NoPlans(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	mockStore.On("ListPurchasePlans", ctx, config.PurchasePlanFilter{}).Return([]config.PurchasePlan{}, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		notifyDays:   7,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.SendUpcomingPurchaseNotifications(ctx)
	require.NoError(t, err)

	assert.Equal(t, 0, result.Notified)

	mockStore.AssertExpectations(t)
}

func TestManager_SendUpcomingPurchaseNotifications_DisabledPlan(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	plans := []config.PurchasePlan{
		{
			ID:           "plan-123",
			Name:         "Test Plan",
			Enabled:      false,
			AutoPurchase: true,
		},
	}

	mockStore.On("ListPurchasePlans", ctx, config.PurchasePlanFilter{}).Return(plans, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		notifyDays:   7,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.SendUpcomingPurchaseNotifications(ctx)
	require.NoError(t, err)

	assert.Equal(t, 0, result.Notified)

	mockStore.AssertExpectations(t)
}

func TestManager_SendUpcomingPurchaseNotifications_NotAutoPurchase(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	plans := []config.PurchasePlan{
		{
			ID:           "plan-123",
			Name:         "Test Plan",
			Enabled:      true,
			AutoPurchase: false,
		},
	}

	mockStore.On("ListPurchasePlans", ctx, config.PurchasePlanFilter{}).Return(plans, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		notifyDays:   7,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.SendUpcomingPurchaseNotifications(ctx)
	require.NoError(t, err)

	assert.Equal(t, 0, result.Notified)

	mockStore.AssertExpectations(t)
}

func TestManager_SendUpcomingPurchaseNotifications_Error(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	mockStore.On("ListPurchasePlans", ctx, config.PurchasePlanFilter{}).Return(nil, errors.New("database error"))

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		notifyDays:   7,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.SendUpcomingPurchaseNotifications(ctx)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to list purchase plans")

	mockStore.AssertExpectations(t)
}

func TestManager_BuildNotificationData(t *testing.T) {
	manager := &Manager{
		dashboardURL: "https://dashboard.example.com",
	}

	plan := config.PurchasePlan{
		ID:   "plan-123",
		Name: "Test Plan",
	}

	scheduledDate := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	execution := &config.PurchaseExecution{
		ExecutionID: "exec-456",
		// ApprovalToken deliberately holds a HASH-shaped value, not the raw
		// token under test: buildNotificationData must source the email's
		// ApprovalToken from the explicit rawApprovalToken parameter
		// (issue #103), never from this field.
		ApprovalToken:    config.HashApprovalToken("token-abc"),
		EstimatedSavings: 500.0,
		TotalUpfrontCost: 1500.0,
		ScheduledDate:    scheduledDate,
		Recommendations: []config.RecommendationRecord{
			{
				Service:      "rds",
				ResourceType: "db.r5.large",
				Engine:       "postgres",
				Region:       "us-east-1",
				Count:        2,
				Savings:      200.0,
			},
		},
	}

	data := manager.buildNotificationData(plan, execution, "token-abc", 5, "notify@example.com")

	assert.Equal(t, "https://dashboard.example.com", data.DashboardURL)
	assert.Equal(t, "token-abc", data.ApprovalToken)
	assert.Equal(t, 500.0, data.TotalSavings)
	assert.Equal(t, 1500.0, data.TotalUpfrontCost)
	assert.Equal(t, "February 1, 2024", data.PurchaseDate)
	assert.Equal(t, 5, data.DaysUntilPurchase)
	assert.Equal(t, "Test Plan", data.PlanName)
	assert.Len(t, data.Recommendations, 1)
	assert.Equal(t, "rds", data.Recommendations[0].Service)
}

func TestManager_GetOrCreateExecution(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	nextExec := time.Now().Add(24 * time.Hour)
	plan := &config.PurchasePlan{
		ID:   "plan-123",
		Name: "Test Plan",
		RampSchedule: config.RampSchedule{
			CurrentStep: 1,
		},
		NextExecutionDate: &nextExec,
	}

	// No existing execution found
	mockStore.On("GetExecutionByPlanAndDate", ctx, "plan-123", nextExec).Return(nil, nil)
	mockStore.On("SavePurchaseExecution", ctx, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	execution, rawToken, _, err := manager.getOrCreateExecution(ctx, plan)
	require.NoError(t, err)
	assert.NotNil(t, execution)
	assert.Equal(t, "plan-123", execution.PlanID)
	assert.Equal(t, "pending", execution.Status)
	// step_number names the step this row will COMPLETE, so a plan with one
	// step already done creates the row for step 2 (issue #1669).
	assert.Equal(t, 2, execution.StepNumber)
	assert.NotEmpty(t, execution.ExecutionID)
	// execution.ApprovalToken is the persisted SHA-256 hash (issue #103); the
	// raw, emailable value comes back as rawToken and must hash to it.
	assert.NotEmpty(t, execution.ApprovalToken)
	assert.NotEmpty(t, rawToken)
	assert.Equal(t, config.HashApprovalToken(rawToken), execution.ApprovalToken)

	mockStore.AssertExpectations(t)
}

func TestManager_GetOrCreateExecution_ExistingExecution(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	nextExec := time.Now().Add(24 * time.Hour)
	plan := &config.PurchasePlan{
		ID:   "plan-123",
		Name: "Test Plan",
		RampSchedule: config.RampSchedule{
			CurrentStep: 1,
		},
		NextExecutionDate: &nextExec,
	}

	existingExec := &config.PurchaseExecution{
		ExecutionID: "existing-exec-id",
		PlanID:      "plan-123",
		Status:      "pending",
		// A hash-shaped placeholder: the whole point of the rotation branch
		// is that this value is never recoverable as a raw token, so
		// getOrCreateExecution must mint a fresh one rather than reuse it.
		ApprovalToken: config.HashApprovalToken("stale-notified-token"),
		ScheduledDate: nextExec,
	}

	// Existing execution found: getOrCreateExecution mints a fresh token into
	// the returned copy (issue #103 -- a stored hash can never be re-emailed
	// raw) with the full ApprovalTokenTTL, but persists nothing: no
	// SavePurchaseExecution expectation, so a write here panics the mock.
	mockStore.On("GetExecutionByPlanAndDate", ctx, "plan-123", nextExec).Return(existingExec, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	execution, rawToken, rotationPending, err := manager.getOrCreateExecution(ctx, plan)
	require.NoError(t, err)
	assert.True(t, rotationPending, "the caller must persist the minted token after sending")
	assert.NotNil(t, execution)
	assert.Equal(t, "existing-exec-id", execution.ExecutionID)
	assert.Equal(t, "plan-123", execution.PlanID)
	// The rotated token must differ from the stale placeholder and hash to
	// the freshly returned raw value.
	assert.NotEmpty(t, rawToken)
	assert.NotEqual(t, config.HashApprovalToken("stale-notified-token"), execution.ApprovalToken)
	assert.Equal(t, config.HashApprovalToken(rawToken), execution.ApprovalToken)
	require.NotNil(t, execution.ApprovalTokenExpiresAt)
	assert.WithinDuration(t, time.Now().Add(config.ApprovalTokenTTL), *execution.ApprovalTokenExpiresAt, time.Minute)

	mockStore.AssertExpectations(t)
}

// TestManager_GetOrCreateExecution_ExistingCompletedNotRotated is the
// regression guard for a CodeRabbit finding on PR #416 (issue #103):
// GetExecutionByPlanAndDate filters only on plan_id + scheduled_date, not
// status, so a later notification tick for the same plan+date (e.g. before
// NextExecutionDate has advanced) can find a row that has already been
// approved, completed, or canceled since the last tick. Rotating its
// approval token unconditionally would overwrite the hash (and expiry) a
// still-live "purchase executed, click to revoke" email already committed
// to, 403-ing that link for no reason connected to the reminder-notification
// feature at all. Only pending/notified rows may be rotated.
func TestManager_GetOrCreateExecution_ExistingCompletedNotRotated(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	nextExec := time.Now().Add(24 * time.Hour)
	plan := &config.PurchasePlan{
		ID:                "plan-123",
		Name:              "Test Plan",
		NextExecutionDate: &nextExec,
	}

	completedExec := &config.PurchaseExecution{
		ExecutionID: "completed-exec-id",
		PlanID:      "plan-123",
		Status:      "completed",
		// This is the hash of the LIVE revocation token already emailed in
		// the "purchase executed" notification. If getOrCreateExecution
		// rotated it, that email's Revoke link would 403.
		ApprovalToken: config.HashApprovalToken("live-revocation-token"),
		ScheduledDate: nextExec,
	}
	mockStore.On("GetExecutionByPlanAndDate", ctx, "plan-123", nextExec).Return(completedExec, nil)
	// No SavePurchaseExecution / GetExecutionByID expectation: a completed
	// row must never be written to by the reminder-notification path.

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	execution, rawToken, _, err := manager.getOrCreateExecution(ctx, plan)
	require.Error(t, err, "a completed row must not be silently re-notified or rotated")
	assert.ErrorIs(t, err, errExecutionNotNotifiable)
	assert.Contains(t, err.Error(), "completed")
	assert.Nil(t, execution)
	assert.Empty(t, rawToken)
	// The stored hash for the live revocation link must be untouched.
	assert.Equal(t, config.HashApprovalToken("live-revocation-token"), completedExec.ApprovalToken)

	mockStore.AssertExpectations(t)
}

func TestManager_GetOrCreateExecution_SaveError(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	nextExec := time.Now().Add(24 * time.Hour)
	plan := &config.PurchasePlan{
		ID:                "plan-123",
		Name:              "Test Plan",
		NextExecutionDate: &nextExec,
	}

	// No existing execution found
	mockStore.On("GetExecutionByPlanAndDate", ctx, "plan-123", nextExec).Return(nil, nil)
	mockStore.On("SavePurchaseExecution", ctx, mock.AnythingOfType("*config.PurchaseExecution")).Return(errors.New("save failed"))

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	execution, _, _, err := manager.getOrCreateExecution(ctx, plan)
	assert.Error(t, err)
	assert.Nil(t, execution)

	mockStore.AssertExpectations(t)
}

func TestManager_GetOrCreateExecution_LookupError(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	nextExec := time.Now().Add(24 * time.Hour)
	plan := &config.PurchasePlan{
		ID:                "plan-123",
		Name:              "Test Plan",
		NextExecutionDate: &nextExec,
	}

	// Error looking up existing execution
	mockStore.On("GetExecutionByPlanAndDate", ctx, "plan-123", nextExec).Return(nil, errors.New("db error"))

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	execution, _, _, err := manager.getOrCreateExecution(ctx, plan)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to check for existing execution")
	assert.Nil(t, execution)

	mockStore.AssertExpectations(t)
}

// TestManager_GetOrCreateExecution_CreatesOnErrNotFound is the F2 regression
// guard: when GetExecutionByPlanAndDate wraps ErrNotFound (zero rows), the
// create branch must fire rather than treating it as a hard error.
// Pre-fix, the store returned a plain fmt.Errorf on zero rows, so getOrCreateExecution
// treated it as a fatal error and the create branch was unreachable.
func TestManager_GetOrCreateExecution_CreatesOnErrNotFound(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)

	nextExec := time.Now().Add(24 * time.Hour)
	plan := &config.PurchasePlan{
		ID:                "plan-f2",
		Name:              "F2 Plan",
		NextExecutionDate: &nextExec,
		RampSchedule:      config.RampSchedule{CurrentStep: 2},
	}

	// Store returns ErrNotFound (wrapped), matching the post-fix store behavior.
	notFoundErr := fmt.Errorf("%w: plan plan-f2 at %v", config.ErrNotFound, nextExec)
	mockStore.On("GetExecutionByPlanAndDate", ctx, "plan-f2", nextExec).Return(nil, notFoundErr)
	mockStore.On("SavePurchaseExecution", ctx, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)

	manager := &Manager{config: mockStore, dashboardURL: "https://example.com"}

	execution, rawToken, _, err := manager.getOrCreateExecution(ctx, plan)
	require.NoError(t, err, "ErrNotFound must trigger the create path, not a hard error (F2)")
	require.NotNil(t, execution)
	assert.Equal(t, "plan-f2", execution.PlanID)
	assert.Equal(t, "pending", execution.Status)
	// Two steps already completed, so this row is step 3 (issue #1669).
	assert.Equal(t, 3, execution.StepNumber)
	assert.NotEmpty(t, execution.ExecutionID)
	assert.NotEmpty(t, execution.ApprovalToken)
	assert.NotEmpty(t, rawToken)
	assert.Equal(t, config.HashApprovalToken(rawToken), execution.ApprovalToken)

	mockStore.AssertExpectations(t)
}

func TestManager_SendUpcomingPurchaseNotifications_WithNotification(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	nextExec := time.Now().Add(3 * 24 * time.Hour) // 3 days from now
	plans := []config.PurchasePlan{
		{
			ID:                     "plan-123",
			Name:                   "Test Plan",
			Enabled:                true,
			AutoPurchase:           true,
			NotificationDaysBefore: 7,
			NextExecutionDate:      &nextExec,
			LastNotificationSent:   nil,
			RampSchedule:           config.RampSchedule{CurrentStep: 0},
		},
	}

	notifyEmailStr := "notify@example.com"
	globalCfg := &config.GlobalConfig{NotificationEmail: &notifyEmailStr}
	mockStore.On("ListPurchasePlans", ctx, config.PurchasePlanFilter{}).Return(plans, nil)
	// No existing execution found
	mockStore.On("GetExecutionByPlanAndDate", ctx, "plan-123", nextExec).Return(nil, nil)
	mockStore.On("SavePurchaseExecution", ctx, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)
	mockStore.On("GetGlobalConfig", ctx).Return(globalCfg, nil)
	mockEmail.On("SendScheduledPurchaseNotification", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil)
	mockStore.On("UpdatePurchasePlan", ctx, mock.AnythingOfType("*config.PurchasePlan")).Return(nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		notifyDays:   7,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.SendUpcomingPurchaseNotifications(ctx)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Notified)

	mockStore.AssertExpectations(t)
	mockEmail.AssertExpectations(t)
}

func TestManager_SendUpcomingPurchaseNotifications_TooFarAway(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	nextExec := time.Now().Add(14 * 24 * time.Hour) // 14 days from now
	plans := []config.PurchasePlan{
		{
			ID:                     "plan-123",
			Name:                   "Test Plan",
			Enabled:                true,
			AutoPurchase:           true,
			NotificationDaysBefore: 7,
			NextExecutionDate:      &nextExec,
		},
	}

	mockStore.On("ListPurchasePlans", ctx, config.PurchasePlanFilter{}).Return(plans, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		notifyDays:   7,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.SendUpcomingPurchaseNotifications(ctx)
	require.NoError(t, err)

	assert.Equal(t, 0, result.Notified)

	mockStore.AssertExpectations(t)
}

func TestManager_SendUpcomingPurchaseNotifications_RecentNotification(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	nextExec := time.Now().Add(3 * 24 * time.Hour) // 3 days from now
	lastNotif := time.Now().Add(-12 * time.Hour)   // 12 hours ago
	plans := []config.PurchasePlan{
		{
			ID:                     "plan-123",
			Name:                   "Test Plan",
			Enabled:                true,
			AutoPurchase:           true,
			NotificationDaysBefore: 7,
			NextExecutionDate:      &nextExec,
			LastNotificationSent:   &lastNotif,
		},
	}

	mockStore.On("ListPurchasePlans", ctx, config.PurchasePlanFilter{}).Return(plans, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		notifyDays:   7,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.SendUpcomingPurchaseNotifications(ctx)
	require.NoError(t, err)

	assert.Equal(t, 0, result.Notified)

	mockStore.AssertExpectations(t)
}

func TestManager_SendUpcomingPurchaseNotifications_NoNextExecutionDate(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	plans := []config.PurchasePlan{
		{
			ID:                     "plan-123",
			Name:                   "Test Plan",
			Enabled:                true,
			AutoPurchase:           true,
			NotificationDaysBefore: 7,
			NextExecutionDate:      nil, // No next execution date
		},
	}

	mockStore.On("ListPurchasePlans", ctx, config.PurchasePlanFilter{}).Return(plans, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		notifyDays:   7,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.SendUpcomingPurchaseNotifications(ctx)
	require.NoError(t, err)

	assert.Equal(t, 0, result.Notified)

	mockStore.AssertExpectations(t)
}

func TestManager_SendUpcomingPurchaseNotifications_EmailFails(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	nextExec := time.Now().Add(3 * 24 * time.Hour)
	plans := []config.PurchasePlan{
		{
			ID:                     "plan-123",
			Name:                   "Test Plan",
			Enabled:                true,
			AutoPurchase:           true,
			NotificationDaysBefore: 7,
			NextExecutionDate:      &nextExec,
			RampSchedule:           config.RampSchedule{CurrentStep: 0},
		},
	}

	notifyEmailStr := "notify@example.com"
	globalCfg := &config.GlobalConfig{NotificationEmail: &notifyEmailStr}
	mockStore.On("ListPurchasePlans", ctx, config.PurchasePlanFilter{}).Return(plans, nil)
	// No existing execution found
	mockStore.On("GetExecutionByPlanAndDate", ctx, "plan-123", nextExec).Return(nil, nil)
	mockStore.On("SavePurchaseExecution", ctx, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)
	mockStore.On("GetGlobalConfig", ctx).Return(globalCfg, nil)
	mockEmail.On("SendScheduledPurchaseNotification", ctx, mock.AnythingOfType("email.NotificationData")).Return(errors.New("email failed"))

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		notifyDays:   7,
		dashboardURL: "https://dashboard.example.com",
	}

	result, err := manager.SendUpcomingPurchaseNotifications(ctx)
	require.NoError(t, err)

	assert.Equal(t, 0, result.Notified)

	mockStore.AssertExpectations(t)
	mockEmail.AssertExpectations(t)
}

// pendingExecForNotification sets up a plan due for a notification whose
// execution row already exists as "pending" with the hash of the token the
// previous notification emailed.
func pendingExecForNotification(ctx context.Context, mockStore *MockConfigStore) (*config.PurchasePlan, *config.PurchaseExecution) {
	nextExec := time.Now().Add(3 * 24 * time.Hour)
	plan := &config.PurchasePlan{ID: "plan-rot", Name: "Rotation Plan", NextExecutionDate: &nextExec}
	existing := &config.PurchaseExecution{
		ExecutionID:   "exec-rot",
		PlanID:        "plan-rot",
		Status:        "pending",
		ApprovalToken: config.HashApprovalToken("previously-emailed-token"),
		ScheduledDate: nextExec,
	}
	mockStore.On("GetExecutionByPlanAndDate", ctx, "plan-rot", nextExec).Return(existing, nil).Maybe()
	return plan, existing
}

// TestManager_SendPlanNotification_NoRecipientLeavesLiveTokenAlone pins the
// #416 review finding: without a notification email nothing is sent, so the
// previously emailed link must stay live. No write expectation is registered,
// so any token persist panics the mock.
func TestManager_SendPlanNotification_NoRecipientLeavesLiveTokenAlone(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	plan, existing := pendingExecForNotification(ctx, mockStore)
	mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{}, nil)

	manager := &Manager{config: mockStore, email: mockEmail, dashboardURL: "https://dashboard.example.com"}
	assert.False(t, manager.sendPlanNotification(ctx, plan))
	assert.Equal(t, config.HashApprovalToken("previously-emailed-token"), existing.ApprovalToken)
	mockStore.AssertExpectations(t)
	mockEmail.AssertExpectations(t)
}

// TestManager_SendPlanNotification_SendFailureLeavesLiveTokenAlone: an SES
// failure must not persist the replacement token, or the previous email's
// link dies with no working successor.
func TestManager_SendPlanNotification_SendFailureLeavesLiveTokenAlone(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	plan, _ := pendingExecForNotification(ctx, mockStore)
	notify := "notify@example.com"
	mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{NotificationEmail: &notify}, nil)
	mockEmail.On("SendScheduledPurchaseNotification", ctx, mock.AnythingOfType("email.NotificationData")).Return(errors.New("ses down"))

	manager := &Manager{config: mockStore, email: mockEmail, dashboardURL: "https://dashboard.example.com"}
	assert.False(t, manager.sendPlanNotification(ctx, plan))
	mockStore.AssertNotCalled(t, "RotatePendingApprovalToken", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	mockStore.AssertExpectations(t)
	mockEmail.AssertExpectations(t)
}

// TestManager_SendPlanNotification_PersistsEmailedTokenHashAfterSend: on a
// successful send, the hash persisted (via the status-guarded targeted
// update, never a full-row upsert) is the hash of exactly the raw token that
// was emailed, and the raw token itself is never stored.
func TestManager_SendPlanNotification_PersistsEmailedTokenHashAfterSend(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	plan, _ := pendingExecForNotification(ctx, mockStore)
	notify := "notify@example.com"
	mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{NotificationEmail: &notify}, nil)

	var emailedToken string
	mockEmail.On("SendScheduledPurchaseNotification", ctx, mock.AnythingOfType("email.NotificationData")).
		Run(func(args mock.Arguments) {
			emailedToken = args.Get(1).(email.NotificationData).ApprovalToken
		}).Return(nil)
	var persistedHash string
	mockStore.On("RotatePendingApprovalToken", ctx, "exec-rot", mock.AnythingOfType("string"), mock.AnythingOfType("time.Time")).
		Run(func(args mock.Arguments) {
			require.NotEmpty(t, emailedToken, "the token must be persisted only after the email went out")
			persistedHash = args.String(2)
		}).Return(true, nil)
	mockStore.On("UpdatePurchasePlan", ctx, plan).Return(nil)

	manager := &Manager{config: mockStore, email: mockEmail, dashboardURL: "https://dashboard.example.com"}
	assert.True(t, manager.sendPlanNotification(ctx, plan))
	require.NotEmpty(t, emailedToken)
	assert.Equal(t, config.HashApprovalToken(emailedToken), persistedHash)
	assert.NotEqual(t, emailedToken, persistedHash)
	mockStore.AssertExpectations(t)
	mockEmail.AssertExpectations(t)
}
