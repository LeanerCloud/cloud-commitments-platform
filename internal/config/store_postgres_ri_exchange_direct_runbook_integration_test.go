//go:build integration

package config

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runbookBreakGlassSQLRaw reads the last-resort UPDATE from the runbook
// itself, so the operator's copy and the tested statement cannot drift.
func runbookBreakGlassSQLRaw(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../runbooks/ri-exchange-direct-execution-settle.md")
	require.NoError(t, err)
	m := regexp.MustCompile("(?s)```sql\n(-- :'id'[^`]*)```").FindSubmatch(raw)
	require.NotNil(t, m, "runbook has no sql block")
	return string(m[1])
}

// runbookBreakGlassSQL turns the :'name' psql placeholders into positional
// parameters.
func runbookBreakGlassSQL(t *testing.T) string {
	t.Helper()
	return strings.NewReplacer(
		":'outcome'", "$1::text", ":'id'", "$2::uuid", ":'operator'", "$3::text",
		":'note'", "$4::text", ":'checked_at'", "$5::timestamptz", ":'expected_status'", "$6::text",
	).Replace(runbookBreakGlassSQLRaw(t))
}

// The intervals in the SQL are the store's constants, not copies that can drift.
func TestRIExchangeDirectRunbook_IntervalsMatchStore(t *testing.T) {
	sql := runbookBreakGlassSQL(t)
	minutes := func(re string) []time.Duration {
		matches := regexp.MustCompile(re).FindAllStringSubmatch(sql, -1)
		out := make([]time.Duration, 0, len(matches))
		for _, m := range matches {
			n, err := strconv.Atoi(m[1])
			require.NoError(t, err)
			out = append(out, time.Duration(n)*time.Minute)
		}
		return out
	}
	assert.Equal(t, []time.Duration{RIExchangeDirectResolveSubmittedAfter, RIExchangeDirectResolveSubmittedAfter},
		minutes(`interval '(\d+) minutes'`), "both submitted-age intervals equal the store constant")
	assert.Equal(t, []time.Duration{RIExchangeDirectResolveUnknownCheckAfter},
		minutes(`interval '(\d+) minute'`), "the unknown check interval equals the store constant")
}

// The note limit in the SQL is the store's constant.
func TestRIExchangeDirectRunbook_NoteLimitMatchesStore(t *testing.T) {
	m := regexp.MustCompile(`octet_length\(:'note'\) <= (\d+)`).FindStringSubmatch(runbookBreakGlassSQLRaw(t))
	require.NotNil(t, m, "runbook SQL has no note length guard")
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	assert.Equal(t, RIExchangeDirectResolveNoteMax, n)
}

