//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Migration 000175 drops users.salt after copying every (id, salt) into a
// backup table; the down migration restores the column from that backup.
func TestMigration_0175_DropUsersSalt_RollbackRestoresSalt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	migrationsPath := getMigrationsPath()

	container := testhelpers.RequireEmptyDatabase(ctx, t)
	pool := container.DB.Pool()

	columnExists := func() bool {
		var exists bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.columns WHERE table_name = 'users' AND column_name = 'salt')`).Scan(&exists))
		return exists
	}
	backupExists := func() bool {
		var exists bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.tables WHERE table_name = 'users_salt_backup_000175')`).Scan(&exists))
		return exists
	}
	pairs := func(query string) map[string]string {
		rows, err := pool.Query(ctx, query)
		require.NoError(t, err)
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var id, salt string
			require.NoError(t, rows.Scan(&id, &salt))
			got[id] = salt
		}
		require.NoError(t, rows.Err())
		return got
	}

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, 175)))
	require.True(t, columnExists(), "salt column present before 000175")
	assert.False(t, backupExists())

	want := map[string]string{}
	for i, salt := range []string{"salt-one", "s4lt/two+==", "x"} {
		var id string
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO users (email, password_hash, salt, group_ids)
			VALUES ($1, 'hash', $2, ARRAY[gen_random_uuid()]) RETURNING id::text`,
			"salt-rollback-"+string(rune('a'+i))+"@example.com", salt).Scan(&id))
		want[id] = salt
	}
	// The bootstrap admin row (empty salt) is also in the table and must round-trip.
	for id, salt := range pairs(`SELECT id::text, salt FROM users WHERE salt = ''`) {
		want[id] = salt
	}

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 175))
	assert.False(t, columnExists(), "salt column dropped by 000175")
	require.True(t, backupExists(), "backup table created by 000175")
	assert.Equal(t, want, pairs(`SELECT id::text, salt FROM users_salt_backup_000175`),
		"backup holds exactly the pre-drop (id, salt) pairs")
	var users int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&users))
	assert.Equal(t, len(want), users, "no user row lost")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, 175)))
	assert.True(t, columnExists(), "salt column restored by the down migration")
	assert.False(t, backupExists(), "backup dropped by the down migration")
	assert.Equal(t, want, pairs(`SELECT id::text, salt FROM users`), "every salt restored")
}
