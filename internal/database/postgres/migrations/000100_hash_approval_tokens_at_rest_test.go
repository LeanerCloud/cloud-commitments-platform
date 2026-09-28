//go:build integration
// +build integration

package migrations_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// purchaseApprovalTokenOf reads back one purchase_executions row's raw
// approval_token column value (NULL scans as "").
func purchaseApprovalTokenOf(ctx context.Context, t *testing.T, pool *pgxpool.Pool, execID string) string {
	t.Helper()
	var tok *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT approval_token FROM purchase_executions WHERE execution_id = $1`, execID).Scan(&tok))
	if tok == nil {
		return ""
	}
	return *tok
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

// riExchangeApprovalTokenOf reads back one ri_exchange_history row's raw
// approval_token column value (NULL scans as "").
func riExchangeApprovalTokenOf(ctx context.Context, t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var tok *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT approval_token FROM ri_exchange_history WHERE id = $1`, id).Scan(&tok))
	if tok == nil {
		return ""
	}
	return *tok
}

// TestMigration_HashApprovalTokensAtRest locks down migration 000100
// (issue #103): every existing non-empty approval_token on both
// purchase_executions and ri_exchange_history is rewritten in place to its
// SHA-256 hex digest, NULL/empty values are left alone, and the down
// migration is a documented no-op that leaves the hashed values in place.
func TestMigration_HashApprovalTokensAtRest(t *testing.T) {
	ctx := context.Background()
	migrationsPath := getMigrationsPath()

	container, err := testhelpers.SetupPostgresContainer(ctx, t)
	require.NoError(t, err)
	defer container.Cleanup(ctx)
	pool := container.DB.Pool()

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, 100)))

	const rawPurchaseToken = "raw-purchase-approval-token-abc123"
	purchaseWithToken := seedPurchaseExecutionWithToken(ctx, t, pool, rawPurchaseToken)
	purchaseWithoutToken := seedPurchaseExecutionWithToken(ctx, t, pool, "")

	const rawExchangeToken = "raw-ri-exchange-approval-token-xyz789"
	exchangeWithToken := seedRIExchangeWithToken(ctx, t, pool, rawExchangeToken)
	exchangeWithoutToken := seedRIExchangeWithToken(ctx, t, pool, "")

	require.Equal(t, rawPurchaseToken, purchaseApprovalTokenOf(ctx, t, pool, purchaseWithToken),
		"seed must start with the raw token stored")
	require.Equal(t, rawExchangeToken, riExchangeApprovalTokenOf(ctx, t, pool, exchangeWithToken),
		"seed must start with the raw token stored")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, 100))

	assert.Equal(t, sha256Hex(rawPurchaseToken), purchaseApprovalTokenOf(ctx, t, pool, purchaseWithToken),
		"a non-empty purchase_executions.approval_token must be rewritten to its SHA-256 hex digest")
	assert.Empty(t, purchaseApprovalTokenOf(ctx, t, pool, purchaseWithoutToken),
		"an empty approval_token must be left alone, not hashed into the digest of an empty string")

	assert.Equal(t, sha256Hex(rawExchangeToken), riExchangeApprovalTokenOf(ctx, t, pool, exchangeWithToken),
		"a non-empty ri_exchange_history.approval_token must be rewritten to its SHA-256 hex digest")
	assert.Empty(t, riExchangeApprovalTokenOf(ctx, t, pool, exchangeWithoutToken),
		"an empty approval_token must be left alone, not hashed into the digest of an empty string")

	// The down migration is a documented no-op: SHA-256 is one-way, so rolling
	// back must succeed and must leave the hashed values in place rather than
	// attempting (and failing) to restore the raw originals.
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, 100)))
	assert.Equal(t, sha256Hex(rawPurchaseToken), purchaseApprovalTokenOf(ctx, t, pool, purchaseWithToken),
		"the no-op down must leave the hashed value in place")
	assert.Equal(t, sha256Hex(rawExchangeToken), riExchangeApprovalTokenOf(ctx, t, pool, exchangeWithToken),
		"the no-op down must leave the hashed value in place")
}
