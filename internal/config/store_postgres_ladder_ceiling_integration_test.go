//go:build integration

package config

// Real PostgreSQL tests for the per-sweep commitment ceiling store methods
// (issue #70, slice M1). Nothing here uses mocks.

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ceilingPtr(v float64) *float64 { return &v }

func TestSweepCeiling_GetSetRoundTripsExactMicros(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	cfgID := seedLadderConfigWithExtID(ctx, t, store, "70"+uuid.NewString()[:10])

	got, err := store.GetLadderSweepCeiling(ctx, cfgID)
	require.NoError(t, err)
	assert.Nil(t, got, "a fresh config has no ceiling")

	for in, want := range map[float64]int64{12.5: 12_500_000, 0.000001: 1, 123.456789: 123_456_789, 1e12: 1_000_000_000_000_000_000} {
		require.NoError(t, store.SetLadderSweepCeiling(ctx, cfgID, ceilingPtr(in)), in)
		got, err = store.GetLadderSweepCeiling(ctx, cfgID)
		require.NoError(t, err, in)
		require.NotNil(t, got, in)
		assert.Equal(t, want, *got, in)
	}

	require.NoError(t, store.SetLadderSweepCeiling(ctx, cfgID, nil))
	got, err = store.GetLadderSweepCeiling(ctx, cfgID)
	require.NoError(t, err)
	assert.Nil(t, got, "nil clears the ceiling")
}

func TestSweepCeiling_InvalidValuesAreRejectedAndLeaveTheRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	cfgID := seedLadderConfigWithExtID(ctx, t, store, "70"+uuid.NewString()[:10])
	require.NoError(t, store.SetLadderSweepCeiling(ctx, cfgID, ceilingPtr(3)))

	for _, bad := range []float64{0, -1, math.NaN(), math.Inf(1), 1e13, 1.1234567} {
		err := store.SetLadderSweepCeiling(ctx, cfgID, ceilingPtr(bad))
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "max_hourly_commit_per_sweep", bad)
		got, getErr := store.GetLadderSweepCeiling(ctx, cfgID)
		require.NoError(t, getErr)
		require.NotNil(t, got, bad)
		assert.Equal(t, int64(3_000_000), *got, "row unchanged after %v", bad)
	}
}

func TestSweepCeiling_UnknownConfigIsAnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	missing := uuid.NewString()

	_, err := store.GetLadderSweepCeiling(ctx, missing)
	require.ErrorContains(t, err, "not found")
	require.ErrorContains(t, store.SetLadderSweepCeiling(ctx, missing, ceilingPtr(1)), "not found")
	require.ErrorContains(t, store.SetLadderSweepCeiling(ctx, missing, nil), "not found")
}

// The whole-config PUT path must never overwrite the ceiling.
func TestSweepCeiling_UpsertLeavesTheCeilingUntouched(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	cfgID := seedLadderConfigWithExtID(ctx, t, store, "70"+uuid.NewString()[:10])
	require.NoError(t, store.SetLadderSweepCeiling(ctx, cfgID, ceilingPtr(7.25)))

	cfgs, err := store.GetLadderConfigs(ctx)
	require.NoError(t, err)
	require.Len(t, cfgs, 1)
	cfgs[0].BufferFraction = 0.2
	_, err = store.UpsertLadderConfig(ctx, &cfgs[0])
	require.NoError(t, err)

	got, err := store.GetLadderSweepCeiling(ctx, cfgID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(7_250_000), *got)
}

// Before the migration both methods answer ErrLadderCeilingUnavailable and the
// existing config queries keep working without the column.
func TestSweepCeiling_BeforeTheMigration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	cfgID := seedLadderConfigWithExtID(ctx, t, store, "70"+uuid.NewString()[:10])
	conn, ok := store.db.(*database.Connection)
	require.True(t, ok)
	require.NoError(t, migrations.MigrateToVersion(ctx, conn.Pool(), getTestMigrationsPath(), migrationBelow(t, sweepCeilingMigrationVersion)))

	_, err := store.GetLadderSweepCeiling(ctx, cfgID)
	require.ErrorIs(t, err, ErrLadderCeilingUnavailable)
	require.ErrorIs(t, store.SetLadderSweepCeiling(ctx, cfgID, ceilingPtr(1)), ErrLadderCeilingUnavailable)
	require.ErrorIs(t, store.SetLadderSweepCeiling(ctx, cfgID, nil), ErrLadderCeilingUnavailable)

	cfgs, err := store.GetLadderConfigs(ctx)
	require.NoError(t, err)
	require.Len(t, cfgs, 1)
	_, err = store.UpsertLadderConfig(ctx, &cfgs[0])
	require.NoError(t, err)
	got, err := store.GetLadderConfig(ctx, cfgs[0].CloudAccountID, cfgs[0].Provider)
	require.NoError(t, err)
	require.NotNil(t, got)
}
