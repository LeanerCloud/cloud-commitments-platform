package api

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPurchasePricingRefusesSourceAccountAmbiguity(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"two sources", "unknown source", "unrelated collector"} {
		t.Run(scenario, func(t *testing.T) {
			first := config.RecommendationRecord{ID: "source-a", Provider: "aws", Service: "rds",
				CloudAccountID: new("collector-a"), SourceAccountID: new("source-a"),
				Region: "region", ResourceType: "sku", Term: 1, Payment: "all-upfront", Count: 1, UpfrontCost: 100}
			second := first
			second.ID, second.SourceAccountID, second.UpfrontCost = "source-b", new("source-b"), 200
			if scenario == "unknown source" {
				second.SourceAccountID = nil
			}
			rows, request := []config.RecommendationRecord{first, second}, first
			if scenario == "unrelated collector" {
				request.ID, request.CloudAccountID, request.SourceAccountID = "source-c", new("collector-b"), new("source-c")
				rows = append(rows, request)
			}
			store := new(MockConfigStore)
			store.On("ListStoredRecommendations", mock.Anything, config.RecommendationFilter{Provider: "aws"}).Return(rows, nil).Once()
			t.Cleanup(func() { store.AssertExpectations(t) })
			input := []config.RecommendationRecord{request}
			err := (&Handler{config: store}).priceRecommendationsFromStore(context.Background(), input)
			require.ErrorContains(t, err, "more than one row for identity key")
			require.Equal(t, request, input[0])
		})
	}
}
