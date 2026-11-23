package mocks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// GetExecutionsByStatusesFiltered mocks the GetExecutionsByStatusesFiltered
// operation. Without an expectation for it, it answers from the
// GetExecutionsByStatuses expectation: the unfiltered rows are a superset of
// the filtered ones, and the History handler re-applies every filter in Go, so
// tests written against GetExecutionsByStatuses keep working.
func (m *MockConfigStore) GetExecutionsByStatusesFiltered(ctx context.Context, q config.ExecutionsByStatusesQuery) ([]config.PurchaseExecution, error) {
	if !isExpected(&m.Mock, "GetExecutionsByStatusesFiltered") {
		return m.GetExecutionsByStatuses(ctx, q.Statuses, q.Limit)
	}
	m.record("GetExecutionsByStatusesFiltered", ctx, q)
	args := m.Called(ctx, q)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("GetExecutionsByStatusesFiltered", "[]config.PurchaseExecution", args.Get(0))
	}
	return filterExecutionsLikeStore(v, q.Statuses, q.Limit), args.Error(1)
}

// GetExecutionsByStatuses mocks the GetExecutionsByStatuses operation.
func (m *MockConfigStore) GetExecutionsByStatuses(ctx context.Context, statuses []string, limit int) ([]config.PurchaseExecution, error) {
	m.record("GetExecutionsByStatuses", ctx, statuses, limit)
	args := m.Called(ctx, statuses, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("GetExecutionsByStatuses", "[]config.PurchaseExecution", args.Get(0))
	}
	return filterExecutionsLikeStore(v, statuses, limit), args.Error(1)
}

// ListHistoryExecutionsPage mocks the ListHistoryExecutionsPage operation.
// With no expectation registered it returns no rows. A registered
// expectation's rows go through the store contract (see
// pageExecutionsLikeStore), so a test can return one unordered fixture and
// page through it.
func (m *MockConfigStore) ListHistoryExecutionsPage(ctx context.Context, q config.HistoryExecutionsPageQuery) ([]config.PurchaseExecution, error) {
	m.record("ListHistoryExecutionsPage", ctx, q)
	if !isExpected(&m.Mock, "ListHistoryExecutionsPage") {
		return nil, nil
	}
	args := m.Called(ctx, q)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("ListHistoryExecutionsPage", "[]config.PurchaseExecution", args.Get(0))
	}
	return pageExecutionsLikeStore(v, &q), args.Error(1)
}

