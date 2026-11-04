package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// If the previous hold cannot be read, nothing may be written: an upsert would
// reset the marker and re-open the purchase of held rows. Fail closed.
func TestPersistCollection_UnreadableHoldStateWritesNothing(t *testing.T) {
	store := new(MockConfigStore)
	upserted := false
	store.On("GetRecommendationsFreshness", mock.Anything).Return(nil, errors.New("database unavailable"))
	store.On("UpsertRecommendations", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { upserted = true }).Return(nil).Maybe()
	store.On("SetRecommendationsCollectionError", mock.Anything, mock.Anything).Return(nil).Maybe()
	s := &Scheduler{config: store}

	s.persistCollection(context.Background(), []config.RecommendationRecord{{ID: "r"}}, nil, nil, nil)

	assert.False(t, upserted, "an upsert would clear the pricing hold marker")
}

func TestCarryForwardAzureHold(t *testing.T) {
	prev := heldPrevious("a", "b")
	for name, tc := range map[string]struct {
		collects []config.SuccessfulCollect
		partial  *config.RecommendationsCollectionPartial
		want     []string
	}{
		"nothing re-priced":       {nil, nil, []string{"a", "b"}},
		"one re-priced":           {expandSuccessfulCollects("azure", []string{"a"}), nil, []string{"b"}},
		"both re-priced":          {expandSuccessfulCollects("azure", []string{"a", "b"}), nil, nil},
		"other provider ignored":  {expandSuccessfulCollects("gcp", []string{"a"}), nil, []string{"a", "b"}},
		"new hold is merged":      {nil, heldPartialOf("c"), []string{"a", "b", "c"}},
		"re-held account dedupes": {nil, heldPartialOf("a"), []string{"a", "b"}},
	} {
		t.Run(name, func(t *testing.T) {
			store := new(MockConfigStore)
			store.On("GetRecommendationsFreshness", mock.Anything).Return(prev, nil)
			s := &Scheduler{config: store}

			got, err := s.carryForwardAzureHold(context.Background(), tc.partial, tc.collects)

			require.NoError(t, err)
			if tc.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Len(t, got.Providers, 1)
			assert.ElementsMatch(t, tc.want, got.Providers[0].HeldAccountIDs)
			assert.Positive(t, got.Providers[0].Reasons[config.PartialReasonPricingUnavailable])
		})
	}
}

func heldPartialOf(ids ...string) *config.RecommendationsCollectionPartial {
	return &config.RecommendationsCollectionPartial{Providers: []config.PartialProviderSweep{{
		Provider: "azure", HeldAccountIDs: ids, Reasons: map[string]int{config.PartialReasonPricingUnavailable: 1},
	}}}
}

func heldPrevious(ids ...string) *config.RecommendationsFreshness {
	return &config.RecommendationsFreshness{LastCollectionPartial: heldPartialOf(ids...)}
}
