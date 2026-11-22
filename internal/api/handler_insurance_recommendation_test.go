package api

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/archera"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

const recRouteID = "55555555-5555-4555-8555-555555555555"

func recRoutePath(id string) string { return "/api/insurance/recommendations/" + id }

type quoteRig struct {
	h     *Handler
	vend  *recClient
	sched *MockScheduler
	rec   config.RecommendationRecord
}

func newQuoteRig(t *testing.T) *quoteRig {
	t.Helper()
	items, cmp := recVendor(t)
	vend := &recClient{items: items, cmp: cmp}
	rec := recRecord()
	rec.ID = recRouteID
	sched := new(MockScheduler)
	return &quoteRig{h: &Handler{insurance: &recProvider{client: vend}, scheduler: sched}, vend: vend, sched: sched, rec: rec}
}

func (r *quoteRig) get(id string) (any, error) {
	return NewRouter(r.h).Route(adminCtx(), "GET", recRoutePath(id), &events.LambdaFunctionURLRequest{})
}

func (r *quoteRig) stored() {
	r.sched.On("GetRecommendationByID", mock.Anything, recRouteID).Return(&r.rec, ([]string)(nil), nil)
}

func TestInsuranceRecommendation_MatchedReturnsOnlyItsOwnQuote(t *testing.T) {
	r := newQuoteRig(t)
	r.stored()
	res, err := r.get(recRouteID)
	require.NoError(t, err)
	dto, ok := res.(*archera.RecommendationComparisonDTO)
	require.True(t, ok)
	require.Equal(t, recRouteID, dto.RecommendationID)
	require.Equal(t, archera.ResourceAssociated, dto.Status)
	require.NotNil(t, dto.Current)
	require.Equal(t, recOffer, dto.Current.OfferID)
	require.Equal(t, []string{recItem}, r.vend.request.LineItemIDs)
	require.NotEmpty(t, dto.NonGatingDisclosure)
}

func TestInsuranceRecommendation_NoLineItemIsUnavailableWithoutFigures(t *testing.T) {
	r := newQuoteRig(t)
	r.stored()
	r.vend.items = nil
	res, err := r.get(recRouteID)
	require.NoError(t, err)
	dto := res.(*archera.RecommendationComparisonDTO)
	require.Equal(t, archera.MatchUnavailable, dto.Status)
	require.Nil(t, dto.Current)
	require.Empty(t, dto.Candidates)
	require.Zero(t, r.vend.cmpCalls)
}

func TestInsuranceRecommendation_AmbiguousLineItemsFailLoud(t *testing.T) {
	r := newQuoteRig(t)
	r.stored()
	twin := r.vend.items[0]
	twin.ID = "33333333-3333-4333-8333-333333333333"
	r.vend.items = append(r.vend.items, twin)
	res, err := r.get(recRouteID)
	require.Nil(t, res)
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, 409, ce.code)
	require.Zero(t, r.vend.cmpCalls)
}

func TestInsuranceRecommendation_IneligibleStoredRecommendationIs422(t *testing.T) {
	r := newQuoteRig(t)
	r.rec.Details = []byte(`{}`)
	r.stored()
	_, err := r.get(recRouteID)
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, 422, ce.code)
	require.Zero(t, r.vend.itemCalls)
}

func TestInsuranceRecommendation_UnknownIDIs404WithoutVendorCall(t *testing.T) {
	r := newQuoteRig(t)
	r.sched.On("GetRecommendationByID", mock.Anything, recRouteID).Return(nil, nil, nil)
	_, err := r.get(recRouteID)
	require.ErrorIs(t, err, errNotFound)
	require.Zero(t, r.vend.itemCalls)
}

func TestInsuranceRecommendation_BadIDIs400BeforeLookup(t *testing.T) {
	r := newQuoteRig(t) // no scheduler expectation: a lookup would panic
	for _, id := range []string{"stored-a", "..%2Fx", "a/b"} {
		_, err := r.get(id)
		ce, ok := IsClientError(err)
		require.True(t, ok, "id %q: %v", id, err)
		require.Equal(t, 400, ce.code, id)
	}
	_, err := r.h.getInsuranceRecommendation(adminCtx(), &events.LambdaFunctionURLRequest{}, "")
	ce, ok := IsClientError(err)
	require.True(t, ok)
	require.Equal(t, 400, ce.code)
}

func TestInsuranceRecommendation_VendorFailureIsSanitized(t *testing.T) {
	r := newQuoteRig(t)
	r.stored()
	r.vend.itemsErr = errors.New(recSpace)
	_, err := r.get(recRouteID)
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, 502, ce.code)
	require.NotContains(t, ce.message, recSpace)
}

func TestInsuranceRecommendation_NotConfiguredIs503(t *testing.T) {
	r := newQuoteRig(t)
	r.stored()
	r.h.insurance = nil
	_, err := r.get(recRouteID)
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, 503, ce.code)
}

// Cross-tenant: a caller scoped to one account is refused before the
// recommendation lookup (no scheduler expectation, so a lookup would panic) and
// before any vendor call, so it cannot probe another tenant's recommendation id.
func TestInsuranceRecommendation_ScopedCallerGets404BeforeLookup(t *testing.T) {
	r := newQuoteRig(t)
	m := new(MockAuthService)
	t.Cleanup(func() { m.AssertExpectations(t) })
	m.On("HasAPIKeyPermissionAPI", mock.Anything, "user-key", "view", "recommendations").Return("u2", "key-1", true, nil)
	m.On("GetAllowedAccountsAPI", mock.Anything, "u2").Return([]string{"acct-1"}, nil)
	r.h.auth = m
	ctx := contextWithPrincipal(context.Background(), &Principal{Kind: PrincipalUserAPIKey, UserID: "u2", APIKeyID: "key-1"})
	req := &events.LambdaFunctionURLRequest{Headers: map[string]string{"x-api-key": "user-key"}}
	_, err := NewRouter(r.h).Route(ctx, "GET", recRoutePath(recRouteID), req)
	require.ErrorIs(t, err, errNotFound)
	require.Zero(t, r.vend.itemCalls)
}

func TestInsuranceRecommendation_DeniedCallerGets403(t *testing.T) {
	r := newQuoteRig(t)
	m := new(MockAuthService)
	t.Cleanup(func() { m.AssertExpectations(t) })
	m.On("HasPermissionAPI", mock.Anything, "u1", "view", "recommendations").Return(false, nil)
	r.h.auth = m
	_, err := NewRouter(r.h).Route(sessionPrincipalCtx(), "GET", recRoutePath(recRouteID), &events.LambdaFunctionURLRequest{})
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, 403, ce.code)
	require.Zero(t, r.vend.itemCalls)
}

func TestInsuranceRecommendation_RouteIsAuthenticated(t *testing.T) {
	r := newQuoteRig(t)
	r.h.auth = new(MockAuthService)
	_, err := NewRouter(r.h).Route(context.Background(), "GET", recRoutePath(recRouteID), &events.LambdaFunctionURLRequest{})
	require.Error(t, err)
	require.Zero(t, r.vend.itemCalls)
}