// ListPurchaseHistoryPage mocks the ListPurchaseHistoryPage operation. With
// no expectation registered it returns no rows. A registered expectation's
// rows are ordered, resumed after `after` and capped like the store does; the
// filter's predicates are not applied (match on the filter argument instead).
// The expectation may return []config.PurchaseHistoryPageRow, or plain
// []config.PurchaseHistoryRecord fixtures, which get row IDs from their
// position in the fixture (see PurchaseHistoryFixtureRowID).
func (m *MockConfigStore) ListPurchaseHistoryPage(ctx context.Context, filter config.PurchaseHistoryFilter, after *config.HistoryPageKey) ([]config.PurchaseHistoryPageRow, error) {
	m.record("ListPurchaseHistoryPage", ctx, filter, after)
	if !isExpected(&m.Mock, "ListPurchaseHistoryPage") {
		return nil, nil
	}
	args := m.Called(ctx, filter, after)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	var v []config.PurchaseHistoryPageRow
	switch rows := args.Get(0).(type) {
	case []config.PurchaseHistoryPageRow:
		v = rows
	case []config.PurchaseHistoryRecord:
		v = make([]config.PurchaseHistoryPageRow, len(rows))
		for i := range rows {
			v[i] = config.PurchaseHistoryPageRow{PurchaseHistoryRecord: rows[i], RowID: PurchaseHistoryFixtureRowID(i)}
		}
	default:
		m.reporter.wrongReturnType("ListPurchaseHistoryPage", "[]config.PurchaseHistoryPageRow or []config.PurchaseHistoryRecord", args.Get(0))
	}
	out := make([]config.PurchaseHistoryPageRow, 0, len(v))
	for i := range v {
		if after == nil || after.SortsBefore(v[i].Key()) {
			out = append(out, v[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key().SortsBefore(out[j].Key()) })
	return capHistoryPage(out, filter.Limit), args.Error(1)
}

// pageExecutionsLikeStore applies ListHistoryExecutionsPage's contract to
// canned rows: status filter, clean completed rows dropped, inclusive
// scheduled_date bounds, rows after q.After only, stream order, then the cap.
func pageExecutionsLikeStore(rows []config.PurchaseExecution, q *config.HistoryExecutionsPageQuery) []config.PurchaseExecution {
	var out []config.PurchaseExecution
	for i := range rows {
		if executionMatchesPageQuery(&rows[i], q) {
			out = append(out, rows[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return executionPageKey(&out[i]).SortsBefore(executionPageKey(&out[j]))
	})
	return capHistoryPage(out, q.Limit)
}

// PurchaseHistoryFixtureRowID is the row ID ListPurchaseHistoryPage gives the
// i-th row of a []config.PurchaseHistoryRecord fixture: a valid UUID, so a
// cursor built from it decodes, and later fixture rows sort lower.
func PurchaseHistoryFixtureRowID(i int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", 0xffffffffffff-i)
}

func executionMatchesPageQuery(r *config.PurchaseExecution, q *config.HistoryExecutionsPageQuery) bool {
	if !slices.Contains(q.Statuses, r.Status) || (r.Status == "completed" && r.Error == "") {
		return false
	}
	if (q.Start != nil && r.ScheduledDate.Before(*q.Start)) || (q.End != nil && r.ScheduledDate.After(*q.End)) {
		return false
	}
	return q.After == nil || q.After.SortsBefore(executionPageKey(r))
}

func executionPageKey(e *config.PurchaseExecution) config.HistoryPageKey {
	return config.HistoryPageKey{Timestamp: e.ScheduledDate, Source: config.HistorySourceExecution, ID: e.ExecutionID}
}

// capHistoryPage applies the History page limit bounds the store uses.
func capHistoryPage[T any](rows []T, limit int) []T {
	if limit <= 0 {
		limit = config.DefaultListLimit
	}
	limit = min(limit, config.MaxListLimit)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// filterExecutionsLikeStore applies the store contract to canned rows: filter by
// status, drop clean completed rows, order by ScheduledDate DESC with future
// pending rows last and soonest first (issue #680), then cap.
func filterExecutionsLikeStore(rows []config.PurchaseExecution, statuses []string, limit int) []config.PurchaseExecution {
	if len(statuses) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = config.DefaultListLimit
	}
	limit = min(limit, config.MaxListLimit)
	var out []config.PurchaseExecution
	for i := range rows {
		r := &rows[i]
		if !slices.Contains(statuses, r.Status) || (r.Status == "completed" && r.Error == "") {
			continue
		}
		out = append(out, *r)
	}
	now := time.Now()
	sort.SliceStable(out, func(i, j int) bool { return executionSortsBefore(&out[i], &out[j], now) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// executionSortsBefore mirrors GetExecutionsByStatuses' ORDER BY: rows that
// are not future pending steps first, newest first; future pending steps
// after them, soonest first.
func executionSortsBefore(a, b *config.PurchaseExecution, now time.Time) bool {
	aFuture := a.Status == "pending" && a.ScheduledDate.After(now)
	bFuture := b.Status == "pending" && b.ScheduledDate.After(now)
	if aFuture != bFuture {
		return bFuture
	}
	if aFuture {
		return a.ScheduledDate.Before(b.ScheduledDate)
	}
	return a.ScheduledDate.After(b.ScheduledDate)
}

// ErrHistorySummaryAggregateNotStubbed is HistorySummaryAggregate's default
// error. The History handler treats an aggregate error as "totals unavailable"
// and falls back to summarizing the rows it fetched, so tests written before
// the aggregate existed (issue #675) keep asserting the page summary without
// registering an expectation.
var ErrHistorySummaryAggregateNotStubbed = errors.New("mock: HistorySummaryAggregate not stubbed")

// HistorySummaryAggregate mocks the HistorySummaryAggregate operation. With no
// expectation registered it returns ErrHistorySummaryAggregateNotStubbed.
func (m *MockConfigStore) HistorySummaryAggregate(ctx context.Context, filter config.HistorySummaryFilter) (*config.HistorySummaryGroups, error) {
	m.record("HistorySummaryAggregate", ctx, filter)
	if !isExpected(&m.Mock, "HistorySummaryAggregate") {
		return nil, ErrHistorySummaryAggregateNotStubbed
	}
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.HistorySummaryGroups)
	if !ok {
		m.reporter.wrongReturnType("HistorySummaryAggregate", "*config.HistorySummaryGroups", args.Get(0))
	}
	return v, args.Error(1)
}
