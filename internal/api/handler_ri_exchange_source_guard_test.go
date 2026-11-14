package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// #1050: an approval whose source RI is held by another processing exchange
// fails with the true reason, naming only the shared RI, and never reaches AWS.
func TestExecuteApprovedExchange_SourceInFlight_FailsWithSharedRIOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const id = "550e8400-e29b-41d4-a716-000000001050"
	const blocker = "6f0e8400-e29b-41d4-a716-00000000b10c"

	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{
		RIExchangeMaxDailyUSD: 1000, RIExchangeMaxPerExchangeUSD: 500,
	}, nil)
	mockStore.On("ReserveApprovedRIExchange", ctx, id, "1000.000000", "500.000000").
		Return("", fmt.Errorf("%w: source RI(s) ri-shared already have an exchange in progress; resolve that exchange first", config.ErrRIExchangeSourceInFlight))
	var stored string
	mockStore.On("FailRIExchange", mock.Anything, id, mock.Anything).Run(func(args mock.Arguments) {
		stored = args.String(2)
	}).Return(nil)

	called := false
	h := &Handler{config: mockStore, executeExchangeFn: func(context.Context, exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
		called = true
		return "", nil, nil
	}}
	resp, err := h.executeApprovedExchange(ctx, id, &config.RIExchangeRecord{
		ID: id, Region: "us-east-1", SourceRIIDs: []string{"ri-shared", "ri-own"}, TargetOfferingID: "o", TargetCount: 1, PaymentDue: "100.00",
	})
	require.NoError(t, err)
	body := resp.(map[string]any)
	assert.Equal(t, "failed", body["status"])
	assert.False(t, called, "provider must not be called while a source RI is in flight")
	for name, text := range map[string]string{"body reason": fmt.Sprint(body["reason"]), "stored error": stored} {
		assert.Contains(t, text, "ri-shared", "%s must name the shared source RI", name)
		assert.Contains(t, text, "already have an exchange in progress", "%s must give the true reason, not the cap-check fallback", name)
		assert.False(t, strings.Contains(text, blocker), "%s must not reveal the blocking exchange", name)
	}
}
