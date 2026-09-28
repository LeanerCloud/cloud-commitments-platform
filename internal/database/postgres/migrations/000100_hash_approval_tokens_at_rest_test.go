//go:build integration
// +build integration

package migrations_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sha256Hex mirrors config.HashApprovalToken so the test asserts against the
// same digest format the application computes, without importing internal/api
// (which would pull the whole handler graph into a migrations_test build).
func sha256Hex(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// seedPurchaseExecutionWithToken inserts a minimal purchase_executions row
// carrying approval_token and returns its execution_id.
func seedPurchaseExecutionWithToken(ctx context.Context, t *testing.T, pool *pgxpool.Pool, token string) string {
	t.Helper()
	var execID string
	err := pool.QueryRow(ctx, `
		INSERT INTO purchase_executions (status, scheduled_date, approval_token)
		VALUES ('pending', NOW(), $1)
		RETURNING execution_id
	`, token).Scan(&execID)
	require.NoError(t, err)
	return execID
}

// tokenColumnsOf reads back one row's (approval_token, approval_token_hash)
// pair; NULL scans as nil.
func tokenColumnsOf(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table, idCol, id string) (raw, hash *string) {
	t.Helper()
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT approval_token, approval_token_hash FROM `+table+` WHERE `+idCol+` = $1`, id).Scan(&raw, &hash))
	return raw, hash
}

// seedRIExchangeWithToken inserts a minimal ri_exchange_history row carrying
// approval_token and returns its id.
func seedRIExchangeWithToken(ctx context.Context, t *testing.T, pool *pgxpool.Pool, token string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO ri_exchange_history (
			account_id, region, source_ri_ids, source_instance_type, source_count,
			target_offering_id, target_instance_type, target_count, approval_token
		) VALUES ('123456789012', 'us-east-1', ARRAY['ri-1'], 'm5.large', 1,
		          'offering-1', 'm5.xlarge', 1, $1)
		RETURNING id
	`, token).Scan(&id)
	require.NoError(t, err)
	return id
}

type tokenTable struct {
	name, idCol string
	seed        func(ctx context.Context, t *testing.T, pool *pgxpool.Pool, token string) string
}

var tokenTables = []tokenTable{
	{"purchase_executions", "execution_id", seedPurchaseExecutionWithToken},
	{"ri_exchange_history", "id", seedRIExchangeWithToken},
}

func assertMigrated(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tt tokenTable, id, rawToken, msg string) {
	t.Helper()
	raw, hash := tokenColumnsOf(ctx, t, pool, tt.name, tt.idCol, id)
	assert.Nil(t, raw, "%s: %s: the raw approval_token must be NULLed", tt.name, msg)
	if assert.NotNil(t, hash, "%s: %s: approval_token_hash must be set", tt.name, msg) {
		assert.Equal(t, sha256Hex(rawToken), *hash, "%s: %s: approval_token_hash must be the SHA-256 hex digest of the raw token exactly once", tt.name, msg)
	}
}

// TestMigration_HashApprovalTokensAtRest locks down migration 000100
// (issue #103, expand-contract step 1): every raw approval_token on
// purchase_executions and ri_exchange_history moves into approval_token_hash
// as its SHA-256 hex digest and the raw column is NULLed; empty raw values
// become NULL, not the digest of ”; re-running the up SQL sweeps rows that
// pre-#103 code wrote after the first run without double-hashing migrated
// rows; and the no-op down keeps the hashes so a down-then-up is lossless.
func TestMigration_HashApprovalTokensAtRest(t *testing.T) {
	ctx := context.Background()
	migrationsPath := getMigrationsPath()

	container, err := testhelpers.SetupPostgresContainer(ctx, t)
	require.NoError(t, err)
	defer container.Cleanup(ctx)
	pool := container.DB.Pool()

	prev := previousMigrationVersion(t, 100)
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, prev))

	const rawToken = "raw-approval-token-abc123"
	withToken := map[string]string{}
	withoutToken := map[string]string{}
	for _, tt := range tokenTables {
		withToken[tt.name] = tt.seed(ctx, t, pool, rawToken+tt.name)
		withoutToken[tt.name] = tt.seed(ctx, t, pool, "")
	}

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 100))

	for _, tt := range tokenTables {
		assertMigrated(ctx, t, pool, tt, withToken[tt.name], rawToken+tt.name, "after up")
		raw, hash := tokenColumnsOf(ctx, t, pool, tt.name, tt.idCol, withoutToken[tt.name])
		assert.Nil(t, raw, "%s: an empty raw token must be NULLed", tt.name)
		assert.Nil(t, hash, "%s: an empty raw token must not become the digest of ''", tt.name)
	}

	// Pre-#103 code still running against the migrated schema writes a raw
	// token; a manual re-run of the up SQL must sweep it and must leave the
	// already-migrated rows' hashes untouched.
	lateWritten := map[string]string{}
	for _, tt := range tokenTables {
		lateWritten[tt.name] = tt.seed(ctx, t, pool, "late-"+rawToken+tt.name)
	}
	upSQL, err := os.ReadFile(filepath.Join(migrationsPath, "000100_hash_approval_tokens_at_rest.up.sql"))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(upSQL))
	require.NoError(t, err, "re-running 000100 up must succeed")
	for _, tt := range tokenTables {
		assertMigrated(ctx, t, pool, tt, withToken[tt.name], rawToken+tt.name, "after a re-run")
		assertMigrated(ctx, t, pool, tt, lateWritten[tt.name], "late-"+rawToken+tt.name, "row written by old code after the first run")
	}

	// The down is a documented no-op (dropping the hash column would destroy
	// the only copy of every live token); down-then-up must not re-hash.
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, prev))
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 100))
	for _, tt := range tokenTables {
		assertMigrated(ctx, t, pool, tt, withToken[tt.name], rawToken+tt.name, "after down-then-up")
	}
}
