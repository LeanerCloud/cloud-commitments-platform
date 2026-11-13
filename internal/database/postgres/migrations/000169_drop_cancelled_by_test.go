//go:build integration

package migrations_test

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	drop169      = 169
	legacyCol169 = "cancel" + "led_by"
)

func seed169(ctx context.Context, t *testing.T, pool *pgxpool.Pool, canceledBy, legacyBy *string, updatedAt time.Time) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO purchase_executions (status, scheduled_date, canceled_by, `+legacyCol169+`, updated_at)
		VALUES ('canceled', NOW(), $1, $2, $3) RETURNING execution_id::text`,
		canceledBy, legacyBy, updatedAt).Scan(&id))
	return id
}

func actorAndStamp169(ctx context.Context, t *testing.T, pool *pgxpool.Pool, col, id string) (actor *string, updatedAt time.Time) {
	t.Helper()
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT `+col+`, updated_at FROM purchase_executions WHERE execution_id = $1`, id).Scan(&actor, &updatedAt))
	return actor, updatedAt
}

func columnExists169(ctx context.Context, t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'purchase_executions' AND column_name = $1`, legacyCol169).Scan(&n))
	return n == 1
}

// TestMigration_000169_DropCancelledBy locks down issue #1747: the up drains
// every legacy-only actor into canceled_by without touching updated_at (the
// plan-health and retention clock), keeps an actor that canceled_by already
// holds, and drops the legacy column; the down re-adds it backfilled from
// canceled_by and the up re-applies cleanly.
func TestMigration_000169_DropCancelledBy(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RequirePostgresContainer(ctx, t)
	t.Cleanup(func() { require.NoError(t, container.Cleanup(context.Background())) })
	pool, path := container.DB.Pool(), getMigrationsPath()
	prev := previousMigrationVersion(t, drop169)

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, prev))
	require.True(t, columnExists169(ctx, t, pool))

	old := time.Now().UTC().AddDate(0, 0, -90).Truncate(time.Microsecond)
	legacyOnly := seed169(ctx, t, pool, nil, s157("legacy@example.com"), old)
	both := seed169(ctx, t, pool, s157("new@example.com"), s157("stale@example.com"), old)
	newOnly := seed169(ctx, t, pool, s157("only-new@example.com"), nil, old)
	neither := seed169(ctx, t, pool, nil, nil, old)

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, drop169))

	assert.False(t, columnExists169(ctx, t, pool), "up must drop the legacy column")
	want := map[string]*string{
		legacyOnly: s157("legacy@example.com"),
		both:       s157("new@example.com"),
		newOnly:    s157("only-new@example.com"),
		neither:    nil,
	}
	for id, wantActor := range want {
		actor, stamp := actorAndStamp169(ctx, t, pool, "canceled_by", id)
		assert.Equal(t, wantActor, actor, "canceled_by after the drain")
		assert.True(t, old.Equal(stamp), "updated_at must survive the drain")
	}
	var enabled int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_trigger
		WHERE tgname = 'update_purchase_executions_updated_at' AND tgenabled = 'O'`).Scan(&enabled))
	assert.Equal(t, 1, enabled, "the updated_at trigger must be enabled again")

	// Down: the column returns, backfilled from canceled_by, updated_at intact.
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, prev))
	require.True(t, columnExists169(ctx, t, pool))
	for id, wantActor := range want {
		actor, stamp := actorAndStamp169(ctx, t, pool, legacyCol169, id)
		assert.Equal(t, wantActor, actor, "down must backfill the legacy column")
		assert.True(t, old.Equal(stamp), "updated_at must survive the down backfill")
	}

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, drop169), "up must re-apply cleanly after down")
}
