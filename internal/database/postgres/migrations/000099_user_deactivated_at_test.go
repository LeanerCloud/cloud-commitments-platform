//go:build integration
// +build integration

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

// seedDeactivatedAtUser inserts a users row with the given active/
// last_login_at combination and returns its id. The test only checks
// whether deactivated_at ends up NULL or non-NULL, not its exact value, so
// updated_at's DEFAULT NOW() (INSERT does not run the UPDATE trigger) is
// fine as-is.
func seedDeactivatedAtUser(ctx context.Context, t *testing.T, pool *pgxpool.Pool, email string, active bool, lastLoginAt *time.Time) string {
	t.Helper()
	var userID string
	err := pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, active, last_login_at)
		VALUES ($1, 'hash', $2, $3)
		RETURNING id
	`, email, active, lastLoginAt).Scan(&userID)
	require.NoError(t, err)
	return userID
}

// deactivatedAtOf reads back one user's deactivated_at.
func deactivatedAtOf(ctx context.Context, t *testing.T, pool *pgxpool.Pool, userID string) *time.Time {
	t.Helper()
	var deactivatedAt *time.Time
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT deactivated_at FROM users WHERE id = $1`, userID).Scan(&deactivatedAt))
	return deactivatedAt
}

// TestMigration_UserDeactivatedAtBackfill locks down migration 000099's
// backfill: a row that is already active = false when the migration runs
// must be classified as admin-deactivated (deactivated_at set) if it has
// ever logged in, and left as invited (deactivated_at NULL) otherwise.
// Regression coverage for the A03-006 gap on pre-existing data: before the
// backfill, every already-inactive row landed at deactivated_at = NULL and
// could self-reactivate via password reset regardless of how it went
// inactive.
func TestMigration_UserDeactivatedAtBackfill(t *testing.T) {
	ctx := context.Background()
	migrationsPath := getMigrationsPath()

	container, err := testhelpers.SetupPostgresContainer(ctx, t)
	require.NoError(t, err)
	defer container.Cleanup(ctx)
	pool := container.DB.Pool()

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, 99)))

	lastLogin := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)

	// Inactive with a login history: was active at some point, is not now --
	// the out-of-band-deactivated shape the backfill exists to mark.
	deactivatedNoAPI := seedDeactivatedAtUser(ctx, t, pool, "deactivated-no-api@example.com", false, &lastLogin)

	// Inactive with NO login history: cannot have been anything but invited
	// (Login requires Active = true), so it must NOT be backfilled.
	invitedNeverActivated := seedDeactivatedAtUser(ctx, t, pool, "invited@example.com", false, nil)

	// Active user: deactivated_at must stay NULL regardless of login history.
	activeUser := seedDeactivatedAtUser(ctx, t, pool, "active@example.com", true, &lastLogin)

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 99))

	deactivatedAt := deactivatedAtOf(ctx, t, pool, deactivatedNoAPI)
	require.NotNil(t, deactivatedAt,
		"an inactive row with login history must be backfilled as admin-deactivated, not left classifiable as invited")

	assert.Nil(t, deactivatedAtOf(ctx, t, pool, invitedNeverActivated),
		"an inactive row that has never logged in is genuinely invited and must not be backfilled")

	assert.Nil(t, deactivatedAtOf(ctx, t, pool, activeUser),
		"an active row must never get a deactivated_at, whatever its login history")

	// The down migration drops the column outright; re-running up must
	// restore the backfilled classification, not just the column shape.
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, 99)))
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 99))
	assert.NotNil(t, deactivatedAtOf(ctx, t, pool, deactivatedNoAPI),
		"re-running the migration after a rollback must re-backfill the same rows")
}
