package api

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// MaxHistoryDateRangeDays caps the inclusive start/end window the History
// handler accepts on a single request. Mirrors the analytics cap (issue
// #414 / PR #529): an unbounded range turns the WHERE-on-timestamp into a
// full-table scan over purchase_history, which the dashboard frontend
// neither needs nor renders coherently past a year. 366 admits a full leap
// year and rejects anything larger with 400.
const MaxHistoryDateRangeDays = 366

// historyFilters carries the shared filter set used by both halves of the
// merged /api/history response: the SQL path (purchase_history rows in
// fetchPurchaseHistory) and the in-memory path (synthesized execution rows
// in fetchExecutionsAsHistory). Keeping them in one struct guarantees the
// two halves stay scoped consistently — the bug behind issue #701 was that
// the executions path ignored the filters the SQL path was supposed to apply.
//
// Two account inputs are accepted but they are NOT interchangeable:
//   - LegacyAccountID is the singular `account_id` query param. It is the
//     cloud-provider account number (VARCHAR(20) — e.g. "123456789012"),
//     matched against purchase_history.account_id by GetPurchaseHistory.
//     Only the no-other-filters fast path consumes it; the filtered SQL
//     path ignores it because cloud_account_id is a UUID column.
//   - AccountIDs is the plural `account_ids` query param, a list of UUIDs
//     matched against purchase_history.cloud_account_id (the
//     cloud_accounts FK). This is what the frontend sends.
//
// HasDate is true iff start OR end was supplied. When false the Start/End
// times are zero-valued and the SQL/in-memory date predicates are skipped
// entirely (so legacy clients that don't send dates keep working).
type historyFilters struct {
	Start                 time.Time
	End                   time.Time
	ExternalIDsByProvider map[string][]string
	// Cursor is the stream position the page starts after (issue #674); nil
	// for the first page.
	Cursor          *config.HistoryPageKey
	Provider        string
	LegacyAccountID string
	// PlanID narrows the history to one purchase plan's purchases and
	// executions (the plan-history view's plan_id); empty means no plan filter.
	PlanID     string
	AccountIDs []string
	Limit      int
	HasDate    bool
}

// parseHistoryFilters validates and normalises the /api/history query string.
// Returns a 400 ClientError for malformed provider, non-UUID account_ids, a
// start/end that doesn't parse as YYYY-MM-DD, an inverted range
// (start > end), or a range exceeding MaxHistoryDateRangeDays.
func parseHistoryFilters(params map[string]string) (historyFilters, error) {
	var f historyFilters

	provider := params["provider"]
	if provider == "all" {
		provider = "" // "all" is the explicit "no filter" sentinel
	}
	if err := validateProvider(provider); err != nil {
		return f, err
	}
	f.Provider = provider

	accountIDs, err := parseAccountIDs(params["account_ids"])
	if err != nil {
		return f, NewClientError(400, err.Error())
	}
	f.AccountIDs = accountIDs
	f.LegacyAccountID = params["account_id"]

	planID, err := parseHistoryPlanID(params["plan_id"])
	if err != nil {
		return f, err
	}
	f.PlanID = planID

	start, end, hasDate, err := parseHistoryDateRange(params["start"], params["end"])
	if err != nil {
		return f, err
	}
	f.HasDate = hasDate
	f.Start = start
	f.End = end

	limit, err := parseHistoryLimit(params["limit"])
	if err != nil {
		return f, err
	}
	f.Limit = limit

	cursor, pagingErr := parseHistoryPaging(params)
	if pagingErr != nil {
		return f, pagingErr
	}
	f.Cursor = cursor

	return f, nil
}

// parseHistoryLimit parses the optional limit, clamped to [1, MaxListLimit]
// with DefaultListLimit for empty or non-positive values.
func parseHistoryLimit(raw string) (int, error) {
	limit := config.DefaultListLimit
	if raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return 0, NewClientError(400, "invalid limit: must be a positive integer")
		}
		limit = n
	}
	if limit < 1 {
		limit = config.DefaultListLimit
	}
	if limit > config.MaxListLimit {
		limit = config.MaxListLimit
	}
	return limit, nil
}

// parseHistoryPlanID validates the optional plan_id filter; empty means none.
// The value is trimmed and lowercased so the SQL predicate and the in-memory
// execution check compare the same canonical text.
func parseHistoryPlanID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", nil
	}
	if err := validateUUID(id); err != nil {
		return "", NewClientError(400, "invalid plan_id: must be a UUID")
	}
	return strings.ToLower(id), nil
}

