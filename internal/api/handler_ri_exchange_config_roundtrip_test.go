package api

import (
	"context"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// PUT /api/ri-exchange/config must store every field of its body in the matching
// ri_exchange_* column and leave the rest of the global config alone: the write
// is a partial update that shares the config lock with other writers.
func TestUpdateRIExchangeConfig_StoresEveryFieldAndKeepsTheRest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	mockAuth.On("ValidateSession", ctx, "admin-token").Return(&Session{UserID: "uid"}, nil)
	mockAuth.grantAdmin()

	stored := &config.GlobalConfig{
		DefaultTerm:                    3,
		NotificationDaysBefore:         7,
		RIExchangeEnabled:              false,
		RIExchangeMode:                 "auto",
		RIExchangeUtilizationThreshold: 95,
		RIExchangeMaxPerExchangeUSD:    1,
		RIExchangeMaxDailyUSD:          2,
		RIExchangeLookbackDays:         30,
	}
	mockStore.On("GetGlobalConfig", ctx).Return(stored, nil)
	var saved *config.GlobalConfig
	mockStore.On("SaveGlobalConfig", ctx, mock.Anything).Run(func(args mock.Arguments) {
		saved = args.Get(1).(*config.GlobalConfig)
	}).Return(nil)

	h := &Handler{auth: mockAuth, config: mockStore}
	_, err := h.updateRIExchangeConfig(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer admin-token"},
		Body: `{"auto_exchange_enabled": true, "mode": "manual", "utilization_threshold": 72.5,
			"max_payment_per_exchange_usd": 150.25, "max_payment_daily_usd": 600.5, "lookback_days": 21}`,
	})
	require.NoError(t, err)

	require.NotNil(t, saved, "nothing was saved")
	assert.True(t, saved.RIExchangeEnabled, "auto_exchange_enabled")
	assert.Equal(t, "manual", saved.RIExchangeMode, "mode")
	assert.Equal(t, 72.5, saved.RIExchangeUtilizationThreshold, "utilization_threshold")
	assert.Equal(t, 150.25, saved.RIExchangeMaxPerExchangeUSD, "max_payment_per_exchange_usd")
	assert.Equal(t, 600.5, saved.RIExchangeMaxDailyUSD, "max_payment_daily_usd")
	assert.Equal(t, 21, saved.RIExchangeLookbackDays, "lookback_days")
	assert.Equal(t, 3, saved.DefaultTerm, "an unrelated field must be preserved")
	assert.Equal(t, 7, saved.NotificationDaysBefore, "an unrelated field must be preserved")
}
