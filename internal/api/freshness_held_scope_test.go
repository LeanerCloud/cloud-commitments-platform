package api

import (
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
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
	t.Parallel()
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

// A scope that cannot be established must fail closed: an error, never the
// unfiltered held IDs.
func TestFreshness_ScopeLookupFailureFailsClosed(t *testing.T) {
	t.Parallel()
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", mock.Anything, scopedToken).Return(&Session{UserID: scopedUserID}, nil).Maybe()
	mockAuth.grantPermissionsScoped(viewRecommendationsOnly(), []string{heldInScope})
	kept := mockAuth.ExpectedCalls[:0]
	for _, c := range mockAuth.ExpectedCalls {
		if c.Method != "GetAllowedAccountsAPI" {
			kept = append(kept, c)
		}
	}
	mockAuth.ExpectedCalls = kept
	mockAuth.On("GetAllowedAccountsAPI", mock.Anything, mock.Anything).Return(nil, errors.New("auth store unavailable"))
	f := newRefreshFixtureWithAuth(t, mockAuth)
	f.store.ExpectedCalls = nil
	f.store.On("GetRecommendationsFreshness", mock.Anything).Return(heldFreshness(heldInScope, heldOutScope), nil)

	res, err := NewRouter(f.handler).Route(t.Context(), "GET", "/api/recommendations/freshness", scopedRequest(""))

	require.Error(t, err, "a failed scope lookup must not produce a payload: got %v", res)
	assert.Nil(t, res, "nothing, filtered or not, may be returned")
	assert.NotContains(t, err.Error(), heldOutScope)
	assert.NotContains(t, err.Error(), heldInScope)
	_, isClient := IsClientError(err)
	assert.False(t, isClient, "a lookup failure is a server error, not a client one")
}

func partialSweepFor(t *testing.T, accounts []string, stored *config.RecommendationsFreshness) config.PartialProviderSweep {
	t.Helper()
	f := newRefreshFixture(t, viewRecommendationsOnly(), accounts)
	f.store.ExpectedCalls = nil
	f.store.On("GetRecommendationsFreshness", mock.Anything).Return(stored, nil)
	res, err := NewRouter(f.handler).Route(t.Context(), "GET", "/api/recommendations/freshness", scopedRequest(""))
	require.NoError(t, err)
	got, ok := res.(*config.RecommendationsFreshness)
	require.True(t, ok, "got %T", res)
	require.NotNil(t, got.LastCollectionPartial)
	return got.LastCollectionPartial.Providers[0]
}

// held_since keys and the expired ids name accounts too, so they are filtered
// like held_account_ids, and the expired count follows the filtered ids.
func TestFreshness_HeldSinceAndExpiredScopedToCaller(t *testing.T) {
	t.Parallel()
	ambient := uuid.Nil.String()
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	stored := func() *config.RecommendationsFreshness {
		f := heldFreshness(heldInScope, heldOutScope)
		sweep := &f.LastCollectionPartial.Providers[0]
		sweep.HeldSince = map[string]time.Time{heldInScope: since, heldOutScope: since, ambient: since}
		sweep.ExpiredHoldAccountIDs = []string{heldInScope, heldOutScope}
		sweep.Reasons[config.PartialReasonPricingHoldExpired] = 2
		return f
	}

	t.Run("unrestricted caller sees everything", func(t *testing.T) {
		got := partialSweepFor(t, nil, stored())
		assert.Equal(t, []string{heldInScope, heldOutScope}, got.HeldAccountIDs)
		assert.Len(t, got.HeldSince, 3)
		assert.Equal(t, []string{heldInScope, heldOutScope}, got.ExpiredHoldAccountIDs)
		assert.Equal(t, 2, got.Reasons[config.PartialReasonPricingHoldExpired])
	})
	t.Run("restricted caller sees only its own accounts", func(t *testing.T) {
		got := partialSweepFor(t, []string{heldInScope}, stored())
		assert.Equal(t, []string{heldInScope}, got.HeldAccountIDs)
		assert.Equal(t, map[string]time.Time{heldInScope: since}, got.HeldSince, "other accounts and the ambient key are hidden")
		assert.Equal(t, []string{heldInScope}, got.ExpiredHoldAccountIDs)
		assert.Equal(t, 1, got.Reasons[config.PartialReasonPricingHoldExpired], "the count matches the filtered ids")
	})
	t.Run("caller with no matching accounts sees no expired count", func(t *testing.T) {
		got := partialSweepFor(t, []string{"33333333-3333-3333-3333-333333333333"}, stored())
		assert.Empty(t, got.HeldSince)
		assert.Empty(t, got.ExpiredHoldAccountIDs)
		assert.NotContains(t, got.Reasons, config.PartialReasonPricingHoldExpired)
	})
	t.Run("the stored value is not mutated", func(t *testing.T) {
		s := stored()
		partialSweepFor(t, []string{heldInScope}, s)
		sweep := s.LastCollectionPartial.Providers[0]
		assert.Len(t, sweep.HeldSince, 3)
		assert.Equal(t, 2, sweep.Reasons[config.PartialReasonPricingHoldExpired])
		assert.Len(t, sweep.ExpiredHoldAccountIDs, 2)
	})
}
