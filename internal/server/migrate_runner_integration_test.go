//go:build integration
// +build integration

package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runnerFixture returns a config for an empty database plus a migrations dir
// whose second file fails, so a run leaves schema_migrations dirty at 2.
func runnerFixture(t *testing.T, secondUp string) (*database.Config, *mockSecretResolver) {
	t.Helper()
	t.Setenv("ADMIN_PASSWORD_SECRET", "admin-secret")
	t.Setenv("ADMIN_EMAIL", "")
	t.Setenv(forceVersionEnv, "")
	dir := t.TempDir()
	files := map[string]string{
		"000001_a.up.sql": "CREATE TABLE a (id int);", "000001_a.down.sql": "DROP TABLE a;",
		"000002_b.up.sql": secondUp, "000002_b.down.sql": "SELECT 1;",
		"000003_c.up.sql": "CREATE TABLE c (id int);", "000003_c.down.sql": "DROP TABLE c;",
	}
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	c := testhelpers.RequireEmptyDatabase(context.Background(), t)
	cfg := *c.Config
	cfg.MigrationsPath = dir
	return &cfg, &mockSecretResolver{getResult: "pw"}
}

func TestRunDeployMigrations_AppliesPendingMigrations(t *testing.T) {
	cfg, r := runnerFixture(t, "CREATE TABLE b (id int);")
	rep, err := RunDeployMigrations(context.Background(), cfg, r, MigrateOptions{})
	require.NoError(t, err)
	assert.Equal(t, MigrateReport{From: 0, To: 3}, rep)

	rep, err = RunDeployMigrations(context.Background(), cfg, r, MigrateOptions{})
	require.NoError(t, err)
	assert.Equal(t, MigrateReport{From: 3, To: 3}, rep)
}

func TestRunDeployMigrations_DirtyRowReturnsRecoveryErrorAndStaysDirty(t *testing.T) {
	cfg, r := runnerFixture(t, "SELECT * FROM missing_table;")
	_, err := RunDeployMigrations(context.Background(), cfg, r, MigrateOptions{})
	require.Error(t, err)

	_, err = RunDeployMigrations(context.Background(), cfg, r, MigrateOptions{})
	require.ErrorContains(t, err, "CUDLY_FORCE_MIGRATION_VERSION")

	v, dirty, verr := realMigrateDeps.readVersion(context.Background(), mustPool(t, cfg), cfg.MigrationsPath)
	require.NoError(t, verr)
	assert.True(t, dirty)
	assert.Equal(t, uint(2), v)
}

func TestRunDeployMigrations_AllowedForceVersionClearsDirtyRow(t *testing.T) {
	cfg, r := runnerFixture(t, "SELECT * FROM missing_table;")
	_, err := RunDeployMigrations(context.Background(), cfg, r, MigrateOptions{})
	require.Error(t, err)

	// Fix the broken file, then force back to N-1 so 2 is re-applied.
	require.NoError(t, os.WriteFile(filepath.Join(cfg.MigrationsPath, "000002_b.up.sql"), []byte("CREATE TABLE b (id int);"), 0o600))
	rep, err := RunDeployMigrations(context.Background(), cfg, r, MigrateOptions{ForceVersion: intPtr(1)})
	require.NoError(t, err)
	assert.Equal(t, MigrateReport{From: 2, To: 3}, rep)
	_, set := os.LookupEnv(forceVersionEnv)
	assert.True(t, set) // t.Setenv("", ...) in the fixture: restored to the prior empty value
	assert.Empty(t, os.Getenv(forceVersionEnv))
}

func TestRunDeployMigrations_ForceDirtyVersionKeepsItApplied(t *testing.T) {
	cfg, r := runnerFixture(t, "SELECT * FROM missing_table;")
	_, err := RunDeployMigrations(context.Background(), cfg, r, MigrateOptions{})
	require.Error(t, err)

	// Force N itself: 2 is marked applied (its effects "landed"), Up() resumes at 3.
	rep, err := RunDeployMigrations(context.Background(), cfg, r, MigrateOptions{ForceVersion: intPtr(2)})
	require.NoError(t, err)
	assert.Equal(t, MigrateReport{From: 2, To: 3}, rep)
	v, dirty, verr := realMigrateDeps.readVersion(context.Background(), mustPool(t, cfg), cfg.MigrationsPath)
	require.NoError(t, verr)
	assert.False(t, dirty)
	assert.Equal(t, uint(3), v)
}

func mustPool(t *testing.T, cfg *database.Config) *pgxpool.Pool {
	t.Helper()
	conn, err := database.NewConnection(context.Background(), cfg, nil)
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	return conn.Pool()
}
