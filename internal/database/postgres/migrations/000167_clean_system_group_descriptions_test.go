//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/require"
)

func TestMigration_CleanSystemGroupDescriptions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	container := testhelpers.RequireEmptyDatabase(ctx, t)
	pool, path := container.DB.Pool(), getMigrationsPath()

	const (
		purchaserID  = "00000000-0000-5000-8000-000000000007"
		exchangerID  = "00000000-0000-5000-8000-000000000008"
		purchaserOld = "Execute, approve, and retry purchases. Membership is required even for admins to spend money (separation of duties, issue #923)."
		purchaserNew = "Execute, approve, and retry purchases. Membership is required even for admins to spend money (separation of duties)."
		exchangerOld = "Execute RI exchanges. Membership is required even for admins, because an exchange consumes existing commitments and cannot be rolled back (separation of duties, issues #923 and #1644)."
		exchangerNew = "Execute RI exchanges. Membership is required even for admins, because an exchange consumes existing commitments and cannot be rolled back (separation of duties)."
	)

	desc := func(id string) string {
		var v string
		require.NoError(t, pool.QueryRow(ctx, `SELECT description FROM groups WHERE id=$1`, id).Scan(&v))
		return v
	}

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, 166))
	require.Equal(t, purchaserOld, desc(purchaserID))
	require.Equal(t, exchangerOld, desc(exchangerID))

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, 167))
	require.Equal(t, purchaserNew, desc(purchaserID))
	require.Equal(t, exchangerNew, desc(exchangerID))

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, 166))
	require.Equal(t, purchaserOld, desc(purchaserID))
	require.Equal(t, exchangerOld, desc(exchangerID))

	// A customized description is left alone in both directions while the
	// other row, still on its seeded text, is rewritten. Each row's guard is
	// proven independently.
	rows := []struct{ id, oldText, otherID, otherOld, otherNew string }{
		{purchaserID, purchaserOld, exchangerID, exchangerOld, exchangerNew},
		{exchangerID, exchangerOld, purchaserID, purchaserOld, purchaserNew},
	}
	for _, r := range rows {
		_, err := pool.Exec(ctx, `UPDATE groups SET description='Custom text' WHERE id=$1`, r.id)
		require.NoError(t, err)

		require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, 167))
		require.Equal(t, "Custom text", desc(r.id), "up must keep a customized %s", r.id)
		require.Equal(t, r.otherNew, desc(r.otherID))

		require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, 166))
		require.Equal(t, "Custom text", desc(r.id), "down must keep a customized %s", r.id)
		require.Equal(t, r.otherOld, desc(r.otherID))

		_, err = pool.Exec(ctx, `UPDATE groups SET description=$2 WHERE id=$1`, r.id, r.oldText)
		require.NoError(t, err)
	}
}
