package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/archera"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/secrets"
)

// The vendor here is synthetic transport behind the real archera.Provider and insurance.Client; it is not
// a live Archera organisation.

const contractOrg = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

type contractSecret struct {
	secrets.Resolver
	calls int
}

func (s *contractSecret) GetSecret(context.Context, string) (string, error) {
	s.calls++
	return insTestKey, nil
}

type contractVendor struct {
	t        *testing.T
	scenario string
	calls    []*url.URL
}

func contractItem(id, account, payment string) string {
	return fmt.Sprintf(`{"id":%q,"plan_id":%q,"offer_id":%q,"account_id":%s,"selected_quantity":7,"selected_commitment":0.5,"is_selected":true,"contract_term":"one_year_gris","payment_option":%q,"contract_spec":{"commitment_type":"x"},"offer":{"id":%q,"type":"savings_plan","provider":"aws","provider_offering_id":"provider-offer-a","offering_id":"cat","duration_seconds":31536000,"region":"us-east-1","az":null,"instance_type":null,"instance_family":"m5","product_description":null,"tenancy":null,"plan_type":"EC2Instance","is_multi_az":null,"is_flexible":null,"payment_option":%q}}`, id, insTestPlan, recOffer, account, payment, recOffer, payment)
}

func contractUnrelatedID(i int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", i) }

func contractOfferRow(current bool, payment, offer string) string {
	return fmt.Sprintf(`{"is_current":%t,"offer_id":%q,"selected_amount":0.5,"commitment_type":"savings_plan","contract_term":"one_year_gris","payment_option":%q,"offer":{"provider":"aws","region":"us-east-1"},"discount_rate":0.1,"commitment_upfront_cost":1200,"commitment_financials_monthly_rate":{},"delta_vs_current":{"monthly_net_savings":5,"upfront_cost":1,"discount_rate":0.1}}`, current, offer, payment)
}

func contractComparison(scenario string) string {
	id := recItem
	if scenario == "comparison_missing" {
		id = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	}
	currentPay, candPay := "no_upfront", "partial_upfront"
	if scenario == "partial" {
		currentPay, candPay = "partial_upfront", "all_upfront"
	}
	cand := contractOfferRow(false, candPay, "faadf00d-ffff-4aba-8ada-abcdefabcdef")
	row := `{"line_item_id":"` + id + `","current":` + contractOfferRow(true, currentPay, recOffer) + `,"candidates":[` + cand + `]}`
	unrelated := `{"line_item_id":"` + contractUnrelatedID(1) + `","current":` + strings.Replace(contractOfferRow(true, currentPay, recOffer), `"commitment_upfront_cost":1200`, `"commitment_upfront_cost":99999999`, 1) + `,"candidates":[]}`
	return `{"current_totals":{"commitment_financials_monthly_rate":{},"commitment_upfront_cost":987654321},"hypothetical_totals":[],"data":[` + unrelated + `,` + row + `]}`
}

func (v *contractVendor) RoundTrip(req *http.Request) (*http.Response, error) {
	v.calls = append(v.calls, req.URL)
	require.Equal(v.t, http.MethodGet, req.Method)
	require.Equal(v.t, "https", req.URL.Scheme)
	require.Equal(v.t, "api.archera.ai", req.URL.Host)
	require.Equal(v.t, insTestKey, req.Header.Get("x-api-key"))
	prefix := "/v1/org/" + contractOrg + "/commitment-plans/" + insTestPlan
	q := req.URL.Query()
	headers := http.Header{}
	status, body := http.StatusOK, ""
	switch req.URL.Path {
	case prefix + "/line-items":
		page := q.Get("page")
		require.Equal(v.t, "100", q.Get("page_size"))
		require.Contains(v.t, []string{"1", "2"}, page)
		headers.Set("X-Pagination", `{"total":101,"total_pages":2,"page":`+page+`,"first_page":1,"last_page":2}`)
		if page == "1" {
			rows := make([]string, 0, 100)
			for i := 1; i <= 100; i++ {
				rows = append(rows, contractItem(contractUnrelatedID(i), `"unrelated-source"`, "No Upfront"))
			}
			body = "[" + strings.Join(rows, ",") + "]"
			break
		}
		account := `"Source-account-a"`
		if v.scenario == "source_case" {
			account = `"source-account-a"`
		}
		payment := "No Upfront"
		if v.scenario == "partial" {
			payment = "Partial Upfront"
		}
		body = "[" + contractItem(recItem, account, payment) + "]"
		if v.scenario == "late_failure" {
			status, body = http.StatusInternalServerError, `{"message":"`+recSpace+`"}`
		}
	case prefix + "/comparison":
		body = contractComparison(v.scenario)
	default:
		v.t.Fatalf("unexpected vendor path: %s", req.URL.Path)
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func contractRig(t *testing.T, scenario string, rows []config.RecommendationRecord) (*Handler, *recStore, *contractSecret, *contractVendor) {
	t.Helper()
	store := &recStore{results: [][]config.RecommendationRecord{rows}}
	resolver := &contractSecret{}
	vendor := &contractVendor{t: t, scenario: scenario}
	provider := archera.NewProvider(archera.Settings{OrgID: contractOrg, PlanID: insTestPlan, KeySecretRef: insTestRef}, resolver, &http.Client{Transport: vendor})
	return &Handler{config: store, insurance: provider}, store, resolver, vendor
}

func contractGet(h *Handler) (any, error) {
	return (&recRig{h: h}).getID("stored-a")
}

func TestInsuranceRecommendationRealClientReadsPageTwoAndScopesComparison(t *testing.T) {
	h, store, resolver, vendor := contractRig(t, "complete", one())
	got, err := contractGet(h)
	require.NoError(t, err)
	dto := got.(*archera.RecommendationComparisonDTO)
	require.Equal(t, archera.ResourceAssociated, dto.Status)
	require.Equal(t, "stored-a", dto.RecommendationID)
	require.Nil(t, dto.Currency)
	require.Nil(t, dto.ComparableDelta)
	require.Len(t, dto.Candidates, 1)

	require.Len(t, vendor.calls, 3)
	prefix := "/v1/org/" + contractOrg + "/commitment-plans/" + insTestPlan
	for i, page := range []string{"1", "2"} {
		require.Equal(t, prefix+"/line-items", vendor.calls[i].Path)
		require.Equal(t, page, vendor.calls[i].Query().Get("page"))
	}
	cq := vendor.calls[2].Query()
	require.Equal(t, prefix+"/comparison", vendor.calls[2].Path)
	require.Equal(t, []string{recItem}, cq["line_item_ids"])
	require.ElementsMatch(t, []string{"no_upfront", "partial_upfront", "all_upfront"}, cq["payment_options"])
	require.Equal(t, 1, resolver.calls)
	require.Equal(t, 2, store.calls, "stored row read before and after the vendor fetch")

	body, err := json.Marshal(dto)
	require.NoError(t, err)
	for _, private := range []string{"Source-account-a", "unrelated-source", contractOrg, insTestPlan, "99999999", "987654321", contractUnrelatedID(1)} {
		require.NotContains(t, string(body), private)
	}
}

func TestInsuranceRecommendationRealClientPartialUpfrontRecommendationMatches(t *testing.T) {
	row := recRecord()
	row.Payment = "partial-upfront"
	h, _, _, vendor := contractRig(t, "partial", []config.RecommendationRecord{row})
	got, err := contractGet(h)
	require.NoError(t, err)
	require.Equal(t, archera.ResourceAssociated, got.(*archera.RecommendationComparisonDTO).Status)
	require.Len(t, vendor.calls, 3)
}

func TestInsuranceRecommendationRealClientRefusesPartialOrUnprovenData(t *testing.T) {
	for _, scenario := range []string{"late_failure", "comparison_missing", "source_case"} {
		t.Run(scenario, func(t *testing.T) {
			h, _, _, vendor := contractRig(t, scenario, one())
			got, err := contractGet(h)
			if scenario == "late_failure" {
				require.Nil(t, got)
				ce := requireCode(t, err, 502)
				require.NotContains(t, ce.message, recSpace)
				require.Len(t, vendor.calls, 2, "no comparison call after a failed page")
				return
			}
			require.NoError(t, err)
			dto := got.(*archera.RecommendationComparisonDTO)
			require.Equal(t, archera.MatchUnavailable, dto.Status)
			require.Nil(t, dto.Current)
			require.Empty(t, dto.Candidates)
		})
	}
}

func TestInsuranceRecommendationRealClientRowChangedAfterFetchIs409(t *testing.T) {
	h, store, _, vendor := contractRig(t, "complete", one())
	changed := recRecord()
	changed.Details = []byte(strings.Replace(string(changed.Details), "0.5", "0.6", 1))
	store.results = append(store.results, []config.RecommendationRecord{changed})
	got, err := contractGet(h)
	require.Nil(t, got)
	requireCode(t, err, 409)
	require.Len(t, vendor.calls, 3)
}

func TestInsuranceRecommendationRealClientStoreFailuresMakeNoVendorCalls(t *testing.T) {
	for name, rows := range map[string][]config.RecommendationRecord{"duplicate_store": append(one(), recRecord()), "missing": nil} {
		t.Run(name, func(t *testing.T) {
			h, _, resolver, vendor := contractRig(t, "complete", rows)
			_, err := contractGet(h)
			require.Error(t, err)
			require.Zero(t, resolver.calls)
			require.Empty(t, vendor.calls)
		})
	}
}

func TestInsuranceRecommendationRealClientUnauthorizedMakesNoVendorCalls(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		h, store, resolver, vendor := contractRig(t, "complete", one())
		m := new(MockAuthService)
		m.On("HasPermissionAPI", mock.Anything, "u1", "view", "recommendations").Return(scoped, nil)
		m.On("GetAllowedAccountsAPI", mock.Anything, "u1").Return([]string{"acct-1"}, nil)
		h.auth = m
		_, err := (&recRig{h: h}).get(sessionPrincipalCtx(), "recommendation_id=stored-a", map[string]string{"recommendation_id": "stored-a"})
		require.Error(t, err)
		require.Zero(t, store.calls)
		require.Zero(t, resolver.calls)
		require.Empty(t, vendor.calls)
	}
}
