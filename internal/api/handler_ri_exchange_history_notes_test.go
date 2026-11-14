package api

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// #1050's two RI exchange notes reach the History row as written: the
// source-RI guard's refusal and the outcome-unknown resolve note. A raw
// provider error on another row is still summarized.
func TestHandler_getRIExchangeHistory_ShowsRIExchangeNotes(t *testing.T) {
	t.Parallel()
	const (
		sourceNote  = "RI exchange source RI is held by an exchange in progress: source RI(s) ri-0a1b2c3d already have an exchange in progress; resolve that exchange first"
		outcomeNote = "outcome unknown: the provider returned an error; verify the source RIs and target reservation at AWS, then POST /api/ri-exchange/resolve/550e8400-e29b-41d4-a716-000000010501"
		rawProvider = "RequestError: send request failed for arn:aws:ec2:us-east-1:123456789012:reserved-instances/ri-leak"
	)
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	mockAuth.On("ValidateSession", ctx, "tok").Return(&Session{UserID: "uid"}, nil)
	mockAuth.grantScoped("u-a")
	store := new(MockConfigStore)
	store.ListCloudAccountsFn = func(_ context.Context, _ config.CloudAccountFilter) ([]config.CloudAccount, error) {
		return []config.CloudAccount{{ID: "u-a", Name: "a", Provider: "aws", ExternalID: exchangeAcctA}}, nil
	}
	store.On("GetRIExchangeHistoryForAccounts", ctx, mock.Anything, 500, mock.Anything).Return([]config.RIExchangeRecord{
		{ID: "source", AccountID: exchangeAcctA, Status: "failed", Error: sourceNote},
		{ID: "outcome", AccountID: exchangeAcctA, Status: "processing", Error: outcomeNote},
		{ID: "raw", AccountID: exchangeAcctA, Status: "failed", Error: rawProvider},
	}, nil).Once()
	h := &Handler{auth: mockAuth, config: store}

	result, err := h.getRIExchangeHistory(ctx, &events.LambdaFunctionURLRequest{Headers: map[string]string{"Authorization": "Bearer tok"}})
	require.NoError(t, err)
	got := map[string]string{}
	for _, r := range result.(*RIExchangeHistoryResponse).Records {
		got[r.ID] = r.Error
	}
	assert.Equal(t, sourceNote, got["source"])
	assert.Equal(t, outcomeNote, got["outcome"])
	assert.NotContains(t, got["raw"], "ri-leak", "a raw provider error is still summarized")
}
