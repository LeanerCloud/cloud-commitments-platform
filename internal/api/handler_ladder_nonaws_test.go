package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func ladderBodyFor(provider string) string {
	return fmt.Sprintf(`{"cloud_account_id":"%s","provider":"%s","enabled":false,"mode":"email_approval","cadence":"daily","max_hourly_commit_per_run":5,%s}`,
		lgAcct, provider, ladderValidRamp)
}

// The engine only plans and fires for AWS, so a new non-AWS config could never
// act; an existing one must stay editable so operators can disable it.
func TestUpsertLadderConfig_NonAWSCreateRefused(t *testing.T) {
	t.Parallel()
	callers := map[string]apgCaller{"user": {perms: apgAdminOnly}, "api key": {key: true}}
	for name, caller := range callers {
		for _, provider := range []string{"azure", "gcp"} {
			t.Run(name+"/create "+provider, func(t *testing.T) {
				store, err := lgCallFor(t, provider, caller, nil, ladderBodyFor(provider))
				require.Error(t, err)
				ce, ok := IsClientError(err)
				require.True(t, ok, "%v", err)
				assert.Equal(t, 400, ce.code)
				assert.Contains(t, ce.message, "the ladder supports AWS accounts only")
				assert.False(t, apgCalled(store, "UpsertLadderConfig"))
			})
			t.Run(name+"/update existing "+provider, func(t *testing.T) {
				stored := &config.LadderConfigDB{ID: "cfg-1", CloudAccountID: lgAcct, Provider: provider, Enabled: true, Mode: "email_approval"}
				store, err := lgCallFor(t, provider, caller, stored, ladderBodyFor(provider))
				require.NoError(t, err)
				assert.True(t, apgCalled(store, "UpsertLadderConfig"))
			})
		}
		t.Run(name+"/create aws", func(t *testing.T) {
			store, err := lgCallFor(t, "aws", caller, nil, ladderBodyFor("aws"))
			require.NoError(t, err)
			assert.True(t, apgCalled(store, "UpsertLadderConfig"))
		})
	}
}

// Moving an existing AWS row to azure is a different (account, provider) key,
// so it is a create of a non-AWS row and is refused (here by the account
// provider check, and by the create check for an azure account).
func TestUpsertLadderConfig_AWSRowToAzureRefused(t *testing.T) {
	t.Parallel()
	store, err := lgCallFor(t, "aws", apgCaller{perms: apgAdminOnly}, lgStored(false, "email_approval", nil), ladderBodyFor("azure"))
	require.Error(t, err)
	ce, ok := IsClientError(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, 400, ce.code)
	assert.False(t, apgCalled(store, "UpsertLadderConfig"))
}

// A failed stored-row lookup must not be read as "no row" (create refusal) or
// "row exists" (allow): it fails loudly as a non-client error.
func TestUpsertLadderConfig_NonAWSLookupErrorFailsClosed(t *testing.T) {
	t.Parallel()
	store := new(MockConfigStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	store.On("GetLadderConfig", mock.Anything, lgAcct, "azure").Return((*config.LadderConfigDB)(nil), fmt.Errorf("db down"))
	store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		return &config.CloudAccount{ID: id, Provider: "azure"}, nil
	}
	h, req := apgCaller{perms: apgAdminOnly}.handlerAndReq(store, ladderBodyFor("azure"))
	_, err := h.upsertLadderConfig(context.Background(), req)
	require.Error(t, err)
	_, isClient := IsClientError(err)
	assert.False(t, isClient, "a lookup failure must be a 500, not a 400: %v", err)
	assert.False(t, apgCalled(store, "UpsertLadderConfig"))
}
