//go:build integration
// +build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A fresh migrate leaves experimental_gcp_plan_term OFF (migration 000173);
// the setting round-trips through the whole-config upsert and survives an
// unrelated read-modify-write (platform issue #858).
func TestPostgresStoreDB_ExperimentalGCPPlanTerm_DefaultOffAndRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool := testhelpers.RequireMigratedDatabase(ctx, t, getTestMigrationsPath()).DB
	store := &PostgresStore{db: pool}

	cfg, err := store.GetGlobalConfig(ctx)
	require.NoError(t, err)
	assert.False(t, cfg.ExperimentalGCPPlanTerm, "a fresh migrate leaves the switch OFF")

	_, err = store.UpdateGlobalConfigAtomic(ctx, func(existing *GlobalConfig) error {
		existing.ExperimentalGCPPlanTerm = true
		return nil
	})
	require.NoError(t, err)

	_, err = store.UpdateGlobalConfigAtomic(ctx, func(existing *GlobalConfig) error {
		existing.RIExchangeEnabled = true
		return nil
	})
	require.NoError(t, err)

	cfg, err = store.GetGlobalConfig(ctx)
	require.NoError(t, err)
	assert.True(t, cfg.ExperimentalGCPPlanTerm)
	assert.True(t, cfg.RIExchangeEnabled)
}
