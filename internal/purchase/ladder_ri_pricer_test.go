package purchase

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ladderPricerManager(store config.StoreInterface, cred *MockCredentialStore) *Manager {
	return &Manager{config: store, credStore: cred}
}

func TestLadderRIPricer_FailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	keys := &MockCredentialStore{LoadRawFn: func(context.Context, string, string) ([]byte, error) {
		return []byte(`{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`), nil
	}}
	tests := []struct {
		name    string
		account *config.CloudAccount
		getErr  error
		region  string
		cred    *MockCredentialStore
		want    string
	}{
		{name: "no region", region: "", account: &config.CloudAccount{ID: "a", Provider: "aws", AWSAuthMode: "access_keys"}, cred: keys, want: "region is required"},
		{name: "account lookup error", region: "us-east-1", getErr: errors.New("db down"), cred: keys, want: "db down"},
		{name: "account missing", region: "us-east-1", cred: keys, want: "not found"},
		{name: "non-AWS account", region: "us-east-1", account: &config.CloudAccount{ID: "a", Provider: "azure"}, cred: keys, want: "credentials"},
		{name: "credential read error", region: "us-east-1", account: &config.CloudAccount{ID: "a", Provider: "aws", AWSAuthMode: "access_keys"},
			cred: &MockCredentialStore{LoadRawFn: func(context.Context, string, string) ([]byte, error) { return nil, errors.New("vault down") }}, want: "vault down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := new(MockConfigStore)
			if tt.region != "" {
				store.On("GetCloudAccount", ctx, "a").Return(tt.account, tt.getErr).Maybe()
			}
			p, err := ladderPricerManager(store, tt.cred).LadderRIPricer(ctx, "a", tt.region)
			require.ErrorContains(t, err, tt.want)
			assert.Nil(t, p, "no pricer may come back with an error")
		})
	}
}

func TestLadderRIPricer_BuildsAFactoryForTheAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := new(MockConfigStore)
	store.On("GetCloudAccount", ctx, "a").Return(&config.CloudAccount{ID: "a", Provider: "aws", AWSAuthMode: "access_keys"}, nil)
	keys := &MockCredentialStore{LoadRawFn: func(context.Context, string, string) ([]byte, error) {
		return []byte(`{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`), nil
	}}
	var factory LadderRIPricerFactory = ladderPricerManager(store, keys).LadderRIPricer
	p, err := factory(ctx, "a", "us-east-1")
	require.NoError(t, err)
	assert.NotNil(t, p)
	store.AssertExpectations(t)
}
