package api

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// GET /api/recommendations/freshness is AuthUser and the state row is global,
// so held_account_ids must only name accounts the caller may see.

const (
	heldInScope  = "11111111-1111-1111-1111-111111111111"
	heldOutScope = "22222222-2222-2222-2222-222222222222"
)

func heldFreshness(ids ...string) *config.RecommendationsFreshness {
	return &config.RecommendationsFreshness{LastCollectionPartial: &config.RecommendationsCollectionPartial{
		Providers: []config.PartialProviderSweep{{
			Provider: "azure", IncompleteAccounts: len(ids), HeldAccountIDs: ids,
			Reasons: map[string]int{config.PartialReasonPricingUnavailable: 1},
		}},
	}}
}

func heldIDsFor(t *testing.T, accounts []string, stored *config.RecommendationsFreshness) []string {
	t.Helper()
	f := newRefreshFixture(t, viewRecommendationsOnly(), accounts)
	f.store.ExpectedCalls = nil
	f.store.On("GetRecommendationsFreshness", mock.Anything).Return(stored, nil)
	res, err := NewRouter(f.handler).Route(t.Context(), "GET", "/api/recommendations/freshness", scopedRequest(""))
	require.NoError(t, err)
	got, ok := res.(*config.RecommendationsFreshness)
	require.True(t, ok, "got %T", res)
	require.NotNil(t, got.LastCollectionPartial)
	return got.LastCollectionPartial.Providers[0].HeldAccountIDs
}

func TestFreshness_HeldAccountsScopedToCaller(t *testing.T) {
	stored := func() *config.RecommendationsFreshness { return heldFreshness(heldInScope, heldOutScope) }

	t.Run("scoped caller sees only its own held account", func(t *testing.T) {
		assert.Equal(t, []string{heldInScope}, heldIDsFor(t, []string{heldInScope}, stored()))
	})
	t.Run("unrestricted caller sees all", func(t *testing.T) {
		assert.Equal(t, []string{heldInScope, heldOutScope}, heldIDsFor(t, nil, stored()))
	})
	t.Run("caller with no matching accounts sees none", func(t *testing.T) {
		assert.Empty(t, heldIDsFor(t, []string{"33333333-3333-3333-3333-333333333333"}, stored()))
	})
	t.Run("the stored value is not mutated", func(t *testing.T) {
		s := stored()
		heldIDsFor(t, []string{heldInScope}, s)
		assert.Equal(t, []string{heldInScope, heldOutScope}, s.LastCollectionPartial.Providers[0].HeldAccountIDs)
	})
}
