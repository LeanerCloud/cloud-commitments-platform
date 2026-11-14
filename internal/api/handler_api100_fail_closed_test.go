package api

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// A failed recommendations read must fail the Coverage page: an empty list
// would render every row as 100% covered (#100).
func TestGetCoverageBreakdown_RecommendationsReadErrorFailsTheRequest(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockScheduler := new(MockScheduler)
	t.Cleanup(func() {
		mockStore.AssertExpectations(t)
		mockScheduler.AssertExpectations(t)
	})
	mockStore.On("GetActivePurchaseHistory", ctx, mock.AnythingOfType("time.Time"), []string(nil), map[string][]string(nil)).
		Return([]config.PurchaseHistoryRecord{}, nil)
	mockStore.ListCloudAccountsFn = func(_ context.Context, _ config.CloudAccountFilter) ([]config.CloudAccount, error) {
		return []config.CloudAccount{}, nil
	}
	mockScheduler.On("ListRecommendations", mock.Anything, config.RecommendationFilter{}).Return(nil, errors.New("db down"))

	mockAuth, req := adminInventoryReq(t, ctx)
	handler := &Handler{auth: mockAuth, config: mockStore, scheduler: mockScheduler}

	_, err := handler.getCoverageBreakdown(ctx, req, map[string]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get recommendations")
}

// An unreadable freshness row is unknown, not fresh: the banner must fire
// like the cold-start case (#100).
func TestAttachReshapeStaleness_FreshnessReadErrorIsHardStale(t *testing.T) {
	ctx := context.Background()
	store := new(MockConfigStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	store.On("GetRecommendationsFreshness", ctx).Return(nil, errors.New("db down"))

	resp := &ReshapeRecommendationsResponse{}
	(&Handler{config: store}).attachReshapeStaleness(ctx, resp)

	assert.Equal(t, "hard", resp.RecsStaleness)
	assert.Nil(t, resp.RecsCollectedAt)
}
