//go:build integration

package config

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runbookSettleSQL reads the settle statement from the runbook itself, so the
// operator's copy and the tested statement cannot drift. The :name placeholders
// become positional parameters.
func runbookSettleSQL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../runbooks/ri-exchange-direct-execution-settle.md")
	require.NoError(t, err)
	m := regexp.MustCompile("(?s)```sql\n(-- :id[^`]*)```").FindSubmatch(raw)
	require.NotNil(t, m, "runbook has no sql block")
	return strings.NewReplacer(":new_status", "$1::text", ":id", "$2::uuid", ":note", "$3::text").Replace(string(m[1]))
}

// #1829 runbook: the settle statement releases only rows it is safe to release.
func TestRIExchangeDirectRunbook_SettleStatement(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	sql := runbookSettleSQL(t)
	age := func(id, interval string) {
		_, err := env.poolA.Exec(env.ctx, "UPDATE ri_exchange_direct_executions SET created_at = now() - $2::interval WHERE id = $1", id, interval)
		require.NoError(t, err)
	}
	settle := func(id string) int64 {
		tag, err := env.poolA.Exec(env.ctx, sql, "failed", id, "checked at AWS")
		require.NoError(t, err)
		return tag.RowsAffected()
	}

	unknown := env.directRow(t, "aws", "unknown", "ri-g1829-rb-unknown")
	age(unknown, "1 minute")
	assert.EqualValues(t, 1, settle(unknown), "a fresh unknown row is settled")
	assert.EqualValues(t, 0, settle(unknown), "a settled row is not settled twice")
	var status, errText string
	require.NoError(t, env.poolA.QueryRow(env.ctx, "SELECT status, error FROM ri_exchange_direct_executions WHERE id = $1", unknown).Scan(&status, &errText))
	assert.Equal(t, "failed", status)
	assert.Contains(t, errText, "checked at AWS")
	require.NoError(t, env.reserve(t, testAccountID(850000000001), "ri-g1829-rb-unknown"), "a settled row stops blocking the guard")

	old := env.directRow(t, "aws", "submitted", "ri-g1829-rb-old")
	age(old, "6 minutes")
	assert.EqualValues(t, 1, settle(old), "a submitted row older than 5 minutes is settled")

	young := env.directRow(t, "aws", "submitted", "ri-g1829-rb-young")
	age(young, "1 minute")
	assert.EqualValues(t, 0, settle(young), "a submitted row that may still be in flight is not settled")

	assert.EqualValues(t, 0, settle(env.directRow(t, "azure", "unknown", "ri-g1829-rb-azure")), "Azure rows are untouched")
	assert.EqualValues(t, 0, settle(env.directRow(t, "aws", "completed", "ri-g1829-rb-done")), "a completed row is final")
	assert.EqualValues(t, 0, settle(env.directRow(t, "aws", "failed", "ri-g1829-rb-failed")), "a failed row is final")
}
