//go:build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_ListPurchaseHistoryPage_Scope pins the issue #632 pushdown
// of the account scope into the completed-history purchase read: rows of other
// tenants (a registered account outside the scope and an unregistered one) do
// not count against the page limit, an empty scope reads nothing, and a nil
// scope reads everything.
func TestIntegration_ListPurchaseHistoryPage_Scope(t *testing.T) {
	ctx := context.Background()
	f := newHistorySummaryFixture(ctx, t)
	a, b := f.accountA, f.accountB
	up := 1.0
	// Oldest first: the scoped account's rows are the oldest, so every other
	// tenant's row sorts ahead of them in the stream.
	f.savePurchase(ctx, t, 1, "aws", "ext-sum-a", &a, &up, 1, false)
	f.savePurchase(ctx, t, 2, "aws", "ext-sum-a", nil, &up, 1, false) // legacy row, external id only
	for day := 3; day < 12; day++ {
		f.savePurchase(ctx, t, day, "azure", "ext-sum-b", &b, &up, 1, false)
		f.savePurchase(ctx, t, day, "aws", "ext-foreign", nil, &up, 1, false)
	}

	read := func(scope *ExecutionScope, limit int) []PurchaseHistoryPageRow {
		t.Helper()
		rows, err := f.store.ListPurchaseHistoryPage(ctx, PurchaseHistoryFilter{Scope: scope, Limit: limit}, nil)
		require.NoError(t, err)
		return rows
	}
	accountsOf := func(rows []PurchaseHistoryPageRow) []string {
		out := make([]string, len(rows))
		for i := range rows {
			out[i] = rows[i].AccountID
		}
		return out
	}

	scopeA := &ExecutionScope{UserID: f.userID, AccountIDs: []string{a, "ext-sum-a"}}
	assert.Equal(t, []string{"ext-sum-a", "ext-sum-a"}, accountsOf(read(scopeA, 10)))
	assert.Len(t, read(scopeA, 1), 1, "the page limit counts only rows in scope")
	assert.Empty(t, read(&ExecutionScope{UserID: f.userID}, 10), "an empty scope reads nothing")
	assert.Len(t, read(nil, 100), 20, "a nil scope is unrestricted")
}

// TestIntegration_ListHistoryExecutionsPage_Pushdown pins the issue #632
// pushdown of provider, account and account scope into the terminal-execution
// read, over every account shape an execution can have, and that the keyset
// walk still returns each scoped row once in stream order.
func TestIntegration_ListHistoryExecutionsPage_Pushdown(t *testing.T) {
	ctx := context.Background()
	f := newHistorySummaryFixture(ctx, t)
	a, b, user := f.accountA, f.accountB, f.userID
	day := func(n int) time.Time { return f.base.Add(time.Duration(n) * 24 * time.Hour) }
	recA := []RecommendationRecord{{Provider: "aws", CloudAccountID: &a}}
	recB := []RecommendationRecord{{Provider: "azure", CloudAccountID: &b}}
	mixed := []RecommendationRecord{{Provider: "aws", CloudAccountID: &a}, {Provider: "azure", CloudAccountID: &b}}

	// Oldest first, so the scoped rows sit behind every foreign row.
	ids := map[string]string{
		"onA":    f.saveExecution(ctx, t, day(1), "failed", "boom", &a, nil, recA),
		"recsA":  f.saveExecution(ctx, t, day(2), "expired", "", nil, nil, recA),
		"mixMy":  f.saveExecution(ctx, t, day(3), StatusCanceled, "", nil, &user, mixed),
		"onB":    f.saveExecution(ctx, t, day(4), "failed", "boom", &b, nil, recB),
		"recsB":  f.saveExecution(ctx, t, day(5), "failed", "boom", nil, nil, recB),
		"noRecs": f.saveExecution(ctx, t, day(6), "failed", "boom", nil, nil, nil),
	}
	f.saveExecution(ctx, t, day(0), "completed", "", &a, nil, recA) // clean completed: always excluded
	f.saveExecution(ctx, t, day(7), "pending", "", &a, nil, recA)   // outside the terminal statuses
	terminal := []string{"completed", "partially_completed", "failed", "expired", StatusCanceled, LegacyStatusCanceled}

	names := func(q HistoryExecutionsPageQuery) []string {
		t.Helper()
		q.Statuses = terminal
		if q.Limit == 0 {
			q.Limit = 100
		}
		got := walkExecutions(ctx, t, f.store, q)
		out := make([]string, 0, len(got))
		for _, id := range got {
			for name, seeded := range ids {
				if seeded == id {
					out = append(out, name)
				}
			}
		}
		require.Len(t, out, len(got), "every row is a seeded execution")
		return out
	}

	tests := []struct {
		name string
		q    HistoryExecutionsPageQuery
		want []string
	}{
		{"no filter", HistoryExecutionsPageQuery{}, []string{"noRecs", "recsB", "onB", "mixMy", "recsA", "onA"}},
		{"provider azure", HistoryExecutionsPageQuery{Provider: "azure"}, []string{"recsB", "onB", "mixMy"}},
		{"account A", HistoryExecutionsPageQuery{AccountIDs: []string{a}}, []string{"mixMy", "recsA", "onA"}},
		{"scope A", HistoryExecutionsPageQuery{Scope: &ExecutionScope{UserID: user, AccountIDs: []string{a}}}, []string{"mixMy", "recsA", "onA"}},
		{"scope B for another user", HistoryExecutionsPageQuery{Scope: &ExecutionScope{UserID: "00000000-0000-4000-8000-000000000000", AccountIDs: []string{b}}},
			[]string{"recsB", "onB", "mixMy"}},
		{"empty scope keeps only the caller's unattributed rows", HistoryExecutionsPageQuery{Scope: &ExecutionScope{UserID: user}}, []string{"mixMy"}},
		{"filters and scope combine", HistoryExecutionsPageQuery{Provider: "azure", Scope: &ExecutionScope{UserID: user, AccountIDs: []string{a}}}, []string{"mixMy"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, names(tt.q))
		})
	}

	t.Run("pages of one row still cover the scope once", func(t *testing.T) {
		assert.Equal(t, []string{"mixMy", "recsA", "onA"},
			names(HistoryExecutionsPageQuery{Limit: 1, Scope: &ExecutionScope{UserID: user, AccountIDs: []string{a}}}))
	})
	t.Run("the limit counts only rows in scope", func(t *testing.T) {
		rows, err := f.store.ListHistoryExecutionsPage(ctx, HistoryExecutionsPageQuery{
			Statuses: terminal, Limit: 1, Scope: &ExecutionScope{UserID: user, AccountIDs: []string{a}},
		})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, ids["mixMy"], rows[0].ExecutionID, "foreign rows are newer but do not fill the page")
	})
}
