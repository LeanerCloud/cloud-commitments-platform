//go:build integration

package migrations_test

// Issue #313: the bootstrap admin INSERTs run after every migration on each
// boot, so they must not name users.salt, a column migration 000175 dropped. This reruns RunMigrations (the migrations are already
// applied, so only the bootstrap runs) on both bootstrap paths.

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureAdminUser_WithoutSaltColumn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	migrationsPath := getMigrationsPath()

	for _, tc := range []struct{ name, email, password string }{
		{"no password", "no-salt-invite@test.example", ""},
		{"with password", "no-salt-admin@test.example", "TestPass!9aF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pool := testhelpers.RequireMigratedDatabase(ctx, t, migrationsPath).DB.Pool()
			require.NoError(t, migrations.RunMigrations(ctx, pool, migrationsPath, tc.email, tc.password))

			assert.Equal(t, []string{defaultAdminGroupIDTest}, queryAdminGroupIDs(t, ctx, pool, tc.email))
			var active bool
			require.NoError(t, pool.QueryRow(ctx, "SELECT active FROM users WHERE email = $1", tc.email).Scan(&active))
			assert.Equal(t, tc.password != "", active)
		})
	}
}
