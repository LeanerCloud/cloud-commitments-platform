//go:build integration

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func historyIDs(rows []RIExchangeDirectHistoryRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// #1883: the History listing covers every provider, status and resolved state,
// newest first, bounded by age and by the account restriction.
func TestPostgresStoreDB_RIExchangeDirectHistory(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	a, b := strPtr("acc-a"), strPtr("acc-b")
	oldest := env.insertDirect(t, directSpec{status: "completed", account: a, createdAt: now.Add(-3 * time.Hour)})
	azure := env.insertDirect(t, directSpec{provider: "azure", status: "failed", account: b, createdAt: now.Add(-2 * time.Hour), errText: "boom"})
	resolved := env.insertDirect(t, directSpec{status: "unknown", account: a, resolved: true, createdAt: now.Add(-time.Hour)})
	unattributed := env.insertDirect(t, directSpec{status: "submitted", createdAt: now})
	env.insertDirect(t, directSpec{status: "completed", account: a, createdAt: now.AddDate(-2, 0, 0)}) // past the window

	since := now.AddDate(-1, 0, 0)
	all, err := env.storeA.ListRIExchangeDirectHistory(env.ctx, RIExchangeDirectHistoryFilter{Since: since, Limit: 10, AllAccounts: true})
	require.NoError(t, err)
	assert.Equal(t, []string{unattributed, resolved, azure, oldest}, historyIDs(all))
	assert.Equal(t, "azure", all[2].Provider)
	assert.Equal(t, "boom", all[2].Error)
	assert.Equal(t, "failed", all[1].ResolvedStatus)
	assert.NotNil(t, all[1].ResolvedAt)
	assert.Empty(t, all[0].ResolvedStatus)

	scoped, err := env.storeA.ListRIExchangeDirectHistory(env.ctx, RIExchangeDirectHistoryFilter{Since: since, Limit: 10, AccountIDs: []string{"acc-b"}})
	require.NoError(t, err)
	assert.Equal(t, []string{azure}, historyIDs(scoped), "a scoped list excludes other accounts and unattributed rows")

	none, err := env.storeA.ListRIExchangeDirectHistory(env.ctx, RIExchangeDirectHistoryFilter{Since: since, Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, none, "an empty scope sees nothing")

	capped, err := env.storeA.ListRIExchangeDirectHistory(env.ctx, RIExchangeDirectHistoryFilter{Since: since, Limit: 2, AllAccounts: true})
	require.NoError(t, err)
	assert.Equal(t, []string{unattributed, resolved}, historyIDs(capped))
}
