package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

const (
	awsID = "11111111-1111-4111-8111-111111111111"
	gcpID = "22222222-2222-4222-8222-222222222222"
)

// historyStoreReads records the completed-history store reads of one request.
type historyStoreReads struct {
	purchases  []config.PurchaseHistoryFilter
	executions []config.HistoryExecutionsPageQuery
}

// historyPushdownResponse runs GET /api/history as a user whose allow-list is
// `scope`, against a store that returns the given stream rows whatever the
// predicates say (like a superset read would), and records the reads.
func historyPushdownResponse(t *testing.T, scope []string, params map[string]string, purchases []config.PurchaseHistoryRecord, execs []config.PurchaseExecution) (HistoryResponse, *historyStoreReads) {
	t.Helper()
	reads := &historyStoreReads{}
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", mock.Anything, "tok").Return(&Session{UserID: "u-1", Email: "u@example.com"}, nil)
	mockAuth.On("HasPermissionAPI", mock.Anything, "u-1", mock.Anything, mock.Anything).Return(true, nil)
	mockAuth.On("GetAllowedAccountsAPI", mock.Anything, "u-1").Return(scope, nil)

	store := new(MockConfigStore)
	store.On("ListPurchaseHistoryPage", mock.Anything, mock.MatchedBy(func(f config.PurchaseHistoryFilter) bool {
		reads.purchases = append(reads.purchases, f)
		return true
	}), mock.Anything).Return(purchases, nil)
	store.On("ListHistoryExecutionsPage", mock.Anything, mock.MatchedBy(func(q config.HistoryExecutionsPageQuery) bool {
		reads.executions = append(reads.executions, q)
		return true
	})).Return(execs, nil)
	store.On("GetExecutionsByStatusesFiltered", mock.Anything, mock.Anything).Return([]config.PurchaseExecution{}, nil)
	store.On("HistorySummaryAggregate", mock.Anything, mock.Anything).Return(&config.HistorySummaryGroups{}, nil)
	store.ListCloudAccountsFn = func(_ context.Context, _ config.CloudAccountFilter) ([]config.CloudAccount, error) {
		return []config.CloudAccount{
			{ID: awsID, Name: "dev", Provider: "aws", ExternalID: "111"},
			{ID: gcpID, Name: "other", Provider: "gcp", ExternalID: "222"},
		}, nil
	}
	h := &Handler{auth: mockAuth, config: store}
	resp, err := h.HandleRequest(context.Background(), &events.LambdaFunctionURLRequest{
		Headers:               map[string]string{"Authorization": "Bearer tok"},
		QueryStringParameters: params,
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: "GET", Path: "/api/history"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, "body: %s", resp.Body)
	var body HistoryResponse
	require.NoError(t, json.Unmarshal([]byte(resp.Body), &body))
	return body, reads
}

// Issue #632: the completed-history stream hands the store the caller's
// account scope, so a scoped user with sparse rows does not walk every
// tenant's rows, and the Go scope check still hides any row a superset read
// returns.
func TestHandleRequest_History_StreamPushesScopeAndFiltersIntoSQL(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	other := gcpID
	mine := awsID
	purchases := []config.PurchaseHistoryRecord{
		{PurchaseID: "mine", Provider: "aws", AccountID: "111", Timestamp: ts},
		{PurchaseID: "foreign", Provider: "gcp", AccountID: "222", Timestamp: ts.Add(-time.Hour)},
	}
	failed := func(id string, account *string, at time.Time) config.PurchaseExecution {
		return config.PurchaseExecution{
			ExecutionID: id, Status: "failed", Error: "boom", ScheduledDate: at, CloudAccountID: account,
			Recommendations: []config.RecommendationRecord{{Provider: "aws", CloudAccountID: account}},
		}
	}
	execs := []config.PurchaseExecution{
		failed("00000000-0000-4000-8000-000000000001", &mine, ts.Add(-2*time.Hour)),
		failed("00000000-0000-4000-8000-000000000002", &other, ts.Add(-3*time.Hour)),
	}

	t.Run("restricted scope", func(t *testing.T) {
		t.Parallel()
		got, reads := historyPushdownResponse(t, []string{awsID}, map[string]string{"provider": "aws"}, purchases, execs)

		ids := make([]string, len(got.Purchases))
		for i := range got.Purchases {
			ids[i] = got.Purchases[i].PurchaseID
		}
		assert.Contains(t, ids, "mine")
		assert.Contains(t, ids, "00000000-0000-4000-8000-000000000001")
		assert.NotContains(t, ids, "foreign", "a row a superset read returns is still checked in Go")
		assert.NotContains(t, ids, "00000000-0000-4000-8000-000000000002")

		wantScope := &config.ExecutionScope{UserID: "u-1", AccountIDs: []string{awsID, "111"}}
		require.NotEmpty(t, reads.purchases)
		for _, f := range reads.purchases {
			assert.Equal(t, wantScope, f.Scope)
			assert.Equal(t, "aws", f.Provider)
		}
		require.NotEmpty(t, reads.executions)
		for _, q := range reads.executions {
			assert.Equal(t, wantScope, q.Scope)
			assert.Equal(t, "aws", q.Provider)
		}
	})

	t.Run("account filter reaches the terminal executions read", func(t *testing.T) {
		t.Parallel()
		_, reads := historyPushdownResponse(t, []string{awsID}, map[string]string{"account_ids": awsID}, purchases, execs)
		require.NotEmpty(t, reads.executions)
		assert.Contains(t, reads.executions[0].AccountIDs, awsID)
	})

	t.Run("unrestricted scope sends no scope", func(t *testing.T) {
		t.Parallel()
		got, reads := historyPushdownResponse(t, nil, nil, purchases, execs)
		assert.Len(t, got.Purchases, 4)
		for _, f := range reads.purchases {
			assert.Nil(t, f.Scope)
		}
		for _, q := range reads.executions {
			assert.Nil(t, q.Scope)
		}
	})
}
