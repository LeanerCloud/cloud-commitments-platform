package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/archera"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/secrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	insuranceHTTPOrg   = "11111111-1111-4111-8111-111111111111"
	insuranceHTTPPlan  = "22222222-2222-4222-8222-222222222222"
	insuranceHTTPToken = "synthetic-insurance-viewer-session"
	insuranceHTTPKey   = "synthetic-insurance-vendor-key"
)

type insuranceHTTPResolver struct {
	*secrets.EnvResolver
	reads []string
}

func (r *insuranceHTTPResolver) GetSecret(ctx context.Context, name string) (string, error) {
	r.reads = append(r.reads, name)
	return r.EnvResolver.GetSecret(ctx, name)
}

type insuranceHTTPStore struct {
	mockConfigStoreForHealth
	reads int
}

func (s *insuranceHTTPStore) ListStoredRecommendations(context.Context, config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	s.reads++
	return nil, nil
}

type insuranceHTTPRequest struct {
	method, url, query, body string
	headers                  http.Header
	readError                error
}

type insuranceHTTPTransport struct {
	response string
	status   int
	requests []insuranceHTTPRequest
}

func (tr *insuranceHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	var err error
	if req.Body != nil {
		body, err = io.ReadAll(req.Body)
	}
	tr.requests = append(tr.requests, insuranceHTTPRequest{
		method: req.Method, url: req.URL.String(), query: req.URL.RawQuery,
		body: string(body), headers: req.Header.Clone(), readError: err,
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: tr.status,
		Header:     http.Header{"Content-Type": {"application/json"}, "Retry-After": {"3"}},
		Body:       io.NopCloser(strings.NewReader(tr.response)),
		Request:    req,
	}, nil
}

func insuranceHTTPAuth(t *testing.T, role string) (*auth.Service, *auth.MockStore) {
	t.Helper()
	store := new(auth.MockStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	const userID = "77777777-7777-4777-8777-777777777777"
	const groupID = "88888888-8888-4888-8888-888888888888"
	group := &auth.Group{ID: groupID, Name: "Synthetic viewers"}
	if role != "denied" {
		group.Permissions = []auth.Permission{{Action: auth.ActionView, Resource: auth.ResourceRecommendations}}
	}
	if role == "scoped" {
		group.AllowedAccounts = []string{"synthetic-account"}
	}
	if role != "anonymous" {
		store.On("GetSession", mock.Anything, hashSessionTokenForTest(insuranceHTTPToken)).Return(&auth.Session{
			Token: hashSessionTokenForTest(insuranceHTTPToken), UserID: userID, ExpiresAt: time.Now().Add(time.Hour),
		}, nil)
		store.On("GetUserByID", mock.Anything, userID).Return(&auth.User{
			ID: userID, Active: true, GroupIDs: []string{groupID},
		}, nil)
		store.On("GetGroup", mock.Anything, groupID).Return(group, nil)
	}
	return auth.NewService(auth.ServiceConfig{
		Store: store, SessionDuration: time.Hour, CSRFKey: auth.TestCSRFKey(),
	}), store
}

func TestInsuranceHTTPContract(t *testing.T) {
	wire, err := os.ReadFile("testdata/insurance_comparison_wire.json")
	require.NoError(t, err)
	cases := []struct {
		name, endpoint, role string
		missing, limited     bool
		status, calls        int
		errorText            string
	}{
		{"status configured", "status", "viewer", false, false, 200, 0, ""},
		{"status missing", "status", "viewer", true, false, 200, 0, ""},
		{"comparison success", "comparison", "viewer", false, false, 200, 1, ""},
		{"comparison missing", "comparison", "viewer", true, false, 503, 0, "Archera comparison is not configured; missing settings: ARCHERA_API_KEY_SECRET, ARCHERA_ORG_ID, ARCHERA_PLAN_ID"},
		{"status anonymous", "status", "anonymous", false, false, 401, 0, "Unauthorized"},
		{"comparison anonymous", "comparison", "anonymous", false, false, 401, 0, "Unauthorized"},
		{"status denied", "status", "denied", false, false, 403, 0, "permission denied: requires view on recommendations"},
		{"comparison denied", "comparison", "denied", false, false, 403, 0, "permission denied: requires view on recommendations"},
		{"status scoped", "status", "scoped", false, false, 404, 0, "Not found"},
		{"comparison scoped", "comparison", "scoped", false, false, 404, 0, "Not found"},
		{"comparison rate limited", "comparison", "viewer", false, true, 429, 1, "Archera rate limit reached"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(archera.EnvOrgID, insuranceHTTPOrg)
			t.Setenv(archera.EnvPlanID, insuranceHTTPPlan)
			t.Setenv(archera.EnvKeySecret, "INSURANCE_HTTP_SYNTHETIC_KEY")
			t.Setenv("INSURANCE_HTTP_SYNTHETIC_KEY", insuranceHTTPKey)
			if tc.missing {
				for _, name := range []string{archera.EnvKeySecret, archera.EnvOrgID, archera.EnvPlanID} {
					t.Setenv(name, "")
				}
			}
			resolver := &insuranceHTTPResolver{EnvResolver: secrets.NewEnvResolver()}
			transport := &insuranceHTTPTransport{status: 200, response: string(wire)}
			if tc.limited {
				transport.status, transport.response = 429, `{"message":"Synthetic request rate exceeded"}`
			}
			service, authStore := insuranceHTTPAuth(t, tc.role)
			store := &insuranceHTTPStore{}
			app := &Application{API: api.NewHandler(api.HandlerConfig{
				AuthService: newAuthServiceAdapter(service), ConfigStore: store,
				Insurance:         archera.NewProvider(archera.SettingsFromEnv(), resolver, &http.Client{Transport: transport}),
				CORSAllowedOrigin: "https://insurance.example.invalid",
			})}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/insurance/"+tc.endpoint, nil)
			if tc.role != "anonymous" {
				req.Header.Set("Authorization", "Bearer "+insuranceHTTPToken)
			}
			recorder := httptest.NewRecorder()
			beforeCall := time.Now()
			app.handleHTTPRequest(recorder, req)
			afterCall := time.Now()
			observation := struct {
				status         int
				headers        http.Header
				body           string
				auth           []mock.Call
				requests       []insuranceHTTPRequest
				resolver       []string
				vendor, stored int
			}{recorder.Code, recorder.Header().Clone(), recorder.Body.String(), append([]mock.Call(nil), authStore.Calls...), append([]insuranceHTTPRequest(nil), transport.requests...), append([]string(nil), resolver.reads...), len(transport.requests), store.reads}
			var authReads []string
			for _, call := range observation.auth {
				authReads = append(authReads, call.Method)
			}
			t.Logf("observation status=%d headers=%v body=%s auth=%v resolver=%v vendor=%d stored=%d", observation.status, observation.headers, observation.body, authReads, observation.resolver, observation.vendor, observation.stored)
			for i, request := range observation.requests {
				t.Logf("request[%d] method=%s url=%s query=%q body=%q read_error=%v accept=%q key_matches=%t authorization_absent=%t", i, request.method, request.url, request.query, request.body, request.readError, request.headers.Get("Accept"), request.headers.Get("X-API-Key") == insuranceHTTPKey, request.headers.Get("Authorization") == "")
			}
			assert.Equal(t, tc.status, observation.status, "native status")
			assert.Equal(t, "application/json", observation.headers.Get("Content-Type"))
			assert.Len(t, observation.resolver, tc.calls, "resolver calls")
			assert.Equal(t, tc.calls, observation.vendor, "vendor calls")
			assert.Zero(t, observation.stored, "stored recommendations")
			if tc.role == "anonymous" {
				assert.Empty(t, authReads)
			} else {
				for _, method := range []string{"GetSession", "GetUserByID", "GetGroup"} {
					assert.Contains(t, authReads, method, "authenticated fixture reads")
				}
			}
			for _, name := range observation.resolver {
				assert.Equal(t, "INSURANCE_HTTP_SYNTHETIC_KEY", name)
			}
			for _, request := range observation.requests {
				assert.NoError(t, request.readError, "request capture must be complete")
				assert.Equal(t, http.MethodGet, request.method)
				assert.Equal(t, "https://api.archera.ai/v1/org/"+insuranceHTTPOrg+"/commitment-plans/"+insuranceHTTPPlan+"/comparison", request.url)
				assert.Empty(t, request.query)
				assert.Empty(t, request.body)
				assert.True(t, request.headers.Get("X-API-Key") == insuranceHTTPKey, "synthetic key matches")
				assert.Equal(t, "application/json", request.headers.Get("Accept"))
				assert.True(t, request.headers.Get("Authorization") == "", "bearer must not reach vendor")
			}
			if tc.limited {
				assert.Equal(t, "3", observation.headers.Get("Retry-After"), "native Retry-After")
				assert.NotContains(t, observation.body, "Synthetic request rate exceeded")
			}
			var body map[string]any
			decoder := json.NewDecoder(strings.NewReader(observation.body))
			decoder.UseNumber()
			if !assert.NoError(t, decoder.Decode(&body), "response JSON prerequisite") {
				return
			}
			if tc.errorText != "" {
				want := map[string]any{"error": tc.errorText}
				if tc.limited {
					want["retry_after_seconds"] = json.Number("3")
				}
				assert.Equal(t, want, body, "fixed error shape and text")
			}
			if (tc.status == 200 || observation.status == 200) && tc.endpoint == "status" {
				missing := []any{}
				if tc.missing {
					missing = []any{"ARCHERA_API_KEY_SECRET", "ARCHERA_ORG_ID", "ARCHERA_PLAN_ID"}
				}
				assert.Equal(t, map[string]any{"configured": !tc.missing, "missing": missing}, body, "presence-only status")
			}
			if (tc.status == 200 || observation.status == 200) && tc.endpoint == "comparison" {
				assertInsuranceHTTPComparison(t, body, beforeCall, afterCall)
			}
		})
	}
}

func insuranceHTTPFinancials(values [6]any) map[string]any {
	return map[string]any{
		"monthly_730h_commitment_cost_total":  values[0],
		"monthly_730h_cloud_provider_cost":    values[1],
		"monthly_730h_archera_premium":        values[2],
		"monthly_730h_gross_savings":          values[3],
		"monthly_730h_net_savings":            values[4],
		"monthly_730h_covered_on_demand_cost": values[5],
	}
}

func assertInsuranceHTTPComparison(t *testing.T, body map[string]any, beforeCall, afterCall time.Time) {
	t.Helper()
	fetched, ok := body["fetched_at"].(string)
	assert.True(t, ok, "fetched_at string")
	parsed, err := time.Parse(time.RFC3339, fetched)
	if assert.NoError(t, err, "fetched_at RFC3339") {
		assert.False(t, parsed.Before(beforeCall.UTC().Truncate(time.Second)))
		assert.False(t, parsed.After(afterCall.UTC().Truncate(time.Second)))
	}
	currentTotals := insuranceHTTPFinancials([6]any{"101.01", "102.02", "103.03", "104.04", "105.05", "106.06"})
	currentTotals["upfront_one_time_cost"] = "107.07"
	hypotheticalTotals := insuranceHTTPFinancials([6]any{"201.01", "202.02", "203.03", "204.04", "205.05", "206.06"})
	hypotheticalTotals["upfront_one_time_cost"] = "207.07"
	current := map[string]any{
		"offer_id":                "44444444-4444-4444-8444-444444444444",
		"is_current":              true,
		"provider":                "aws",
		"commitment_type":         "aws/AmazonEC2",
		"region":                  "us-east-1",
		"contract_term":           "one_year",
		"payment_option":          "no_upfront",
		"lease_attached":          true,
		"lease_menu_item_id":      "66666666-6666-4666-8666-666666666666",
		"archera_offer_name":      "Synthetic guaranteed offer",
		"archera_offer_name_note": "Archera's name for this offer; a plan comparison is a hypothetical rollup, not a bindable quote, and an insured target is subject to Archera underwriting allowances, not a guarantee.",
		"archera_product_support": map[string]any{
			"status":                 "supported",
			"source":                 "https://docs.archera.ai/help-center/getting-started/supported-reservable-services",
			"evidence":               `AWS Reserved Instances table, service "EC2": Guaranteed terms "30 day, 1 year". Mapping of API type "aws/AmazonEC2" to page service "EC2" is inferred from the schema example; not stated by Archera.`,
			"underwriting_allowance": "unknown",
			"customer_eligibility":   "unknown",
		},
		"discount_rate":         "0.308",
		"breakeven_days":        nil,
		"monthly":               insuranceHTTPFinancials([6]any{"301.01", "302.02", "303.03", "304.04", "305.05", "306.06"}),
		"upfront_one_time_cost": "307.07",
		"delta_vs_current": map[string]any{
			"monthly_730h_net_savings": "311.11",
			"upfront_one_time_cost":    "312.12",
			"discount_rate":            "0.313",
			"breakeven_days":           "314.14",
		},
	}
	candidate := map[string]any{
		"offer_id":           "55555555-5555-4555-8555-555555555555",
		"is_current":         false,
		"provider":           "gcp",
		"commitment_type":    "gcp/other",
		"region":             nil,
		"contract_term":      nil,
		"payment_option":     nil,
		"lease_attached":     false,
		"lease_menu_item_id": nil,
		"archera_product_support": map[string]any{
			"status":                 "unknown",
			"underwriting_allowance": "unknown",
			"customer_eligibility":   "unknown",
		},
		"discount_rate":         "0.408",
		"breakeven_days":        "409.09",
		"monthly":               insuranceHTTPFinancials([6]any{"401.01", "402.02", "0", "404.04", "405.05", nil}),
		"upfront_one_time_cost": "407.07",
		"delta_vs_current": map[string]any{
			"monthly_730h_net_savings": "-411.11",
			"upfront_one_time_cost":    "412.12",
			"discount_rate":            "-0.413",
			"breakeven_days":           "414.14",
		},
	}
	want := map[string]any{
		"title":            "Archera commitment plan comparison (read-only)",
		"plan_id":          insuranceHTTPPlan,
		"fetched_at":       fetched,
		"currency":         nil,
		"currency_note":    "currency not provided by Archera",
		"premium_included": true,
		"basis_note":       "Monthly figures are 730-hour monthly rates; the Archera premium is already included in commitment cost totals; upfront figures are one-time amounts and are never summed with monthly rates.",
		"delta_basis_note": "Hypothetical deltas compare against the current plan, not against on-demand.",
		"current":          currentTotals,
		"hypotheticals": []any{map[string]any{
			"contract_term":  "three_year",
			"payment_option": "partial_upfront",
			"totals":         hypotheticalTotals,
			"delta_vs_current": map[string]any{
				"monthly_730h_net_savings":     "211.11",
				"monthly_730h_commitment_cost": "-212.12",
				"upfront_one_time_cost":        "213.13",
			},
			"line_items": []any{map[string]any{
				"line_item_id":           "33333333-3333-4333-8333-333333333333",
				"reason":                 "fallback_closest_shorter",
				"actual_term":            "one_year",
				"actual_payment_option":  "all_upfront",
				"actual_commitment_type": "aws/AmazonEC2",
			}},
		}},
		"rows": []any{map[string]any{
			"line_item_id": "33333333-3333-4333-8333-333333333333",
			"current":      current,
			"candidates":   []any{candidate},
		}},
		"non_gating_disclosure":  common.ArcheraNonGatingDisclosure,
		"sponsorship_disclosure": common.ArcheraSponsorshipDisclosure,
	}
	assert.Equal(t, want, body, "complete comparison keys, shapes, types and independent values")
	for _, forbidden := range []string{"org_id", "api_key", "raw_body"} {
		assert.NotContains(t, body, forbidden)
	}
}
