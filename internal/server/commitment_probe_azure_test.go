package server

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAzureProbeCurrency_IsUSD(t *testing.T) {
	got, err := azureProbeCurrency(context.Background(), &config.CloudAccount{Provider: "azure"})
	require.NoError(t, err)
	assert.Equal(t, "USD", got)
}

func TestNewCommitmentProbeAzureCredential(t *testing.T) {
	build := newCommitmentProbeAzureCredential(nil, nil, "")

	t.Run("managed identity needs no stored secret", func(t *testing.T) {
		cred, err := build(context.Background(), &config.CloudAccount{ID: "az1", Provider: "azure", AzureAuthMode: "managed_identity"})
		require.NoError(t, err)
		assert.NotNil(t, cred)
	})

	t.Run("client secret without a store fails loud", func(t *testing.T) {
		cred, err := build(context.Background(), &config.CloudAccount{ID: "az1", Provider: "azure", AzureAuthMode: "client_secret"})
		require.Error(t, err)
		assert.Nil(t, cred)
		assert.Contains(t, err.Error(), "credential store required")
	})

	t.Run("workload identity federation without a signer fails loud", func(t *testing.T) {
		cred, err := build(context.Background(), &config.CloudAccount{ID: "az1", Provider: "azure", AzureAuthMode: "workload_identity_federation"})
		require.Error(t, err)
		assert.Nil(t, cred)
	})
}
