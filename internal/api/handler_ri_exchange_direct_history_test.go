package api

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// #1883: GET /api/ri-exchange/history also lists the direct (interactive)
// exchanges, read-only, filtered by the caller's account scope.

type directHistoryStore struct {
	*MockConfigStore
	rows    []config.RIExchangeDirectHistoryRow
	err     error
	filter  config.RIExchangeDirectHistoryFilter
	callCnt int
}

func (s *directHistoryStore) ListRIExchangeDirectHistory(_ context.Context, f config.RIExchangeDirectHistoryFilter) ([]config.RIExchangeDirectHistoryRow, error) {
	s.callCnt++
	s.filter = f
	return s.rows, s.err
}

func dhCall(t *testing.T, store *directHistoryStore, accounts []string) (*RIExchangeHistoryResponse, error) {
	t.Helper()
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", mock.Anything, "tok").Return(&Session{UserID: apgUserID}, nil).Maybe()
	mockAuth.grantPermissionsScoped([]auth.Permission{{Action: auth.ActionView, Resource: auth.ResourcePurchases}}, accounts)
	store.On("GetRIExchangeHistory", ctx, mock.Anything, 500).Return([]config.RIExchangeRecord{}, nil).Maybe()
	store.On("GetRIExchangeHistoryForAccounts", ctx, mock.Anything, 500, mock.Anything).Return([]config.RIExchangeRecord{}, nil).Maybe()
	store.ListCloudAccountsFn = func(context.Context, config.CloudAccountFilter) ([]config.CloudAccount, error) {
		return []config.CloudAccount{
			{ID: "u-a", Name: "a", Provider: "aws", ExternalID: "111111111111"},
			{ID: "u-b", Name: "b", Provider: "azure", ExternalID: "sub-b"},
		}, nil
	}
	h := &Handler{config: store, auth: mockAuth}
	out, err := h.getRIExchangeHistory(ctx, &events.LambdaFunctionURLRequest{Headers: map[string]string{"Authorization": "Bearer tok"}})
	if err != nil {
		return nil, err
	}
	return out.(*RIExchangeHistoryResponse), nil
}

func TestGetRIExchangeHistory_ListsDirectExecutions(t *testing.T) {
	t.Parallel()
	acct, net := "u-b", "-12.50"
	done := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	store := &directHistoryStore{MockConfigStore: new(MockConfigStore), rows: []config.RIExchangeDirectHistoryRow{
		{ID: "d1", Provider: "azure", Status: "completed", CloudAccountID: &acct, NetPayable: &net, NetPayableCurrency: "EUR",
			ExecutedBy: "op@example.com", CreatedAt: done, CompletedAt: &done},
		{ID: "d2", Provider: "aws", Status: "unknown", Region: "eu-west-1", ExecutedBy: "op@example.com", CreatedAt: done,
			Error: "RequestError: send request failed for arn:aws:ec2:us-east-1:123456789012:reserved-instances/ri-leak"},
	}}
	resp, err := dhCall(t, store, nil)
	require.NoError(t, err)
	require.Len(t, resp.DirectExecutions, 2)
	assert.True(t, store.filter.AllAccounts)
	assert.Equal(t, 500, store.filter.Limit)
	assert.Equal(t, "azure", resp.DirectExecutions[0].Provider)
	require.NotNil(t, resp.DirectExecutions[0].NetPayable)
	assert.Equal(t, "-12.50", *resp.DirectExecutions[0].NetPayable)
	assert.Equal(t, "unknown", resp.DirectExecutions[1].Status)
	assert.NotContains(t, resp.DirectExecutions[1].ErrorSummary, "ri-leak", "a raw provider error is summarized")
}

func TestGetRIExchangeHistory_DirectExecutionsScopedToAccounts(t *testing.T) {
	t.Parallel()
	store := &directHistoryStore{MockConfigStore: new(MockConfigStore)}
	_, err := dhCall(t, store, []string{"u-b"})
	require.NoError(t, err)
	assert.False(t, store.filter.AllAccounts)
	assert.Equal(t, []string{"u-b"}, store.filter.AccountIDs, "only the in-scope account reaches the query")
}

func TestGetRIExchangeHistory_DirectExecutionsEmptyBeforeMigration(t *testing.T) {
	t.Parallel()
	store := &directHistoryStore{MockConfigStore: new(MockConfigStore), err: config.ErrRIExchangeDirectResolveNotReady}
	resp, err := dhCall(t, store, nil)
	require.NoError(t, err)
	assert.NotNil(t, resp.DirectExecutions, "serializes as [], not null")
	assert.Empty(t, resp.DirectExecutions)
}

func TestGetRIExchangeHistory_DirectExecutionsStoreError(t *testing.T) {
	t.Parallel()
	store := &directHistoryStore{MockConfigStore: new(MockConfigStore), err: assert.AnError}
	_, err := dhCall(t, store, nil)
	require.Error(t, err)
}
