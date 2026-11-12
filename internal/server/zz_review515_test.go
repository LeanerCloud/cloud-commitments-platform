package server

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestReview515_UnauthPathVariants(t *testing.T) {
	t.Setenv("CUDLY_SOURCE_CLOUD", "gcp")
	ms := new(auth.MockStore)
	ms.On("AdminExists", mock.Anything).Return(true, nil).Maybe()
	svc := auth.NewService(auth.ServiceConfig{Store: ms, SessionDuration: time.Hour, CSRFKey: auth.TestCSRFKey()})
	h := struct{ handler *api.Handler }{api.NewHandler(api.HandlerConfig{AuthService: newAuthServiceAdapter(svc), CORSAllowedOrigin: "https://dashboard.example.com"})}
	for _, p := range []string{
		"/api/info", "/api/info/", "/api/info/deployment", "/api/info/deployment/",
		"//api/info/deployment", "/api/info//deployment", "/api/info/./deployment",
		"/API/INFO/DEPLOYMENT", "/api/info/%64eployment", "/Api/info", "/api/infox",
	} {
		req := &events.LambdaFunctionURLRequest{
			Headers:        map[string]string{},
			RawQueryString: "a=1",
			RequestContext: events.LambdaFunctionURLRequestContext{
				HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: "GET", Path: p},
			},
		}
		resp, err := h.handler.HandleRequest(context.Background(), req)
		require.NoError(t, err)
		t.Logf("UNAUTH %-28s -> %d %s", p, resp.StatusCode, resp.Body)
		if p != "/api/info" {
			require.NotEqual(t, 200, resp.StatusCode, p)
		}
	}
}

func TestReview515_APIKeyDeploymentBooks(t *testing.T) {
	t.Setenv("CUDLY_SOURCE_CLOUD", "gcp")
	h := newUsageBookingHarness(t, []auth.Permission{{Action: auth.ActionView, Resource: auth.ResourceAPIKeys}})
	resp, err := h.handler.HandleRequest(context.Background(), h.request("GET", "/api/info/deployment"))
	require.NoError(t, err)
	t.Logf("APIKEY /api/info/deployment -> %d validations=%d", resp.StatusCode, h.validations.Load())
	require.Equal(t, 200, resp.StatusCode)
	h.requireExactlyOneBooking(t)
	t.Logf("booked total=%d", h.totalDelta.Load())
}
