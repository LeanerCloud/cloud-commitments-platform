package api

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rows stored before the write side summarized provider errors hold raw text;
// the freshness response must show only a category message for them.
func TestFreshness_SanitizesStoredCollectionError(t *testing.T) {
	ctx := context.Background()
	rawStored := "azure: PUT https://management.azure.com/subscriptions/" + fakeSubID + "/providers/Microsoft.Capacity/x\n" +
		"RESPONSE 403: Forbidden\nERROR CODE: AuthorizationFailed\n; aws: operation error EC2: Op, https response error StatusCode: 403, " +
		"RequestID: " + fakeRequestID + ", api error UnauthorizedOperation: User: " + fakeRoleARN + " in account 123456789012"
	newCategory := "gcp: the provider returned an error"

	for name, tc := range map[string]struct{ stored, want string }{
		"raw text":      {rawStored, "the cloud provider refused the purchase: CUDly's role is not allowed to make it in this account"},
		"category form": {newCategory, newCategory},
	} {
		t.Run(name, func(t *testing.T) {
			store := new(MockConfigStore)
			t.Cleanup(func() { store.AssertExpectations(t) })
			now := time.Now().UTC()
			stored := tc.stored
			store.On("GetRecommendationsFreshness", ctx).Return(&config.RecommendationsFreshness{LastCollectedAt: &now, LastCollectionError: &stored}, nil)
			h := &Handler{config: store, apiKey: "test-key"}

			got, err := h.getRecommendationsFreshness(ctx, &events.LambdaFunctionURLRequest{Headers: map[string]string{"x-api-key": "test-key"}})

			require.NoError(t, err)
			require.NotNil(t, got.LastCollectionError)
			assertNoProviderDetail(t, "freshness", *got.LastCollectionError)
			assert.Equal(t, tc.want, *got.LastCollectionError)
			assert.Equal(t, tc.stored, stored, "the stored value is never rewritten")
		})
	}
}
