//go:build integration
// +build integration

package testhelpers

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewIsolatedSchema_MigratesAndIsolatesRows(t *testing.T) {
	ctx := context.Background()
	c := RequireEmptyDatabase(ctx, t)

	var schemaA string
	t.Run("rows stay in their schema", func(t *testing.T) {
		a := NewIsolatedSchema(ctx, t, c)
		b := NewIsolatedSchema(ctx, t, c)

		require.NoError(t, migrations.RunMigrations(ctx, a, sharedTestMigrationsPath(), "", ""))
		require.NoError(t, migrations.RunMigrations(ctx, b, sharedTestMigrationsPath(), "", ""))

		require.NoError(t, a.QueryRow(ctx, "SELECT current_schema()").Scan(&schemaA))
		var schemaB string
		require.NoError(t, b.QueryRow(ctx, "SELECT current_schema()").Scan(&schemaB))
		require.NotEqual(t, schemaA, schemaB)

		// Migrated tables exist in both schemas, so insert into a scratch
		// table created in each to avoid the users constraints.
		for _, p := range []*pgxpool.Pool{a, b} {
			_, err := p.Exec(ctx, "CREATE TABLE scratch (name text)")
			require.NoError(t, err)
		}
		_, err := a.Exec(ctx, "INSERT INTO scratch VALUES ('a')")
		require.NoError(t, err)

		var inA, inB int
		require.NoError(t, a.QueryRow(ctx, "SELECT count(*) FROM scratch").Scan(&inA))
		require.NoError(t, b.QueryRow(ctx, "SELECT count(*) FROM scratch").Scan(&inB))
		assert.Equal(t, 1, inA)
		assert.Equal(t, 0, inB)
	})

	// The subtest's Cleanup has run: the schema must be gone.
	var n int
	require.NoError(t, c.DB.QueryRow(ctx,
		"SELECT count(*) FROM information_schema.schemata WHERE schema_name = $1", schemaA).Scan(&n))
	assert.Equal(t, 0, n, "schema %s should be dropped by Cleanup", schemaA)
}

func TestIsolatedSchemaName(t *testing.T) {
	name := isolatedSchemaName("TestFoo/Sub test-1 ÄÖ")
	assert.Regexp(t, `^t_testfoo_sub_test_1_\d+_\d+$`, name)
	assert.LessOrEqual(t, len(isolatedSchemaName(string(make([]byte, 200)))), 63)
	assert.NotEqual(t, isolatedSchemaName("x"), isolatedSchemaName("x"))
}
