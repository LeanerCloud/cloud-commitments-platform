//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
)

const m166Version = 166

type m166State struct {
	payment   string
	untouched bool // updated_at = created_at
}

func m166Read(ctx context.Context, t *testing.T, pool *pgxpool.Pool) m166State {
	t.Helper()
	var s m166State
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT default_payment, updated_at = created_at FROM global_config WHERE id = 1`).Scan(&s.payment, &s.untouched))
	return s
}

func m166Exec(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	_, err := pool.Exec(ctx, sql)
	require.NoError(t, err, sql)
}

// TestMigration_DefaultPaymentNoUpfront pins 000166: only the untouched seeded
// row moves (and back on down); anything an admin saved, and any other value,
// is left alone, and the trigger-driven updated_at stays unequal/equal as found.
func TestMigration_DefaultPaymentNoUpfront(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		beforeUp string
		afterUp  string
		wantUp   m166State
		wantDown m166State
	}{
		{"a untouched seed", "", "",
			m166State{"no-upfront", true}, m166State{"all-upfront", true}},
		{"b admin saved all-upfront", `UPDATE global_config SET default_payment = 'all-upfront' WHERE id = 1`, "",
			m166State{"all-upfront", false}, m166State{"all-upfront", false}},
		{"c admin saved partial-upfront", `UPDATE global_config SET default_payment = 'partial-upfront' WHERE id = 1`, "",
			m166State{"partial-upfront", false}, m166State{"partial-upfront", false}},
		{"e inserted partial-upfront, created=updated", "DELETE FROM global_config; INSERT INTO global_config (id, default_payment) VALUES (1, 'partial-upfront')", "",
			m166State{"partial-upfront", true}, m166State{"partial-upfront", true}},
		{"f admin chose no-upfront after up", "", `UPDATE global_config SET default_payment = 'no-upfront' WHERE id = 1`,
			m166State{"no-upfront", false}, m166State{"no-upfront", false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			path := getMigrationsPath()
			pool := testhelpers.RequireEmptyDatabase(ctx, t).DB.Pool()
			prev := previousMigrationVersion(t, m166Version)
			require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, prev))
			if tc.beforeUp != "" {
				m166Exec(ctx, t, pool, tc.beforeUp)
			}

			require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, m166Version))
			if tc.afterUp != "" {
				m166Exec(ctx, t, pool, tc.afterUp)
			}
			assert.Equal(t, tc.wantUp, m166Read(ctx, t, pool), "after up")

			require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, prev))
			assert.Equal(t, tc.wantDown, m166Read(ctx, t, pool), "after down")
		})
	}
}

// TestMigration_DefaultPaymentNoUpfront_ColumnDefaults covers (d) and (d2): a
// new row with no payment gets no-upfront, and down restores all-upfront.
func TestMigration_DefaultPaymentNoUpfront_ColumnDefaults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := getMigrationsPath()
	pool := testhelpers.RequireEmptyDatabase(ctx, t).DB.Pool()
	prev := previousMigrationVersion(t, m166Version)
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, m166Version))

	insert := func() (global, service string) {
		m166Exec(ctx, t, pool, `DELETE FROM global_config; DELETE FROM service_configs`)
		m166Exec(ctx, t, pool, `INSERT INTO global_config (id) VALUES (1)`)
		m166Exec(ctx, t, pool, `INSERT INTO service_configs (provider, service) VALUES ('aws', 'ec2')`)
		require.NoError(t, pool.QueryRow(ctx, `SELECT default_payment FROM global_config WHERE id = 1`).Scan(&global))
		require.NoError(t, pool.QueryRow(ctx, `SELECT payment FROM service_configs WHERE provider = 'aws'`).Scan(&service))
		return global, service
	}

	g, s := insert()
	assert.Equal(t, "no-upfront", g, "(d) global_config default")
	assert.Equal(t, "no-upfront", s, "(d2) service_configs default")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, prev))
	g, s = insert()
	assert.Equal(t, "all-upfront", g, "down restores global_config default")
	assert.Equal(t, "all-upfront", s, "down restores service_configs default")
}
