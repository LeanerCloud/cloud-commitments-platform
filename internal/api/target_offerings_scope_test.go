package api

import (
	"context"
	"testing"

	ec2svc "github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/ec2"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// GET /api/ri-exchange/target-offerings reads the deployment's own AWS
// account (issue #520): a caller whose allowed_accounts exclude that account
// must get the same not-found as an unknown source RI, and the AWS call must
// not happen.

const targetOfferingsSourceRI = "296818b6-73f8-4cd2-94bc-dbb95f794812"

func targetOfferingsHandler(t *testing.T, deploymentAccountID string, scoped bool) (*Handler, *stubTargetOfferingsEC2) {
	t.Helper()
	var h *Handler
	if scoped {
		var store *MockConfigStore
		h, store = scopedHandler(t, scopedInAccount)
		store.On("ListCloudAccounts", mock.Anything, mock.Anything).
			Return([]config.CloudAccount{inScopeAccount(), outOfScopeAccount()}, nil).Maybe()
	} else {
		h, _ = scopedHandler(t)
	}
	stub := &stubTargetOfferingsEC2{
		instances: []ec2svc.ConvertibleRI{{ReservedInstanceID: targetOfferingsSourceRI}},
		offerings: []ec2svc.TargetOffering{{OfferingID: "4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91"}},
	}
	h.targetOfferingsEC2Factory = func(_ aws.Config) targetOfferingsEC2Client { return stub }
	h.reshapeAccountResolver = func(_ context.Context) (string, error) { return deploymentAccountID, nil }
	h.awsCfgOnce.Do(func() { h.awsCfg = aws.Config{Region: "us-east-1"} })
	return h, stub
}

func targetOfferingsRequest() *events.LambdaFunctionURLRequest {
	req := scopedRequest("")
	req.QueryStringParameters = map[string]string{"source_ri_id": targetOfferingsSourceRI}
	return req
}

func TestRouterDispatch_TargetOfferings_DeploymentAccountOutOfScope(t *testing.T) {
	h, _ := targetOfferingsHandler(t, scopedOutAccount, true)

	res, err := NewRouter(h).Route(context.Background(), "GET", "/api/ri-exchange/target-offerings", targetOfferingsRequest())

	require.Error(t, err, "got response %v", res)
	assert.True(t, IsNotFoundError(err), "expected not-found, got %v", err)
}

func TestRouterDispatch_TargetOfferings_DeploymentAccountInScope(t *testing.T) {
	h, _ := targetOfferingsHandler(t, scopedInAccount, true)

	res, err := NewRouter(h).Route(context.Background(), "GET", "/api/ri-exchange/target-offerings", targetOfferingsRequest())

	require.NoError(t, err)
	resp, ok := res.(*TargetOfferingsResponse)
	require.True(t, ok)
	assert.Len(t, resp.Offerings, 1)
}

func TestRouterDispatch_TargetOfferings_UnscopedUserSkipsAccountLookup(t *testing.T) {
	h, _ := targetOfferingsHandler(t, scopedOutAccount, false)
	// No ListCloudAccounts expectation: any scope lookup would panic the mock.
	h.reshapeAccountResolver = func(_ context.Context) (string, error) {
		t.Fatal("unrestricted session must not resolve the deployment account")
		return "", nil
	}

	res, err := NewRouter(h).Route(context.Background(), "GET", "/api/ri-exchange/target-offerings", targetOfferingsRequest())

	require.NoError(t, err)
	_, ok := res.(*TargetOfferingsResponse)
	assert.True(t, ok)
}
