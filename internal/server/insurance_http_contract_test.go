package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/archera"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/secrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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
	return nil, errors.New("unexpected synthetic insurance vendor request")
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
	cases := []struct {
		name, role string
		missing    bool
		status     int
		errorText  string
	}{
		{"status configured", "viewer", false, 200, ""},
		{"status missing", "viewer", true, 200, ""},
		{"status anonymous", "anonymous", false, 401, "Unauthorized"},
		{"status denied", "denied", false, 403, "permission denied: requires view on recommendations"},
		{"status scoped", "scoped", false, 404, "Not found"},
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
			transport := &insuranceHTTPTransport{}
			service, authStore := insuranceHTTPAuth(t, tc.role)
			store := &insuranceHTTPStore{}
			app := &Application{API: api.NewHandler(api.HandlerConfig{
				AuthService: newAuthServiceAdapter(service), ConfigStore: store,
				Insurance:         archera.NewProvider(archera.SettingsFromEnv(), resolver, &http.Client{Transport: transport}),
				CORSAllowedOrigin: "https://insurance.example.invalid",
			})}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/insurance/status", nil)
			if tc.role != "anonymous" {
				req.Header.Set("Authorization", "Bearer "+insuranceHTTPToken)
			}
			recorder := httptest.NewRecorder()
			app.handleHTTPRequest(recorder, req)
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
				t.Logf("request[%d] method=%s url=%s query=%q body=%q read_error=%v accept=%q key_present=%t authorization_present=%t", i, request.method, request.url, request.query, request.body, request.readError, request.headers.Get("Accept"), request.headers.Get("X-API-Key") != "", request.headers.Get("Authorization") != "")
			}
			assert.Equal(t, tc.status, observation.status, "native status")
			assert.Equal(t, "application/json", observation.headers.Get("Content-Type"))
			assert.Empty(t, observation.resolver, "resolver calls")
			assert.Zero(t, observation.vendor, "vendor calls")
			assert.Zero(t, observation.stored, "stored recommendations")
			if tc.role == "anonymous" {
				assert.Empty(t, authReads)
			} else {
				for _, method := range []string{"GetSession", "GetUserByID", "GetGroup"} {
					assert.Contains(t, authReads, method, "authenticated fixture reads")
				}
			}
			for _, request := range observation.requests {
				assert.NoError(t, request.readError, "request capture must be complete")
			}
			var body map[string]any
			decoder := json.NewDecoder(strings.NewReader(observation.body))
			decoder.UseNumber()
			if !assert.NoError(t, decoder.Decode(&body), "response JSON prerequisite") {
				return
			}
			var extra any
			assert.ErrorIs(t, decoder.Decode(&extra), io.EOF, "response must contain exactly one JSON value")
			if tc.errorText != "" {
				want := map[string]any{"error": tc.errorText}
				assert.Equal(t, want, body, "fixed error shape and text")
			}
			if tc.status == 200 || observation.status == 200 {
				missing := []any{}
				if tc.missing {
					missing = []any{"ARCHERA_API_KEY_SECRET", "ARCHERA_ORG_ID", "ARCHERA_PLAN_ID"}
				}
				assert.Equal(t, map[string]any{"configured": !tc.missing, "missing": missing}, body, "presence-only status")
			}
		})
	}
}
