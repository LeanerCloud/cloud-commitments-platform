package scheduler

import (
	"context"
	"errors"
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

func TestAlertNewlyExpiredHolds_RetriesAfterSendFailure(t *testing.T) {
	ctx := context.Background()
	store := new(MockConfigStore)
	store.On("GetRecommendationsFreshness", ctx).Return(freshWithExpired("a-new"), nil)
	mailer := new(MockEmailSender)
	mailer.On("SendNotification", ctx, mock.Anything, mock.Anything).Return(errors.New("smtp down")).Once()
	mailer.On("SendNotification", ctx, mock.Anything, mock.Anything).Return(nil).Once()

	s := &Scheduler{config: store, email: mailer}
	s.alertNewlyExpiredHolds(ctx, map[string]bool{})
	// The failed id is now carried in the persisted state, so before contains it.
	s.alertNewlyExpiredHolds(ctx, map[string]bool{"a-new": true})
	mailer.AssertNumberOfCalls(t, "SendNotification", 2)

	s.alertNewlyExpiredHolds(ctx, map[string]bool{"a-new": true})
	mailer.AssertNumberOfCalls(t, "SendNotification", 2)
}

func TestAlertNewlyExpiredHolds_NilBeforeSendsNothing(t *testing.T) {
	ctx := context.Background()
	store := new(MockConfigStore)
	store.On("GetRecommendationsFreshness", ctx).Return(freshWithExpired("a-old"), nil)
	mailer := new(MockEmailSender)

	s := &Scheduler{config: store, email: mailer}
	before, err := (&Scheduler{config: failingFreshnessStore(t, ctx)}).expiredAzureHolds(ctx)
	assert.Error(t, err)
	s.alertNewlyExpiredHolds(ctx, before)
	mailer.AssertNotCalled(t, "SendNotification", mock.Anything, mock.Anything, mock.Anything)
}

func failingFreshnessStore(t *testing.T, ctx context.Context) *MockConfigStore {
	t.Helper()
	store := new(MockConfigStore)
	store.On("GetRecommendationsFreshness", ctx).Return(nil, errors.New("db down"))
	return store
}

func TestAlertNewlyExpiredHolds_DropsAmbientSentinel(t *testing.T) {
	ctx := context.Background()
	const nilID = "00000000-0000-0000-0000-000000000000"
	store := new(MockConfigStore)
	store.On("GetRecommendationsFreshness", ctx).Return(freshWithExpired(nilID, "a-new"), nil)
	mailer := new(MockEmailSender)
	mailer.On("SendNotification", ctx, mock.MatchedBy(func(sub string) bool {
		return assert.Contains(t, sub, "for 1 account")
	}), mock.MatchedBy(func(m string) bool {
		return assert.NotContains(t, m, nilID)
	})).Return(nil).Once()

	s := &Scheduler{config: store, email: mailer}
	s.alertNewlyExpiredHolds(ctx, map[string]bool{})
	mailer.AssertNumberOfCalls(t, "SendNotification", 1)
}
