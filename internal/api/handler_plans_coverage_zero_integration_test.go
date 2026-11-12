//go:build integration
// +build integration

package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// A plan's target_coverage of 0 goes through PUT /api/plans/{id} into Postgres
// and comes back as 0 on GET; an omitted field keeps the stored 0 and an
// explicit 80 replaces it (#523). A mock store cannot show the column round trip.
func TestUpdatePlan_TargetCoverageZeroRoundTripsThroughPostgres(t *testing.T) {
	ctx := context.Background()
	f := newCreateConcurrencyFixture(ctx, t)
	auth := f.handler.auth.(*MockAuthService)
	t.Cleanup(func() { auth.AssertExpectations(t) })
	auth.On("ValidateCSRFToken", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	call := func(method, body string) *events.LambdaFunctionURLResponse {
		t.Helper()
		resp, err := f.handler.HandleRequest(ctx, &events.LambdaFunctionURLRequest{
			Headers: map[string]string{"Authorization": "Bearer admin-token", "Content-Type": "application/json", "X-CSRF-Token": "test-csrf"}, Body: body,
			RequestContext: events.LambdaFunctionURLRequestContext{HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: method, Path: "/api/plans/" + f.planID}},
		})
		require.NoError(t, err)
		return resp
	}
	const base = `"name":"Coverage plan","provider":"azure","service":"compute","ramp_schedule":"weekly-25pct","notification_days_before":2`
	for _, step := range []struct {
		name, extra string
		want        float64
	}{
		{"explicit zero", `,"target_coverage":0`, 0},
		{"omitted keeps the stored zero", ``, 0},
		{"explicit 80", `,"target_coverage":80`, 80},
	} {
		resp := call("PUT", `{`+base+step.extra+`}`)
		require.Equal(t, 200, resp.StatusCode, step.name+": "+resp.Body)
		stored, err := f.store.GetPurchasePlan(ctx, f.planID)
		require.NoError(t, err)
		assert.Equal(t, step.want, stored.Services["azure:compute"].Coverage, step.name+": stored")

		resp = call("GET", "")
		require.Equal(t, 200, resp.StatusCode, resp.Body)
		var returned config.PurchasePlan
		require.NoError(t, json.Unmarshal([]byte(resp.Body), &returned))
		assert.Equal(t, step.want, returned.Services["azure:compute"].Coverage, step.name+": GET")
	}
}
