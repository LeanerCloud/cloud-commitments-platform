//go:build integration
// +build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ages come from the database clock. The 000128 trigger overwrites updated_at
// on UPDATE, so the aged row is INSERTed with explicit timestamps.
func TestGetRedshiftRetryClock_UsesDBNow(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)

	var id string
	require.NoError(t, store.db.QueryRow(ctx, `
		INSERT INTO purchase_executions (status, step_number, scheduled_date, first_dispatch_at, updated_at)
		VALUES ('failed', 1, now(), now() - interval '3 hours', now() - interval '7 minutes')
		RETURNING execution_id::text`).Scan(&id))

	clock, err := store.GetRedshiftRetryClock(ctx, id)
	require.NoError(t, err)
	assert.InDelta(t, (7 * time.Minute).Seconds(), clock.SinceUpdate.Seconds(), 30)
	require.NotNil(t, clock.SinceStamp)
	assert.InDelta(t, (3 * time.Hour).Seconds(), clock.SinceStamp.Seconds(), 30)
}

func TestGetRedshiftRetryClock_NullStampIsNil(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)
	exec := newNoSubmissionExec(ctx, t, store, "failed")

	clock, err := store.GetRedshiftRetryClock(ctx, exec.ExecutionID)
	require.NoError(t, err)
	assert.Nil(t, clock.SinceStamp, "a NULL stamp must not read as fresh")
	assert.GreaterOrEqual(t, clock.SinceUpdate, time.Duration(0))
}

func TestGetRedshiftRetryClock_UnknownExecutionIsNotFound(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)

	_, err := store.GetRedshiftRetryClock(ctx, "00000000-0000-0000-0000-000000000000")
	assert.ErrorIs(t, err, ErrNotFound)
}
