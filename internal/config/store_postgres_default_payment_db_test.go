//go:build integration
// +build integration

package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultPayment_OneDefaultEverywhere pins issue #1758: the documented
// default in DefaultSettings, the store's no-row fallback and the migrated
// column defaults all agree on no-upfront.
func TestDefaultPayment_OneDefaultEverywhere(t *testing.T) {
	conn := setupTestContainerDB(t)
	if conn == nil {
		return
	}
	ctx := context.Background()

	var documented any
	for _, s := range DefaultSettings {
		if s.Key == "purchase_defaults.payment_option" {
			documented = s.Value
		}
	}
	assert.Equal(t, "no-upfront", documented, "defaults.go")

	for _, col := range []struct{ table, column string }{
		{"global_config", "default_payment"},
		{"service_configs", "payment"},
	} {
		var def string
		require.NoError(t, conn.QueryRow(ctx, `SELECT column_default FROM information_schema.columns
			WHERE table_name = $1 AND column_name = $2`, col.table, col.column).Scan(&def))
		assert.Equal(t, "'no-upfront'::character varying", def, col.table+"."+col.column)
	}

	_, err := conn.Exec(ctx, "DELETE FROM global_config")
	require.NoError(t, err)
	cfg, err := NewPostgresStore(conn).GetGlobalConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "no-upfront", cfg.DefaultPayment, "store no-row fallback")
}
