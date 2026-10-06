package migrations

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildMigrateDSN_RoundTripsReservedCharacters builds the DSN from pool
// configs whose credentials contain URL-reserved characters and checks that
// both net/url and pgx recover the exact original values (issue #301).
func TestBuildMigrateDSN_RoundTripsReservedCharacters(t *testing.T) {
	const (
		reserved = "p@ss:word/with?x=1#frag%41 space"
		plus     = "a+b c"
	)
	cases := map[string]struct{ user, password string }{
		"reserved characters": {user: "cudly", password: reserved},
		"literal plus":        {user: "cudly", password: plus},
		"reserved in user":    {user: "us@er:name", password: "plain"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			poolCfg, err := pgxpool.ParseConfig("postgres://x:y@db.example.com:5433/cudly?sslmode=require")
			require.NoError(t, err)
			poolCfg.ConnConfig.User = tc.user
			poolCfg.ConnConfig.Password = tc.password

			dsn := buildMigrateDSN(poolCfg)

			parsed, err := url.Parse(dsn)
			require.NoError(t, err)
			password, _ := parsed.User.Password()
			assert.Equal(t, tc.user, parsed.User.Username())
			assert.Equal(t, tc.password, password)
			assert.Equal(t, "db.example.com", parsed.Hostname())
			assert.Equal(t, "5433", parsed.Port())
			assert.Equal(t, "/cudly", parsed.Path)
			assert.Equal(t, "require", parsed.Query().Get("sslmode"))
			assert.NotContains(t, dsn, "%252541", "the password must not be double-encoded")

			pgxCfg, err := pgx.ParseConfig(strings.Replace(dsn, migrateURLScheme+"://", "postgres://", 1))
			require.NoError(t, err)
			assert.Equal(t, tc.user, pgxCfg.User)
			assert.Equal(t, tc.password, pgxCfg.Password)
			assert.Equal(t, "db.example.com", pgxCfg.Host)
			assert.Equal(t, uint16(5433), pgxCfg.Port)
			assert.Equal(t, "cudly", pgxCfg.Database)
		})
	}
}

// TestDatabaseMigrationWorkflow_EncodesPassword guards the shell half of
// issue #301: the workflow must never interpolate the raw password into the
// migration URL's userinfo.
func TestDatabaseMigrationWorkflow_EncodesPassword(t *testing.T) {
	path := filepath.Join(mainModuleRoot(t), ".github", "workflows", "database-migration.yml")
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	var dsnLines int
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.Contains(line, "DB_URL=\"pgx5://") {
			continue
		}
		dsnLines++
		assert.NotContains(t, line, "${DB_PASSWORD}", "raw password in migration URL: %s", strings.TrimSpace(line))
		assert.Contains(t, line, "${ENCODED_PASSWORD}", "migration URL must use the encoded password: %s", strings.TrimSpace(line))
	}
	assert.NotZero(t, dsnLines, "no migration URL found in %s; update this guard", path)
}
