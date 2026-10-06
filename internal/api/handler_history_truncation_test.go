package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func historyRows(n int) []config.PurchaseHistoryRecord {
	rows := make([]config.PurchaseHistoryRecord, n)
	for i := range rows {
		rows[i] = config.PurchaseHistoryRecord{PurchaseID: fmt.Sprintf("p-%d", i), Timestamp: time.Now()}
	}
	return rows
}

func pendingExecutions(n int, creatorID *string) []config.PurchaseExecution {
	execs := make([]config.PurchaseExecution, n)
	for i := range execs {
		execs[i] = config.PurchaseExecution{
			ExecutionID:     fmt.Sprintf("e-%d", i),
			Status:          "pending",
			ScheduledDate:   time.Now(),
			CreatedByUserID: creatorID,
		}
	}
	return execs
}

// historyJSON round-trips the response through JSON so the assertion sees the
// wire shape a client sees, not the Go struct.
func historyJSON(t *testing.T, resp any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func runAdminHistory(t *testing.T, completed []config.PurchaseHistoryRecord, execs []config.PurchaseExecution, params map[string]string) map[string]any {
	t.Helper()
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockStore.On("GetAllPurchaseHistory", ctx, mock.Anything).Return(completed, nil)
	mockStore.On("GetExecutionsByStatuses", ctx, mock.Anything, mock.Anything).Return(execs, nil)
	mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{}, nil).Maybe()
	mockAuth, req := adminHistoryReq(ctx)
	handler := &Handler{auth: mockAuth, config: mockStore}

	result, err := handler.getHistory(ctx, req, params)
	require.NoError(t, err)
	return historyJSON(t, result)
}

func TestHandler_getHistory_TruncatedWhenCompletedAtCap(t *testing.T) {
	body := runAdminHistory(t, historyRows(config.DefaultListLimit), nil, map[string]string{})
	assert.Equal(t, true, body["truncated"], "a full page of completed rows may hide older ones")
	assert.EqualValues(t, config.DefaultListLimit, body["limit"])
}

func TestHandler_getHistory_TruncatedWhenExecutionsAtCap(t *testing.T) {
	// The execution fetch is capped before the in-memory filters run, so a
	// full page means pending approvals may have been dropped.
	body := runAdminHistory(t, nil, pendingExecutions(config.DefaultListLimit, nil), map[string]string{})
	assert.Equal(t, true, body["truncated"])
}

func TestHandler_getHistory_TruncatedHonorsCustomLimit(t *testing.T) {
	body := runAdminHistory(t, historyRows(5), nil, map[string]string{"limit": "5"})
	assert.Equal(t, true, body["truncated"])
	assert.EqualValues(t, 5, body["limit"])
}

func TestHandler_getHistory_NotTruncatedUnderLimit(t *testing.T) {
	body := runAdminHistory(t, historyRows(config.DefaultListLimit-1), pendingExecutions(config.DefaultListLimit-1, nil), map[string]string{})
	assert.Equal(t, false, body["truncated"])
	assert.EqualValues(t, config.DefaultListLimit, body["limit"])
}

// A scoped user's visible rows can be few while the fetch hit the cap: the
// flag is derived from the pre-scope fetch (the cap drops rows before scope
// filtering) but must expose only a boolean, never other tenants' counts.
func TestHandler_getHistory_ScopedUserTruncationLeaksNoCounts(t *testing.T) {
	ctx := context.Background()
	const scopedUserID = "scoped-user-id"
	own := scopedUserID
	execs := pendingExecutions(config.DefaultListLimit, nil)
	execs[0].CreatedByUserID = &own

	mockStore := new(MockConfigStore)
	mockStore.On("GetAllPurchaseHistory", ctx, mock.Anything).Return([]config.PurchaseHistoryRecord{}, nil)
	mockStore.On("GetExecutionsByStatuses", ctx, mock.Anything, mock.Anything).Return(execs, nil)
	mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{}, nil).Maybe()
	mockStore.ListCloudAccountsFn = func(_ context.Context, _ config.CloudAccountFilter) ([]config.CloudAccount, error) {
		return nil, nil
	}
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", ctx, "scoped-token").Return(&Session{UserID: scopedUserID, Email: "s@example.com"}, nil)
	mockAuth.On("HasPermissionAPI", ctx, scopedUserID, "view", "purchases").Return(true, nil)
	mockAuth.On("GetAllowedAccountsAPI", ctx, scopedUserID).Return([]string{"aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"}, nil)
	mockAuth.On("GetUser", ctx, mock.Anything).Return(&User{Email: "s@example.com"}, nil).Maybe()
	handler := &Handler{auth: mockAuth, config: mockStore}
	req := &events.LambdaFunctionURLRequest{Headers: map[string]string{"Authorization": "Bearer scoped-token"}}

	result, err := handler.getHistory(ctx, req, map[string]string{})
	require.NoError(t, err)
	body := historyJSON(t, result)

	assert.Len(t, body["purchases"], 1, "scoped user sees only their own row")
	assert.Equal(t, true, body["truncated"])
	assert.NotContains(t, body, "total", "no count of rows outside the caller's scope may be exposed")
	summary, ok := body["summary"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 1, summary["total_purchases"])
}
