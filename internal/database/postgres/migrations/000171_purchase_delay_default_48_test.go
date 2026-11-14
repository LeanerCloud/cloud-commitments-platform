//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
)

const m171Version = 171

// TestMigration_PurchaseDelayDefault48 pins 000171: the seeded row still at 0
// moves to 48, any other value is left alone, and new rows default to 48.
func TestMigration_PurchaseDelayDefault48(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		beforeUp string
		wantUp   int
	}{
		{"untouched seed at 0", "", 48},
		{"admin chose 12", `UPDATE global_config SET purchase_delay_hours = 12 WHERE id = 1`, 12},
		{"admin chose 48", `UPDATE global_config SET purchase_delay_hours = 48 WHERE id = 1`, 48},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			path := getMigrationsPath()
			pool := testhelpers.RequireEmptyDatabase(ctx, t).DB.Pool()
			prev := previousMigrationVersion(t, m171Version)
			require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, prev))

			var before int
			require.NoError(t, pool.QueryRow(ctx, `SELECT purchase_delay_hours FROM global_config WHERE id = 1`).Scan(&before))
			require.Equal(t, 0, before, "seeded row starts at 0 before the migration")
			if tc.beforeUp != "" {
				_, err := pool.Exec(ctx, tc.beforeUp)
				require.NoError(t, err)
			}

			require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, m171Version))
			var got int
			require.NoError(t, pool.QueryRow(ctx, `SELECT purchase_delay_hours FROM global_config WHERE id = 1`).Scan(&got))
			assert.Equal(t, tc.wantUp, got, "after up")
			var untouched bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT updated_at = created_at FROM global_config WHERE id = 1`).Scan(&untouched))
			assert.Equal(t, tc.beforeUp == "", untouched, "updated_at is not bumped by the migration")

			// The trigger must be enabled again after up.
			_, err := pool.Exec(ctx, `UPDATE global_config SET purchase_delay_hours = purchase_delay_hours WHERE id = 1`)
			require.NoError(t, err)
			var bumped bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT updated_at <> created_at FROM global_config WHERE id = 1`).Scan(&bumped))
			assert.True(t, bumped, "updated_at trigger re-enabled after up")

			_, err = pool.Exec(ctx, `DELETE FROM global_config; INSERT INTO global_config (id) VALUES (1)`)
			require.NoError(t, err)
			require.NoError(t, pool.QueryRow(ctx, `SELECT purchase_delay_hours FROM global_config WHERE id = 1`).Scan(&got))
			assert.Equal(t, 48, got, "new row default after up")

			require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, prev))
			_, err = pool.Exec(ctx, `DELETE FROM global_config; INSERT INTO global_config (id) VALUES (1)`)
			require.NoError(t, err)
			require.NoError(t, pool.QueryRow(ctx, `SELECT purchase_delay_hours FROM global_config WHERE id = 1`).Scan(&got))
			assert.Equal(t, 0, got, "new row default after down")
		})
	}
}
