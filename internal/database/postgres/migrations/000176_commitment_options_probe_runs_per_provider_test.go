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

// Migration 000176 turns the singleton probe-run row into one row per
// provider: the existing row becomes 'aws', and the down migration backs up
// and removes every other provider before restoring the singleton.
func TestMigration_0176_ProbeRunsPerProvider_UpDownAndAWSBackfill(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	migrationsPath := getMigrationsPath()
	pool := testhelpers.RequireEmptyDatabase(ctx, t).DB.Pool()

	count := func(q string) (n int) {
		require.NoError(t, pool.QueryRow(ctx, q).Scan(&n))
		return n
	}
	columns := func() map[string]bool {
		rows, err := pool.Query(ctx, `SELECT column_name FROM information_schema.columns
			WHERE table_name = 'commitment_options_probe_runs'`)
		require.NoError(t, err)
		defer rows.Close()
		got := map[string]bool{}
		for rows.Next() {
			var c string
			require.NoError(t, rows.Scan(&c))
			got[c] = true
		}
		require.NoError(t, rows.Err())
		return got
	}

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, 176)))
	assert.Equal(t, map[string]bool{"singleton": true, "probed_at": true, "source_account_id": true}, columns())
	_, err := pool.Exec(ctx, `INSERT INTO commitment_options_probe_runs (probed_at, source_account_id) VALUES ('2026-01-02T03:04:05Z', 'acct-1')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO commitment_options_combos VALUES ('aws','rds',1,'no-upfront')`)
	require.NoError(t, err)

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 176))
	assert.Equal(t, map[string]bool{"provider": true, "probed_at": true, "source_account_id": true}, columns())
	var provider, src string
	require.NoError(t, pool.QueryRow(ctx, `SELECT provider, source_account_id FROM commitment_options_probe_runs`).Scan(&provider, &src))
	assert.Equal(t, "aws", provider, "the singleton row is backfilled as aws")
	assert.Equal(t, "acct-1", src)

	assert.Equal(t, 1, count(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'commitment_options_probe_runs'
		AND column_name = 'provider' AND column_default IS NULL`), "provider has no default after the backfill")
	_, err = pool.Exec(ctx, `INSERT INTO commitment_options_probe_runs (provider, probed_at, source_account_id) VALUES ('azure', NOW(), 'sub-1')`)
	require.NoError(t, err, "a second provider row coexists")
	_, err = pool.Exec(ctx, `INSERT INTO commitment_options_probe_runs (provider, probed_at, source_account_id) VALUES ('azure', NOW(), 'sub-2')`)
	require.Error(t, err, "provider is the primary key")
	_, err = pool.Exec(ctx, `INSERT INTO commitment_options_combos VALUES ('azure','compute',1,'all-upfront')`)
	require.NoError(t, err)

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, 176)))
	assert.Equal(t, map[string]bool{"singleton": true, "probed_at": true, "source_account_id": true}, columns())
	assert.Equal(t, 1, count(`SELECT count(*) FROM commitment_options_probe_runs`), "only the aws row survives the down migration")
	assert.Equal(t, 1, count(`SELECT count(*) FROM commitment_options_probe_runs WHERE source_account_id = 'acct-1'`))
	assert.Equal(t, 1, count(`SELECT count(*) FROM commitment_options_combos`))
	assert.Equal(t, 1, count(`SELECT count(*) FROM commitment_options_combos WHERE provider = 'aws'`))
	assert.Equal(t, 1, count(`SELECT count(*) FROM commitment_options_probe_runs_backup_000176 WHERE provider = 'azure' AND source_account_id = 'sub-1'`))
	assert.Equal(t, 1, count(`SELECT count(*) FROM commitment_options_combos_backup_000176 WHERE provider = 'azure'`))
	_, err = pool.Exec(ctx, `INSERT INTO commitment_options_probe_runs (probed_at, source_account_id) VALUES (NOW(), 'x')`)
	require.Error(t, err, "the singleton primary key is restored")
	_, err = pool.Exec(ctx, `INSERT INTO commitment_options_probe_runs (singleton, probed_at, source_account_id) VALUES (FALSE, NOW(), 'x')`)
	require.Error(t, err, "the singleton check is restored")

	// up -> down again with newer azure data: the backup keeps the newest run
	// row and the union of removed combos, and the second down does not fail.
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 176), "up again after down")
	_, err = pool.Exec(ctx, `INSERT INTO commitment_options_probe_runs (provider, probed_at, source_account_id) VALUES ('azure', NOW(), 'sub-9')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO commitment_options_combos VALUES ('azure','compute',3,'all-upfront'), ('azure','compute',1,'all-upfront')`)
	require.NoError(t, err)
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, 176)))
	assert.Equal(t, 1, count(`SELECT count(*) FROM commitment_options_probe_runs_backup_000176`))
	assert.Equal(t, 1, count(`SELECT count(*) FROM commitment_options_probe_runs_backup_000176 WHERE provider = 'azure' AND source_account_id = 'sub-9'`), "newer run row kept")
	assert.Equal(t, 2, count(`SELECT count(*) FROM commitment_options_combos_backup_000176`), "combos are the union of both downs")
}
