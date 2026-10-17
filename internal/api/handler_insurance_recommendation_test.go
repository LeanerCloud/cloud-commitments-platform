package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/archera"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// recStore serves one scripted result per ListStoredRecommendations call; the last one repeats.
type recStore struct {
	config.StoreInterface
	results [][]config.RecommendationRecord
	errs    []error
	calls   int
	filter  config.RecommendationFilter
}

func (s *recStore) ListStoredRecommendations(_ context.Context, f config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	i := min(s.calls, len(s.results)-1)
	s.calls++
	s.filter = f
	var err error
	if s.calls <= len(s.errs) {
		err = s.errs[s.calls-1]
	}
	return s.results[i], err
}

type recRig struct {
	store    *recStore
	client   *recClient
	provider *recProvider
	h        *Handler
}

func newRecRig(t *testing.T, results ...[]config.RecommendationRecord) *recRig {
	t.Helper()
	items, cmp := recVendor(t)
	c := &recClient{items: items, cmp: cmp}
	p := &recProvider{client: c}
	s := &recStore{results: results}
	return &recRig{s, c, p, &Handler{config: s, insurance: p}}
}

func (r *recRig) get(ctx context.Context, raw string, params map[string]string) (any, error) {
	return NewRouter(r.h).Route(ctx, "GET", "/api/insurance/comparison", &events.LambdaFunctionURLRequest{RawQueryString: raw, QueryStringParameters: params})
}

func (r *recRig) getID(id string) (any, error) {
	return r.get(adminCtx(), "recommendation_id="+id, map[string]string{"recommendation_id": id})
}

func requireCode(t *testing.T, err error, code int) *clientError {
	t.Helper()
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, code, ce.code)
	require.NotContains(t, ce.message, recSpace)
	return ce
}

func one() []config.RecommendationRecord { r := recRecord(); return []config.RecommendationRecord{r} }

func TestInsuranceRecommendationMatchesThroughRouter(t *testing.T) {
	r := newRecRig(t, one())
	got, err := r.getID("stored-a")
	require.NoError(t, err)
	dto, ok := got.(*archera.RecommendationComparisonDTO)
	require.True(t, ok, "got %T", got)
	require.Equal(t, archera.ResourceAssociated, dto.Status)
	require.Equal(t, "stored-a", dto.RecommendationID)
	require.Equal(t, "stored-a", r.store.filter.ID)
	require.Equal(t, 2, r.store.calls)
	require.Equal(t, []string{recItem}, r.client.request.LineItemIDs)
	require.ElementsMatch(t, []insurance.PaymentOption{insurance.PaymentNoUpfront, insurance.PaymentPartialUpfront, insurance.PaymentAllUpfront}, r.client.request.PaymentOptions)
	require.Equal(t, insTestPlan, r.client.request.PlanID)
}

func TestInsuranceRecommendationNilCloudAccountStillCompares(t *testing.T) {
	row := recRecord()
	row.CloudAccountID = nil
	r := newRecRig(t, []config.RecommendationRecord{row})
	got, err := r.getID("stored-a")
	require.NoError(t, err)
	require.Equal(t, archera.ResourceAssociated, got.(*archera.RecommendationComparisonDTO).Status)
}

func TestInsuranceRecommendationAbsentParamKeepsPlanComparison(t *testing.T) {
	r := newRecRig(t, one())
	r.client.cmp = &insurance.Comparison{}
	got, err := r.get(adminCtx(), "unrelated=1", map[string]string{"unrelated": "1"})
	require.NoError(t, err)
	_, ok := got.(*archera.ComparisonDTO)
	require.True(t, ok, "got %T", got)
	require.Zero(t, r.store.calls)
	require.Zero(t, r.client.itemCalls)
}

func TestInsuranceRecommendationNoLineItemIsUnavailableWithoutComparison(t *testing.T) {
	r := newRecRig(t, one())
	r.client.items = nil
	got, err := r.getID("stored-a")
	require.NoError(t, err)
	dto := got.(*archera.RecommendationComparisonDTO)
	require.Equal(t, archera.MatchUnavailable, dto.Status)
	require.Nil(t, dto.Current)
	require.Zero(t, r.client.cmpCalls)
}

func TestInsuranceRecommendationDeniedOrScopedMakesNoCalls(t *testing.T) {
	for name, allowed := range map[string]struct {
		permission bool
		accounts   []string
	}{"denied": {false, nil}, "scoped": {true, []string{"acct-1"}}} {
		t.Run(name, func(t *testing.T) {
			m := new(MockAuthService)
			m.On("HasPermissionAPI", mock.Anything, "u1", "view", "recommendations").Return(allowed.permission, nil)
			m.On("GetAllowedAccountsAPI", mock.Anything, "u1").Return(allowed.accounts, nil)
			r := newRecRig(t, one())
			r.h.auth = m
			_, err := r.get(sessionPrincipalCtx(), "recommendation_id=stored-a", map[string]string{"recommendation_id": "stored-a"})
			require.Error(t, err)
			require.Zero(t, r.store.calls)
			require.Zero(t, r.provider.keys)
			require.Zero(t, r.client.itemCalls)
		})
	}
}

