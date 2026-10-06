package api

import (
	"context"
	"errors"
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

func targetOfferingsHandler(t *testing.T, deploymentAccountID string, scoped bool) *Handler {
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
	return h
}

func targetOfferingsRequest() *events.LambdaFunctionURLRequest {
	req := scopedRequest("")
	req.QueryStringParameters = map[string]string{"source_ri_id": targetOfferingsSourceRI}
	return req
}

func TestRouterDispatch_TargetOfferings_DeploymentAccountOutOfScope(t *testing.T) {
	h := targetOfferingsHandler(t, scopedOutAccount, true)

	res, err := NewRouter(h).Route(context.Background(), "GET", "/api/ri-exchange/target-offerings", targetOfferingsRequest())

	require.Error(t, err, "got response %v", res)
	assert.True(t, IsNotFoundError(err), "expected not-found, got %v", err)
}

func TestRouterDispatch_TargetOfferings_DeploymentAccountInScope(t *testing.T) {
	h := targetOfferingsHandler(t, scopedInAccount, true)

	res, err := NewRouter(h).Route(context.Background(), "GET", "/api/ri-exchange/target-offerings", targetOfferingsRequest())

	require.NoError(t, err)
	resp, ok := res.(*TargetOfferingsResponse)
	require.True(t, ok)
	assert.Len(t, resp.Offerings, 1)
}

func TestRouterDispatch_TargetOfferings_UnscopedUserSkipsAccountLookup(t *testing.T) {
	h := targetOfferingsHandler(t, scopedOutAccount, false)
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

func TestRouterDispatch_TargetOfferings_ResolverErrorPropagates(t *testing.T) {
	h := targetOfferingsHandler(t, scopedInAccount, true)
	resolveErr := errors.New("deployment account lookup failed")
	h.reshapeAccountResolver = func(_ context.Context) (string, error) { return "", resolveErr }

	res, err := NewRouter(h).Route(context.Background(), "GET", "/api/ri-exchange/target-offerings", targetOfferingsRequest())

	require.Error(t, err, "got response %v", res)
	assert.ErrorIs(t, err, resolveErr)
	assert.False(t, IsNotFoundError(err))
}

func TestListTargetOfferings_PermissionCheckPrecedesVisibility(t *testing.T) {
	h := targetOfferingsHandler(t, scopedOutAccount, true)
	req := targetOfferingsRequest()
	req.Headers = nil // no session

	res, err := h.listTargetOfferings(context.Background(), req)

	require.Error(t, err, "got response %v", res)
	assert.Contains(t, err.Error(), "no authorization token")
	assert.False(t, IsNotFoundError(err))
}