// The last-resort statement resolves only rows the store would resolve, under
// the same age and checked_at rules, and a resolved row stops blocking.
func TestRIExchangeDirectRunbook_BreakGlassStatement(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	sql := runbookBreakGlassSQL(t)
	// Fixtures are relative to the database clock; the checked_at values are
	// computed in SQL from the row's own times, never from Go time.
	checkedAt := func(id, offset string) time.Time {
		var at time.Time
		require.NoError(t, env.poolA.QueryRow(env.ctx, `
			SELECT CASE WHEN status = 'submitted' THEN created_at + interval '10 minutes' ELSE completed_at + interval '1 minute' END
			       + ($2::text)::interval
			  FROM ri_exchange_direct_executions WHERE id = $1`, id, offset).Scan(&at))
		return at
	}
	agedFor := func(provider, status, ri, createdAgo, completedAgo string) string {
		id := env.directRow(t, provider, status, ri)
		_, err := env.poolA.Exec(env.ctx, `
			UPDATE ri_exchange_direct_executions
			   SET created_at = now() - ($2::text)::interval,
			       completed_at = CASE WHEN status = 'submitted' THEN NULL ELSE now() - ($3::text)::interval END
			 WHERE id = $1`, id, createdAgo, completedAgo)
		require.NoError(t, err)
		return id
	}
	aged := func(status, ri, createdAgo, completedAgo string) string {
		return agedFor("aws", status, ri, createdAgo, completedAgo)
	}
	settleWith := func(id, outcome, expected, note string, at time.Time) int64 {
		tag, err := env.poolA.Exec(env.ctx, sql, outcome, id, "operator-a", note, at, expected)
		require.NoError(t, err)
		return tag.RowsAffected()
	}
	settle := func(id, outcome string, at time.Time) int64 {
		var status string
		require.NoError(t, env.poolA.QueryRow(env.ctx, "SELECT status FROM ri_exchange_direct_executions WHERE id = $1", id).Scan(&status))
		return settleWith(id, outcome, status, "checked at AWS", at)
	}

	unknown := aged("unknown", "ri-g1836-rb-unknown", "2 hours", "1 hour")
	assert.EqualValues(t, 0, settle(unknown, "failed", checkedAt(unknown, "-1 microsecond")), "unknown: checked_at just before completed_at + 1 minute")
	assert.EqualValues(t, 0, settle(unknown, "failed", checkedAt(unknown, "10 hours")), "unknown: checked_at in the future")
	require.ErrorIs(t, env.reserve(t, testAccountID(850000000001), "ri-g1836-rb-unknown"), ErrRIExchangeSourceInFlight, "an unresolved row keeps blocking")
	assert.EqualValues(t, 0, settleWith(unknown, "failed", "submitted", "checked at AWS", checkedAt(unknown, "0")), "a status other than the one seen is not resolved")
	assert.EqualValues(t, 0, settleWith(unknown, "failed", "unknown", strings.Repeat("x", RIExchangeDirectResolveNoteMax+1), checkedAt(unknown, "0")), "a note over the store limit is not stored")
	assert.EqualValues(t, 1, settle(unknown, "failed", checkedAt(unknown, "0")), "unknown: exactly completed_at + 1 minute")
	assert.EqualValues(t, 0, settle(unknown, "failed", checkedAt(unknown, "0")), "a resolved row is not resolved twice")
	var status, resolved, by, note string
	var userID *string
	require.NoError(t, env.poolA.QueryRow(env.ctx, `
		SELECT status, resolved_status, resolved_by, resolution_note, resolved_by_user_id::text
		  FROM ri_exchange_direct_executions WHERE id = $1`, unknown).Scan(&status, &resolved, &by, &note, &userID))
	assert.Equal(t, []any{"unknown", "failed", "operator-a", "checked at AWS", (*string)(nil)}, []any{status, resolved, by, note, userID})
	require.NoError(t, env.reserve(t, testAccountID(850000000001), "ri-g1836-rb-unknown"), "a resolved row stops blocking the guard")

	old := aged("submitted", "ri-g1836-rb-old", "11 minutes", "0")
	assert.EqualValues(t, 0, settle(old, "completed", checkedAt(old, "-1 microsecond")), "submitted: checked_at before created_at + 10 minutes")
	assert.EqualValues(t, 1, settle(old, "completed", checkedAt(old, "0")), "submitted older than 10 minutes, checked at exactly the bound")

	young := aged("submitted", "ri-g1836-rb-young", "6 minutes", "0")
	assert.EqualValues(t, 0, settle(young, "failed", checkedAt(young, "0")), "submitted inside the 10 minute window")

	// Old enough and checked late enough that only the provider test refuses it.
	azure := agedFor("azure", "unknown", "ri-g1836-rb-azure", "2 hours", "1 hour")
	assert.EqualValues(t, 0, settle(azure, "failed", checkedAt(azure, "0")), "Azure rows are untouched")
	done := env.directRow(t, "aws", "completed", "ri-g1836-rb-done")
	assert.EqualValues(t, 0, settle(done, "failed", time.Now().UTC().Add(-time.Minute)), "a completed row is final")
}
