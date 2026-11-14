//go:build integration

package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #120 item 3: UpdateCloudAccount refuses an external_id change while
// history rows key on the stored value, and still allows it otherwise.
func TestUpdateCloudAccount_ExternalIDChangeWithHistory(t *testing.T) {
	conn := setupTestContainerDB(t)
	cleanupTestData(t, conn)
	store := NewPostgresStore(conn)
	pool := conn.Pool()
	ctx := context.Background()

	extID := func(id string) string {
		var v string
		require.NoError(t, pool.QueryRow(ctx, `SELECT external_id FROM cloud_accounts WHERE id = $1`, id).Scan(&v))
		return v
	}
	rename := func(id, provider, ext string) error {
		return store.UpdateCloudAccount(ctx, &CloudAccount{ID: id, Name: "renamed", Provider: provider, ExternalID: ext, Enabled: true})
	}

	t.Run("no history allows the change", func(t *testing.T) {
		id := registerStampAccount(ctx, t, pool, "aws", "100000000001")
		require.NoError(t, rename(id, "aws", "100000000002"))
		assert.Equal(t, "100000000002", extID(id))
	})

	t.Run("purchase history blocks the change", func(t *testing.T) {
		id := registerStampAccount(ctx, t, pool, "aws", "100000000003")
		require.NoError(t, store.SavePurchaseHistory(ctx, stampRecord("aws", "100000000003", "ph-120-a")))
		require.ErrorIs(t, rename(id, "aws", "100000000004"), ErrAccountExternalIDHasHistory)
		assert.Equal(t, "100000000003", extID(id))
		require.NoError(t, rename(id, "aws", "100000000003"), "an update that keeps the external id is still allowed")
	})

	t.Run("legacy upper-case provider history blocks the change", func(t *testing.T) {
		id := registerStampAccount(ctx, t, pool, "aws", "100000000005")
		require.NoError(t, store.SavePurchaseHistory(ctx, stampRecord("AWS", "100000000005", "ph-120-b")))
		require.ErrorIs(t, rename(id, "aws", "100000000006"), ErrAccountExternalIDHasHistory)
	})

	t.Run("history of another provider with the same id does not block", func(t *testing.T) {
		id := registerStampAccount(ctx, t, pool, "azure", "100000000007")
		require.NoError(t, store.SavePurchaseHistory(ctx, stampRecord("aws", "100000000007", "ph-120-c")))
		require.NoError(t, rename(id, "azure", "100000000008"))
	})

	t.Run("ri exchange history blocks an aws account", func(t *testing.T) {
		id := registerStampAccount(ctx, t, pool, "aws", "100000000009")
		_, err := pool.Exec(ctx, `INSERT INTO ri_exchange_history
			(account_id, region, source_ri_ids, source_instance_type, source_count,
			 target_offering_id, target_instance_type, target_count)
			VALUES ('100000000009', 'us-east-1', ARRAY['ri-1'], 'm5.large', 1, 'off-1', 'm5.xlarge', 1)`)
		require.NoError(t, err)
		require.ErrorIs(t, rename(id, "aws", "100000000010"), ErrAccountExternalIDHasHistory)
	})

	t.Run("unknown account is not found, not a history refusal", func(t *testing.T) {
		err := rename("00000000-0000-0000-0000-000000000000", "aws", "100000000011")
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrAccountExternalIDHasHistory)
		assert.Contains(t, err.Error(), "not found")
	})
}
