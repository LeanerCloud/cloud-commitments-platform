package commitmentopts

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func azureComboRows() []Combo {
	return []Combo{
		{Provider: "azure", Service: azureSpService, TermYears: 1, Payment: "upfront"},
		{Provider: "azure", Service: azureSpService, TermYears: 3, Payment: "monthly"},
	}
}

// cachedOptsService builds a Service whose account lister, config builder and
// probers fail the test if CachedOptions reaches any cloud-facing dependency.
func cachedOptsService(t *testing.T, store Store) *Service {
	t.Helper()
	var touched int32
	t.Cleanup(func() { assert.Zero(t, atomic.LoadInt32(&touched), "CachedOptions must not do cloud I/O") })
	accounts := &fakeAccounts{err: errors.New("accounts must not be listed")}
	build := func(context.Context, *config.CloudAccount) (aws.Config, error) {
		atomic.AddInt32(&touched, 1)
		return aws.Config{}, nil
	}
	probers := []Prober{proberFunc{name: "rds", fn: func() ([]Combo, error) {
		atomic.AddInt32(&touched, 1)
		return nil, nil
	}}}
	return New(store, accounts, build, probers)
}

func TestService_CachedOptions_AzureRowOnlyNoAWSAccount(t *testing.T) {
	store := newProviderStore()
	store.rows["azure"] = azureComboRows()
	svc := cachedOptsService(t, store)

	got, ok, err := svc.CachedOptions(context.Background(), "azure")

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, azureComboRows(), got[azureSpService])
	// Get still reports AWS as unavailable: Azure must not warm it.
	_, getErr := New(store, &fakeAccounts{}, noopBuildConfig, nil).Get(context.Background())
	require.ErrorIs(t, getErr, ErrNoData)
}

func TestService_CachedOptions_FiltersToRequestedProvider(t *testing.T) {
	store := newProviderStore()
	store.rows["azure"] = azureComboRows()
	store.rows["aws"] = []Combo{{Provider: "aws", Service: "rds", TermYears: 1, Payment: "all-upfront"}}
	svc := cachedOptsService(t, store)

	az, ok, err := svc.CachedOptions(context.Background(), "azure")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Len(t, az, 1)
	assert.Contains(t, az, azureSpService)

	aw, ok, err := svc.CachedOptions(context.Background(), "aws")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Len(t, aw, 1)
	assert.Contains(t, aw, "rds")
}

func TestService_CachedOptions_NoRowOrUnknownProvider(t *testing.T) {
	store := newProviderStore()
	store.rows["aws"] = []Combo{{Provider: "aws", Service: "rds", TermYears: 1, Payment: "all-upfront"}}
	svc := cachedOptsService(t, store)

	for _, provider := range []string{"azure", "gcp", ""} {
		got, ok, err := svc.CachedOptions(context.Background(), provider)
		require.NoError(t, err, provider)
		assert.False(t, ok, provider)
		assert.Empty(t, got, provider)
	}
}

func TestService_CachedOptions_StoreErrorsBubble(t *testing.T) {
	boom := errors.New("store boom")

	_, ok, err := cachedOptsService(t, &fakeStore{hasErr: boom}).CachedOptions(context.Background(), "azure")
	require.ErrorIs(t, err, boom)
	assert.False(t, ok)

	_, ok, err = cachedOptsService(t, &fakeStore{getErr: boom, has: true}).CachedOptions(context.Background(), "azure")
	require.ErrorIs(t, err, boom)
	assert.False(t, ok)
}
