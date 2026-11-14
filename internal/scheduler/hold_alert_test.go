package scheduler

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func freshWithExpired(ids ...string) *config.RecommendationsFreshness {
	return &config.RecommendationsFreshness{LastCollectionPartial: &config.RecommendationsCollectionPartial{
		Providers: []config.PartialProviderSweep{{Provider: "azure", ExpiredHoldAccountIDs: ids}},
	}}
}

func TestAlertNewlyExpiredHolds_DedupesCarriedIDs(t *testing.T) {
	ctx := context.Background()
	store := new(MockConfigStore)
	store.On("GetRecommendationsFreshness", ctx).Return(freshWithExpired("a-old", "a-new"), nil)
	mailer := new(MockEmailSender)
	mailer.On("SendNotification", ctx, mock.Anything, mock.MatchedBy(func(m string) bool {
		return assert.Contains(t, m, "a-new") && assert.NotContains(t, m, "a-old")
	})).Return(nil).Once()

	s := &Scheduler{config: store, email: mailer}
	s.alertNewlyExpiredHolds(ctx, map[string]bool{"a-old": true})
	mailer.AssertNumberOfCalls(t, "SendNotification", 1)

	// The next cycle carries both ids: no repeat alert.
	s.alertNewlyExpiredHolds(ctx, map[string]bool{"a-old": true, "a-new": true})
	mailer.AssertNumberOfCalls(t, "SendNotification", 1)
}
