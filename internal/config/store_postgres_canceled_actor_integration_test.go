//go:build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPostgresStore_CanceledActorReadsCanceledByAlone is the issue #1747
// regression: on the schema without the legacy cancel-spelled actor column,
// every execution reader must project canceled_by, and a save must never erase
// an actor already stored. Against the COALESCE(canceled_by, <dropped column>)
// projections this fails with an undefined-column error.
func TestPostgresStore_CanceledActorReadsCanceledByAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db := setupTestContainerDB(t)
	pool := db.Pool()
	store := NewPostgresStore(db)

	acct := &CloudAccount{Name: "Canceled Actor", Provider: "aws", ExternalID: "ext-canceled-actor", Enabled: true}
	require.NoError(t, store.CreateCloudAccount(ctx, acct))
	plan := &PurchasePlan{Name: "Canceled Actor Plan", Services: map[string]ServiceConfig{}}
	require.NoError(t, store.CreatePurchasePlan(ctx, plan))

	var id string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO purchase_executions (plan_id, status, step_number, scheduled_date, cloud_account_id, canceled_by)
		VALUES ($1, $2, 1, NOW(), $3, 'actor@example.com') RETURNING execution_id::text`,
		plan.ID, StatusCanceled, acct.ID).Scan(&id))

	var legacyCols int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'purchase_executions' AND column_name = 'cancel'||'led_by'`).Scan(&legacyCols))
	require.Zero(t, legacyCols, "the legacy actor column must be gone")

	t.Run("get", func(t *testing.T) {
		got, err := store.GetExecutionByID(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, got.CancelledBy)
		assert.Equal(t, "actor@example.com", *got.CancelledBy)
	})

	t.Run("history page", func(t *testing.T) {
		page, err := store.ListHistoryExecutionsPage(ctx, HistoryExecutionsPageQuery{Statuses: []string{StatusCanceled}, Limit: 10})
		require.NoError(t, err)
		require.Len(t, page, 1)
		require.NotNil(t, page[0].CancelledBy)
		assert.Equal(t, "actor@example.com", *page[0].CancelledBy)
	})

	t.Run("a stale save never erases or replaces the stored actor", func(t *testing.T) {
		got, err := store.GetExecutionByID(ctx, id)
		require.NoError(t, err)
		stale := *got
		stale.CancelledBy = nil
		require.NoError(t, store.SavePurchaseExecution(ctx, &stale))
		other := *got
		otherActor := "stale-copy@example.com"
		other.CancelledBy = &otherActor
		require.NoError(t, store.SavePurchaseExecution(ctx, &other))

		reread, err := store.GetExecutionByID(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, reread.CancelledBy)
		assert.Equal(t, "actor@example.com", *reread.CancelledBy)
	})
}
