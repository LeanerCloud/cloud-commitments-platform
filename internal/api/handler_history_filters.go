package api

import (
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
