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

func TestMigration_DropRawApprovalToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	container := testhelpers.RequireEmptyDatabase(ctx, t)
	pool, path := container.DB.Pool(), getMigrationsPath()

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, 169))

	const raw = "left-behind-raw-token"
	peRaw := seedPurchaseExecutionWithToken(ctx, t, pool, "pending", strPtr(raw+"-pe"))
	riRaw := seedRIExchangeWithToken(ctx, t, pool, "", strPtr(raw+"-ri"))

	// The trap: a migrated row (hash set, raw blanked) must keep its hash.
	const existingHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	peEmpty := seedPurchaseExecutionWithToken(ctx, t, pool, "pending", strPtr(""))
	riEmpty := seedRIExchangeWithToken(ctx, t, pool, "", strPtr(""))
	_, err := pool.Exec(ctx, `UPDATE purchase_executions SET approval_token_hash = $1 WHERE execution_id = $2`, existingHash, peEmpty)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE ri_exchange_history SET approval_token_hash = $1 WHERE id = $2`, existingHash, riEmpty)
	require.NoError(t, err)

	columnExists := func(table string) bool {
		var ok bool
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM information_schema.columns
			 WHERE table_schema = current_schema() AND table_name = $1 AND column_name = 'approval_token')`,
			table).Scan(&ok))
		return ok
	}
	uniqueIndexOnRaw := func() bool {
		var ok bool
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_indexes
			 WHERE tablename = 'ri_exchange_history' AND indexdef ILIKE '%UNIQUE%'
			   AND indexdef ~ '\(approval_token\)')`).Scan(&ok))
		return ok
	}
	require.True(t, columnExists("purchase_executions"))
	require.True(t, columnExists("ri_exchange_history"))
	require.True(t, uniqueIndexOnRaw(), "000009 unique index must exist before the drop")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, 170))

	hashOf := func(table, idCol, id string) *string {
		var h *string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT approval_token_hash FROM `+table+` WHERE `+idCol+` = $1`, id).Scan(&h))
		return h
	}
	for _, c := range []struct{ table, idCol, id, raw string }{
		{"purchase_executions", "execution_id", peRaw, raw + "-pe"},
		{"ri_exchange_history", "id", riRaw, raw + "-ri"},
	} {
		h := hashOf(c.table, c.idCol, c.id)
		if assert.NotNil(t, h, "%s: leftover raw token must be hashed before the drop", c.table) {
			assert.Equal(t, sha256Hex(c.raw), *h, c.table)
		}
	}
	for _, c := range []struct{ table, idCol, id string }{
		{"purchase_executions", "execution_id", peEmpty},
		{"ri_exchange_history", "id", riEmpty},
	} {
		h := hashOf(c.table, c.idCol, c.id)
		if assert.NotNil(t, h, "%s: an existing hash must survive an empty raw value", c.table) {
			assert.Equal(t, existingHash, *h, c.table)
		}
	}
	assert.False(t, columnExists("purchase_executions"))
	assert.False(t, columnExists("ri_exchange_history"))
	assert.False(t, uniqueIndexOnRaw(), "the unique index on approval_token must go with the column")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, 169))
	for _, table := range []string{"purchase_executions", "ri_exchange_history"} {
		var nullable string
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT is_nullable FROM information_schema.columns
			 WHERE table_schema = current_schema() AND table_name = $1 AND column_name = 'approval_token'`,
			table).Scan(&nullable), "%s: column must be re-added by down", table)
		assert.Equal(t, "YES", nullable, table)
	}
	assert.Equal(t, existingHash, *hashOf("ri_exchange_history", "id", riEmpty), "down must keep the hashes")
}
