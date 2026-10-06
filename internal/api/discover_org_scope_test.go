package api

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/accounts"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// POST /api/accounts/discover-org lists the organization with the org root's
// own credentials and persists every member as a cloud_accounts row, so a
// caller scoped to [A] must not be able to aim it at an org root outside
// their scope (issue #520).

// countingCredStore records credential loads so tests can prove an org root's
// credentials were never resolved.
type countingCredStore struct {
	*fakeCredStore
	loads int
}

func (c *countingCredStore) LoadRaw(ctx context.Context, accountID, credType string) ([]byte, error) {
	c.loads++
	return c.fakeCredStore.LoadRaw(ctx, accountID, credType)
}

type discoverOrgProbe struct {
	h       *Handler
	store   *MockConfigStore
	creds   *countingCredStore
	called  bool
	created []config.CloudAccount
}

func newDiscoverOrgProbe(t *testing.T, rootID string, scope ...string) *discoverOrgProbe {
	t.Helper()
	h, store := scopedHandler(t, scope...)
	p := &discoverOrgProbe{h: h, store: store}
	store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		root := orgRootAccount()
		root.ID = rootID
		if id != rootID {
			return nil, nil
		}
		return root, nil
	}
	store.ListCloudAccountsFn = func(_ context.Context, _ config.CloudAccountFilter) ([]config.CloudAccount, error) {
		return nil, nil
	}
	store.CreateCloudAccountFn = func(_ context.Context, a *config.CloudAccount) error {
		p.created = append(p.created, *a)
		return nil
	}
	p.creds = &countingCredStore{fakeCredStore: &fakeCredStore{data: map[string][]byte{
		rootID + "::aws_access_keys": []byte(`{"access_key_id":"AKIATEST","secret_access_key":"shh"}`),
	}}}
	h.credStore = p.creds
	h.discoverOrgFn = func(_ context.Context, _ aws.Config) (*accounts.OrgDiscoveryResult, error) {
		p.called = true
		return &accounts.OrgDiscoveryResult{Accounts: []config.CloudAccount{
			{Provider: "aws", ExternalID: "300000000003", Name: "Member"},
		}}, nil
	}
	return p
}

func discoverOrgBody(rootID string) string {
	return `{"account_id":"` + rootID + `"}`
}

func TestRouterDispatch_DiscoverOrg_OutOfScopeRootRefused(t *testing.T) {
	p := newDiscoverOrgProbe(t, scopedOutAccount, scopedInAccount)

	_, err := NewRouter(p.h).Route(context.Background(), "POST", "/api/accounts/discover-org",
		scopedRequest(discoverOrgBody(scopedOutAccount)))

	require.Error(t, err)
	assert.True(t, IsNotFoundError(err), "expected the enumeration-safe not-found, got %v", err)
	assert.Zero(t, p.creds.loads, "the out-of-scope org root's credentials must not be loaded")
	assert.False(t, p.called, "organization listing must not run")
	assert.Empty(t, p.created, "no member rows may be persisted")
}

func TestRouterDispatch_DiscoverOrg_InScopeRootAllowed(t *testing.T) {
	p := newDiscoverOrgProbe(t, scopedInAccount, scopedInAccount)

	res, err := NewRouter(p.h).Route(context.Background(), "POST", "/api/accounts/discover-org",
		scopedRequest(discoverOrgBody(scopedInAccount)))

	require.NoError(t, err)
	dr, ok := res.(DiscoverOrgResult)
	require.True(t, ok, "result type = %T", res)
	assert.Equal(t, 1, dr.Created)
	assert.True(t, p.called)
}

func TestRouterDispatch_DiscoverOrg_UnrestrictedAdminUnchanged(t *testing.T) {
	p := newDiscoverOrgProbe(t, scopedOutAccount)

	res, err := NewRouter(p.h).Route(context.Background(), "POST", "/api/accounts/discover-org",
		scopedRequest(discoverOrgBody(scopedOutAccount)))

	require.NoError(t, err)
	dr, ok := res.(DiscoverOrgResult)
	require.True(t, ok, "result type = %T", res)
	assert.Equal(t, 1, dr.Created)
}

func TestRouterDispatch_DiscoverOrg_MissingRootSameErrorForScoped(t *testing.T) {
	p := newDiscoverOrgProbe(t, scopedOutAccount, scopedInAccount)
	missing := "33333333-3333-4333-8333-333333333333"

	_, errMissing := NewRouter(p.h).Route(context.Background(), "POST", "/api/accounts/discover-org",
		scopedRequest(discoverOrgBody(missing)))
	_, errOut := NewRouter(p.h).Route(context.Background(), "POST", "/api/accounts/discover-org",
		scopedRequest(discoverOrgBody(scopedOutAccount)))

	require.Error(t, errMissing)
	require.Error(t, errOut)
	assert.ErrorIs(t, errMissing, errNotFound)
	assert.ErrorIs(t, errOut, errNotFound)
	assert.Equal(t, errMissing, errOut, "missing and out-of-scope roots must be indistinguishable")
}