// parseHistoryDateRange parses optional YYYY-MM-DD start/end strings. Both
// empty -> no date filter (hasDate=false, returned times are zero). One or
// both populated -> hasDate=true; an unset bound defaults to span the
// maximum allowed window from the supplied side. The window is capped at
// MaxHistoryDateRangeDays to mirror PR #529 (issue #414) and prevent a full-
// table-scan DoS via start=1970-01-01&end=2100-12-31.
//
// YYYY-MM-DD only (per issue #701 spec): the frontend's <input type="date">
// fields emit exactly that format, so accepting RFC 3339 here would be a
// surface area not exercised in production and a divergence from the
// analytics handler's broader format set.
func parseHistoryDateRange(startStr, endStr string) (time.Time, time.Time, bool, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	if startStr == "" && endStr == "" {
		return time.Time{}, time.Time{}, false, nil
	}
	start, end, err := parseHistoryDateBounds(startStr, endStr)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	const maxWindow = MaxHistoryDateRangeDays * 24 * time.Hour
	// Fill in the open side, if any, so the absent bound spans the maximum
	// allowed window from the supplied side. This keeps a "start only" or
	// "end only" call meaningful AND still bounded by the same DoS cap.
	switch {
	case startStr == "" && endStr != "":
		start = end.Add(-maxWindow)
	case startStr != "" && endStr == "":
		end = start.Add(maxWindow)
	}
	if start.After(end) {
		return time.Time{}, time.Time{}, false, NewClientError(400,
			"start date must be before or equal to end date")
	}
	if end.Sub(start) > maxWindow {
		return time.Time{}, time.Time{}, false, NewClientError(400,
			fmt.Sprintf("date range too large: maximum allowed range is %d days", MaxHistoryDateRangeDays))
	}
	return start, end, true, nil
}

// parseHistoryDateBounds parses each YYYY-MM-DD side independently. An empty
// string returns the zero value for that side; the caller is responsible for
// substituting a sensible default. The end side is rolled forward to end-of-
// day so a date input is inclusive of the chosen day.
func parseHistoryDateBounds(startStr, endStr string) (time.Time, time.Time, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	const layout = "2006-01-02"
	var start, end time.Time
	if startStr != "" {
		s, err := time.ParseInLocation(layout, startStr, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, NewClientError(400,
				"invalid start date format: expected YYYY-MM-DD")
		}
		start = s
	}
	if endStr != "" {
		e, err := time.ParseInLocation(layout, endStr, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, NewClientError(400,
				"invalid end date format: expected YYYY-MM-DD")
		}
		// Make end inclusive of the requested day (frontend semantics: a
		// user picking 2024-01-31 expects rows from that day to render).
		end = e.Add(24*time.Hour - time.Second)
	}
	return start, end, nil
}

// matchesExecution reports whether a PurchaseExecution should be retained
// after applying the request filters in Go (the SQL path is on
// purchase_history; this in-memory equivalent keeps the two halves of the
// merged response consistently scoped, issue #701).
//
//   - Provider: matches when ANY recommendation in the execution carries
//     the filter value. A single-rec execution collapses to one provider; a
//     multi-rec basket that spans providers (e.g. aws+azure) matches any of
//     them — dropping such an execution because its collapsed display label
//     is "multiple" would hide real activity the user owns.
//   - Account: matches when the execution's effective account ID is one of the
//     filtered IDs. The effective account ID is exec.CloudAccountID when set;
//     otherwise collapseRecommendationAccount(exec.Recommendations) provides
//     the fallback, matching the same logic used in executionToHistoryRow.
//     Web-initiated bulk purchases never set exec.CloudAccountID — only the
//     per-rec field is populated — so without this fallback an account-filtered
//     approval queue would silently drop all pending web purchases (issue #704).
//     An execution whose effective account ID is "" (nil exec field AND recs
//     disagree or have no account) is excluded once account_ids is non-empty.
//   - Date: matches when ScheduledDate is within [Start, End]. Inclusive
//     both sides; End is the end-of-day for YYYY-MM-DD inputs. Actionable
//     statuses skip this check so a pending approval scheduled after the
//     range end still reaches the approval queue (#1685).
func (f historyFilters) matchesExecution(exec config.PurchaseExecution) bool {
	if !f.matchesExecutionProviderAndAccount(exec) {
		return false
	}
	if f.PlanID != "" && exec.PlanID != f.PlanID {
		return false
	}
	if f.HasDate && !slices.Contains(actionableExecutionStatuses, exec.Status) {
		if exec.ScheduledDate.Before(f.Start) || exec.ScheduledDate.After(f.End) {
			return false
		}
	}
	return true
}

// matchesExecutionProviderAndAccount is matchesExecution without the date
// check. The History summary aggregate pushes the date bound into SQL
// (scheduled_date between Start and End, inclusive) and applies this to each
// execution group, so the row list and the totals share one provider and
// account predicate.
func (f historyFilters) matchesExecutionProviderAndAccount(exec config.PurchaseExecution) bool {
	if f.Provider != "" {
		if !executionHasProvider(exec, f.Provider) {
			return false
		}
	}
	if len(f.AccountIDs) > 0 || len(f.ExternalIDsByProvider) > 0 {
		if !accountMatchesFilters(exec, f.AccountIDs, f.ExternalIDsByProvider) {
			return false
		}
	}
	return true
}
