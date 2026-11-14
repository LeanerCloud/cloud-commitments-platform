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

// DiscoverOrgRequest.account_id must reach the account lookup, select that
// root's stored credentials for the AWS call, and stamp the same root on the
// persisted members. The other discover-org tests return one fixed root
// whatever id is asked for, so a dropped or constant id would pass them.
func TestDiscoverOrgAccounts_AccountIDSelectsRootCredentialsAndBastion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	setupAdminAuth(ctx, mockAuth)

	const otherRootID = "33333333-3333-3333-3333-333333333333"
	wanted := orgRootAccount()
	other := orgRootAccount()
	other.ID = otherRootID
	other.ExternalID = "100000000009"
	roots := map[string]*config.CloudAccount{wanted.ID: wanted, other.ID: other}

	store := setupAdminMock(ctx)
	t.Cleanup(func() { store.AssertExpectations(t) })
	var looked []string
	store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		looked = append(looked, id)
		return roots[id], nil
	}
	store.ListCloudAccountsFn = func(_ context.Context, _ config.CloudAccountFilter) ([]config.CloudAccount, error) {
		return nil, nil
	}
	var created []config.CloudAccount
	store.CreateCloudAccountFn = func(_ context.Context, a *config.CloudAccount) error {
		created = append(created, *a)
		return nil
	}

	var gotKey string
	handler := &Handler{
		auth:   mockAuth,
		config: store,
		credStore: &fakeCredStore{data: map[string][]byte{
			wanted.ID + "::aws_access_keys": []byte(`{"access_key_id":"AKIAWANTED","secret_access_key":"shh"}`),
			other.ID + "::aws_access_keys":  []byte(`{"access_key_id":"AKIAOTHER","secret_access_key":"shh"}`),
		}},
		discoverOrgFn: func(ctx context.Context, cfg aws.Config) (*accounts.OrgDiscoveryResult, error) {
			creds, err := cfg.Credentials.Retrieve(ctx)
			require.NoError(t, err)
			gotKey = creds.AccessKeyID
			return &accounts.OrgDiscoveryResult{Accounts: []config.CloudAccount{
				{Provider: "aws", ExternalID: "300000000003", Name: "Member"},
			}}, nil
		},
	}

	_, err := handler.discoverOrgAccounts(ctx, adminRequest(`{"account_id":"`+otherRootID+`"}`))
	require.NoError(t, err)

	assert.Contains(t, looked, otherRootID, "account_id must reach the account lookup")
	assert.NotContains(t, looked, wanted.ID)
	assert.Equal(t, "AKIAOTHER", gotKey, "AWS Organizations must be called with the requested root's credentials")
	require.Len(t, created, 1)
	assert.Equal(t, otherRootID, created[0].AWSBastionID, "members must point at the requested root")
}