func TestInsuranceRecommendationStoreOutcomesPrecedeVendorWork(t *testing.T) {
	for name, tc := range map[string]struct {
		rows []config.RecommendationRecord
		err  error
		code int
	}{
		"missing":   {nil, nil, 404},
		"duplicate": {append(one(), recRecord()), nil, 409},
		"error":     {nil, errors.New(recSpace), 503},
		"bad_hourly": {func() []config.RecommendationRecord {
			row := recRecord()
			row.Details = []byte(`{"offering_id":"x","plan_type":"EC2Instance","region":"r","instance_family":"m5"}`)
			return []config.RecommendationRecord{row}
		}(), nil, 422},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRecRig(t, tc.rows)
			r.store.errs = []error{tc.err}
			_, err := r.getID("stored-a")
			if tc.code == 404 {
				require.ErrorIs(t, err, errNotFound)
			} else {
				requireCode(t, err, tc.code)
			}
			require.Zero(t, r.provider.keys)
			require.Zero(t, r.client.itemCalls)
			require.Equal(t, 1, r.store.calls)
		})
	}
}

func TestInsuranceRecommendationRereadChangeDeletionOrErrorIs409(t *testing.T) {
	changed := recRecord()
	changed.Details = []byte(strings.Replace(string(changed.Details), "0.5", "0.6", 1))
	other := recRecord()
	other.Payment = "all-upfront"
	for name, tc := range map[string]struct {
		after []config.RecommendationRecord
		err   error
	}{
		"hourly": {[]config.RecommendationRecord{changed}, nil}, "payment": {[]config.RecommendationRecord{other}, nil},
		"deleted": {nil, nil}, "duplicate": {append(one(), recRecord()), nil}, "error": {nil, errors.New(recSpace)},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRecRig(t, one(), tc.after)
			r.store.errs = []error{nil, tc.err}
			got, err := r.getID("stored-a")
			require.Nil(t, got)
			requireCode(t, err, 409)
		})
	}
}

func TestInsuranceRecommendationVendorFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(*recRig)
		code  int
		retry string
	}{
		"line_items": {func(r *recRig) { r.client.itemsErr = errors.New(recSpace) }, 502, ""},
		"comparison": {func(r *recRig) { r.client.cmpErr = errors.New(recSpace) }, 502, ""},
		"rate_limited": {func(r *recRig) {
			r.client.itemsErr = &insurance.HTTPError{StatusCode: 429, RetryAfter: 3 * time.Second}
		}, 429, "3"},
		"unsupported": {func(r *recRig) { r.provider.client = quoteOnly{r.client} }, 503, ""},
		"deadline":    {func(r *recRig) { r.client.hang = true }, 504, ""},
	} {
		t.Run(name, func(t *testing.T) {
			prev := insuranceRecommendationTimeout
			insuranceRecommendationTimeout = 50 * time.Millisecond
			t.Cleanup(func() { insuranceRecommendationTimeout = prev })
			r := newRecRig(t, one())
			tc.setup(r)
			got, err := r.getID("stored-a")
			require.Nil(t, got)
			ce := requireCode(t, err, tc.code)
			if tc.retry != "" {
				require.Equal(t, tc.retry, ce.headers["Retry-After"])
			}
			require.Equal(t, 1, r.store.calls, "no reread after a vendor failure")
		})
	}
}

func TestInsuranceRecommendationQueryRules(t *testing.T) {
	long := strings.Repeat("x", 1025)
	for name, tc := range map[string]struct {
		raw    string
		params map[string]string
		code   int
	}{
		"repeated":          {"recommendation_id=stored-a&recommendation_id=stored-a", map[string]string{"recommendation_id": "stored-a"}, 400},
		"encoded_repeated":  {"recommendation_id=stored-a&%72ecommendation_id=stored-a", map[string]string{"recommendation_id": "stored-a"}, 400},
		"semicolon":         {"recommendation_id=stored-a;recommendation_id=stored-a", map[string]string{"recommendation_id": "stored-a"}, 400},
		"raw_map_missing":   {"recommendation_id=stored-a", nil, 400},
		"encoded_key_alone": {"%72ecommendation_id=stored-a", map[string]string{}, 400},
		"empty":             {"recommendation_id=", map[string]string{"recommendation_id": ""}, 400},
		"too_long":          {"recommendation_id=" + long, map[string]string{"recommendation_id": long}, 400},
		"ok":                {"recommendation_id=stored-a", map[string]string{"recommendation_id": "stored-a"}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRecRig(t, one())
			got, err := r.get(adminCtx(), tc.raw, tc.params)
			if tc.code == 0 {
				require.NoError(t, err)
				require.IsType(t, &archera.RecommendationComparisonDTO{}, got)
				return
			}
			requireCode(t, err, tc.code)
			require.Zero(t, r.store.calls)
			require.Zero(t, r.provider.keys)
		})
	}
}

func (s *recStore) ListCloudAccounts(context.Context, config.CloudAccountFilter) ([]config.CloudAccount, error) {
	return nil, nil
}
