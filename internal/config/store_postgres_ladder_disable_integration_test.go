//go:build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type ladderConfigSnapshot struct {
	enabled   bool
	mode      string
	cadence   string
	updatedAt time.Time
}

func readLadderConfigSnapshot(ctx context.Context, t *testing.T, store *PostgresStore, id string) ladderConfigSnapshot {
	t.Helper()
	var s ladderConfigSnapshot
	require.NoError(t, store.db.QueryRow(ctx, `SELECT enabled, mode, cadence, updated_at FROM ladder_configs WHERE id = $1`, id).
		Scan(&s.enabled, &s.mode, &s.cadence, &s.updatedAt))
	return s
}

func TestDisableLadderConfigTx(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	cfgID := seedLadderConfigWithExtID(ctx, t, store, "732222222222")

	t.Run("flips an enabled config and leaves the rest alone", func(t *testing.T) {
		before := readLadderConfigSnapshot(ctx, t, store, cfgID)
		require.True(t, before.enabled)
		var flipped bool
		require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
			var err error
			flipped, err = store.DisableLadderConfigTx(ctx, tx, cfgID)
			return err
		}))
		assert.True(t, flipped)
		after := readLadderConfigSnapshot(ctx, t, store, cfgID)
		assert.False(t, after.enabled)
		assert.Equal(t, before.mode, after.mode)
		assert.Equal(t, before.cadence, after.cadence)
		assert.True(t, after.updatedAt.After(before.updatedAt), "updated_at must advance")
	})

	t.Run("a second call is a no-op", func(t *testing.T) {
		before := readLadderConfigSnapshot(ctx, t, store, cfgID)
		var flipped bool
		require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
			var err error
			flipped, err = store.DisableLadderConfigTx(ctx, tx, cfgID)
			return err
		}))
		assert.False(t, flipped)
		assert.Equal(t, before, readLadderConfigSnapshot(ctx, t, store, cfgID))
	})

	t.Run("an unknown id is not an error", func(t *testing.T) {
		var flipped bool
		require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
			var err error
			flipped, err = store.DisableLadderConfigTx(ctx, tx, uuid.NewString())
			return err
		}))
		assert.False(t, flipped)
	})

	t.Run("an empty id is an error", func(t *testing.T) {
		err := store.WithTx(ctx, func(tx pgx.Tx) error {
			_, disableErr := store.DisableLadderConfigTx(ctx, tx, "")
			return disableErr
		})
		require.Error(t, err)
	})
}

func TestDisableLadderConfigTx_AfterTrancheLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	cfgID := seedLadderConfigWithExtID(ctx, t, store, "733333333333")
	tr := lifecycleTranche(cfgID, 1, ladder.TrancheStatusFired, time.Now().UTC())
	require.NoError(t, store.SaveLadderTranches(ctx, []LadderTrancheDB{tr}))

	var flipped bool
	require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
		var lockedID string
		if err := tx.QueryRow(ctx, `SELECT id FROM ladder_tranches WHERE id = $1 FOR UPDATE`, tr.ID).Scan(&lockedID); err != nil {
			return err
		}
		var err error
		flipped, err = store.DisableLadderConfigTx(ctx, tx, cfgID)
		return err
	}))
	assert.True(t, flipped)
	assert.False(t, readLadderConfigSnapshot(ctx, t, store, cfgID).enabled)
}

func TestDisableLadderConfigTx_BusyRowReturnsAtOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	cfgID := seedLadderConfigWithExtID(ctx, t, store, "734444444444")

	holder, err := store.db.Begin(ctx)
	require.NoError(t, err)
	defer func() { assert.NoError(t, holder.Rollback(ctx)) }()
	var held bool
	require.NoError(t, holder.QueryRow(ctx, `SELECT enabled FROM ladder_configs WHERE id = $1 FOR NO KEY UPDATE`, cfgID).Scan(&held))

	start := time.Now()
	disableErr := store.WithTx(ctx, func(tx pgx.Tx) error {
		_, callErr := store.DisableLadderConfigTx(ctx, tx, cfgID)
		return callErr
	})
	require.ErrorIs(t, disableErr, ErrLadderConfigBusy)
	assert.Less(t, time.Since(start), time.Second, "a busy config row must not make the caller wait")
	assert.True(t, readLadderConfigSnapshot(ctx, t, store, cfgID).enabled)
}

func TestGetGlobalConfigTx(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)

	pooled, err := store.GetGlobalConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, pooled)
	require.False(t, pooled.LadderingEnabled)

	require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
		inTx, txErr := store.GetGlobalConfigTx(ctx, tx)
		require.NoError(t, txErr)
		assert.Equal(t, pooled.LadderingEnabled, inTx.LadderingEnabled)
		assert.Equal(t, pooled.LadderExecutionEnabled, inTx.LadderExecutionEnabled)
		assert.Equal(t, pooled.OfferingClass, inTx.OfferingClass)
		assert.Equal(t, pooled.RequireDifferentApprover, inTx.RequireDifferentApprover)

		_, updErr := store.UpdateGlobalConfigAtomic(ctx, func(g *GlobalConfig) error {
			g.LadderingEnabled = true
			return nil
		})
		require.NoError(t, updErr)

		again, txErr := store.GetGlobalConfigTx(ctx, tx)
		require.NoError(t, txErr)
		assert.True(t, again.LadderingEnabled, "a statement in a READ COMMITTED tx sees a later commit")
		return nil
	}))
}
