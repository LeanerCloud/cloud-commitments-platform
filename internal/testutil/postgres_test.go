//go:build integration
// +build integration

package testutil

import (
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectionString_PasswordRoundTrip(t *testing.T) {
	pw := `p@ss:w/rd%#? "q" 'it\s'`
	pc := &PostgresContainer{
		Host: "localhost", Port: "5432", Database: "cudly_test",
		Username: "cudly_test", Password: pw,
	}

	u, err := url.Parse(pc.ConnectionString())
	require.NoError(t, err)
	got, _ := u.User.Password()
	assert.Equal(t, pw, got)

	cfg, err := pgconn.ParseConfig(pc.ConnectionString())
	require.NoError(t, err)
	assert.Equal(t, pw, cfg.Password)
	assert.Equal(t, "cudly_test", cfg.User)
	assert.Equal(t, "localhost", cfg.Host)
	assert.Equal(t, uint16(5432), cfg.Port)
	assert.Equal(t, "cudly_test", cfg.Database)
}
