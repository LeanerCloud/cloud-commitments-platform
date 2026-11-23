// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"sort"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// History handlers.
func (h *Handler) getHistory(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	// Purchase history can leak across accounts — gate on view:purchases AND
	// filter the returned records by the session's allowed_accounts list.
	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}

	filters, err := parseHistoryFilters(params)
	if err != nil {
		return nil, err
	}

	// Resolve the requested account identifiers to the dual-column filter
	// inputs (UUIDs + their external account numbers) so both the SQL path and
	// the in-memory execution path match rows that carry only one of the two
	// representations (issue #701/#498/#866). The legacy singular account_id
	// (a top-bar chip UUID, or a raw external number for pre-UUID callers) is
	// folded in here so fetchPurchaseHistory sees a single unified account set.
	h.resolveHistoryAccountFilter(ctx, &filters)

	// The account scope is resolved once per request and applied to the
	// paged stream (pushed into SQL and re-checked per batch), the actionable
	// rows and the summary (issues #632, #675).
	scope, err := h.loadHistoryScope(ctx, session)
	if err != nil {
		return nil, err
	}

	// The paged stream: completed purchases and terminal executions, newest
	// first, filters.Limit rows the caller may see, starting after
	// filters.Cursor (issue #674, see handler_history_page.go).
	page, err := h.fetchHistoryPage(ctx, scope, &filters)
	if err != nil {
		return nil, err
	}

	// Pull the actionable executions so the user can see (and cancel) their
	// own in-flight approvals without waiting for the email. Executions live
	// in a separate table (purchase_executions) from the completed
	// purchase_history rows, so we merge after the fact. They are not paged:
	// the first page lists the whole class (up to its own cap) whatever the
	// page size, and later pages leave it out, so paging can never push a
	// pending approval off the first page. A failure to list them must not
	// hide completed history — log, skip, continue, and flag the response.
	// The same filter set and the account scope are pushed into the SQL read
	// so the class cap counts only rows the caller can be shown (issue #632),
	// and re-checked in memory against the synthesized row so the two halves
	// of the merged response are consistently scoped (issue #701).
	//
	// Stale pending/notified executions are expired AFTER the response is
	// assembled so the GET handler is a pure read (issue #1032): the caller
	// sees current DB state and the next History load reflects the updated
	// status. On servers the transitions fire in a goroutine; on Lambda they
	// run synchronously before returning because the execution environment
	// freezes once the response is out (issue #1170). The sweep is deferred so
	// it also runs after the summary aggregate below: on Lambda a synchronous
	// sweep before it would make the cards count as "expired" the rows the
	// list still shows as pending (issue #675). Pages fetched with a cursor
	// do not list the actionable class, so they do not sweep it either.
	var extra []config.PurchaseHistoryRecord
	var staleExecs []config.PurchaseExecution
	actionableIncomplete := false
	if filters.Cursor == nil {
		extra, staleExecs, actionableIncomplete = h.fetchExecutionsAsHistory(ctx, filters, scope)
		extra = scope.filter(extra)
	}
	defer h.expireStaleExecutions(staleExecs)

	all := make([]config.PurchaseHistoryRecord, 0, len(page.rows)+len(extra))
	all = append(all, page.rows...)
	all = append(all, extra...)

	// Newest first across both sources so pending items don't get buried at
	// the bottom just because their synthetic records were appended last.
	// The stream rows are already in order and the sort is stable, so their
	// relative order (and the cursor's position) is unchanged.
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].Timestamp.After(all[j].Timestamp)
	})

	// The summary covers every row matching the filters and the caller's
	// account scope, not just this page (issue #675), so it is the same on
	// every page. It falls back to the page when the aggregate query fails,
	// and SummaryComplete says which.
	summary, summaryComplete := h.historySummary(ctx, filters, scope, all)

	// Truncated is a single boolean: a count would reveal other tenants' row
	// volume to a scoped user. has_more is judged on the rows the caller may
	// see, so it is never set by rows outside the caller's scope.
	return HistoryResponse{
		Summary:                   summary,
		SummaryComplete:           summaryComplete,
		Purchases:                 all,
		Truncated:                 page.nextCursor != "" || page.incomplete || actionableIncomplete,
		Limit:                     filters.Limit,
		ExecutionsLimit:           filters.Limit,
		ActionableExecutionsLimit: config.MaxListLimit,
		HasMore:                   page.nextCursor != "",
		NextCursor:                page.nextCursor,
	}, nil
}

// fetchExecutionsAsHistory returns the actionable PurchaseExecutions as
// synthetic PurchaseHistoryRecord entries so the /api/history response is a
// single flat list. Execution-level aggregates (TotalUpfrontCost /
// EstimatedSavings) become the row's cost fields; provider/service collapse
// to "multiple" because a single execution can span providers. The approver
// address is looked up from global config once and attached to pending/
// notified rows so the UI can tell the user exactly whose inbox holds the
// approval link. Stale pending/notified executions are expired after this
// call returns (see expireStaleExecutions) so the GET is a pure read:
// the response rows carry the pre-transition status and the next request
// sees the updated state. A listing error is logged and skipped — completed
// history must still render. Terminal executions are read by the paged
// stream instead (fetchHistoryPage).
//
// The filter set (issue #701) and the caller's account scope are pushed into
// the SQL read as a superset (issue #632), so the MaxListLimit cap counts only
// executions the caller could be shown and other tenants' or filtered-out rows
// cannot crowd a pending approval out. The exact checks still run in Go
// against the synthetic row: provider via the recs' providers, account via
// CloudAccountID, plan via PlanID, date via ScheduledDate here, and the scope
// in the caller.
//
// The third return value reports that actionable executions may be missing:
// the class reached its MaxListLimit cap, or its listing failed (issue #686).
func (h *Handler) fetchExecutionsAsHistory(ctx context.Context, filters historyFilters, scope historyScope) ([]config.PurchaseHistoryRecord, []config.PurchaseExecution, bool) {
	q := actionableExecutionsQuery(&filters, &scope)
	executions, ok := h.fetchExecutionsByClass(ctx, q)
	incomplete := !ok || len(executions) >= config.MaxListLimit
	staleExecs := h.staleExecutionsToSweep(ctx, &q, executions)
	if len(executions) == 0 {
		return nil, staleExecs, incomplete
	}
	approver := h.resolvePendingApproverEmail(ctx)
	userEmailCache := h.resolveUserEmails(ctx, executions)
	out := make([]config.PurchaseHistoryRecord, 0, len(executions))
	for i := range executions {
		exec := executions[i]
		if !filters.matchesExecution(exec) {
			continue
		}
		out = append(out, executionToHistoryRow(exec, approver, createdByEmail(userEmailCache, &exec)))
	}
	return out, staleExecs, incomplete
}

// fetchExecutionsByClass lists one status class. A listing error is logged and
// the class skipped (ok=false) so the other class and completed history still
// render; the caller flags the response as incomplete so the hidden rows are
// not silent (issue #686).
//
// q carries the request filters, plan_id included, and the caller's scope, so
// a plan view or a scoped caller is not crowded out by other rows under the
// limit (issues #632, #1404).
func (h *Handler) fetchExecutionsByClass(ctx context.Context, q config.ExecutionsByStatusesQuery) ([]config.PurchaseExecution, bool) {
	rows, err := h.config.GetExecutionsByStatusesFiltered(ctx, q)
	if err != nil {
		logging.Warnf("history: failed to load executions with statuses %v: %v", q.Statuses, err)
		return nil, false
	}
	return rows, true
}

// resolveUserEmails builds a map of user-ID to email by calling GetUser once
// per unique non-nil CreatedByUserID found in the execution list. Lookup
// failures are logged and skipped — a missing email degrades gracefully (the
// UI falls back to the raw UUID via created_by_user_id). Called once per
// /api/history request so the cost is proportional to the number of distinct
// creators, not the number of execution rows.
func (h *Handler) resolveUserEmails(ctx context.Context, executions []config.PurchaseExecution) map[string]string {
	seen := make(map[string]struct{})
	for i := range executions {
		exec := executions[i]
		if exec.CreatedByUserID != nil && *exec.CreatedByUserID != "" {
			seen[*exec.CreatedByUserID] = struct{}{}
		}
	}
	out := make(map[string]string, len(seen))
	for uid := range seen {
		user, err := h.auth.GetUser(ctx, uid)
		if err != nil {
			logging.Warnf("history: failed to resolve email for user %s: %v", uid, err)
			continue
		}
		out[uid] = user.Email
	}
	return out
}

// resolvePendingApproverEmail returns the notification email the approval
// link was sent to (or would have been, if SES failed). Single-tenant
// deployments share one value across every pending row, so this is looked up
// once per /api/history call. A lookup error or an empty address returns a
// sentinel so the UI still renders the pending row — just without an
// addressee.
func (h *Handler) resolvePendingApproverEmail(ctx context.Context) string {
	globalCfg, cfgErr := h.config.GetGlobalConfig(ctx)
	if cfgErr != nil {
		logging.Warnf("history: failed to resolve approver email for pending rows: %v", cfgErr)
		return "(notification email not set)"
	}
	if globalCfg.NotificationEmail == nil || *globalCfg.NotificationEmail == "" {
		return "(notification email not set)"
	}
	return *globalCfg.NotificationEmail
}

// accountMatchesFilters pulls the dual-id account-match logic out of
// matchesExecution to keep it under the cyclomatic limit.
//
// AccountIDs are cloud_accounts UUIDs; externalIDsByProvider are the
// cloud-provider external account numbers resolved from them, grouped by
// provider. An execution's effective account ID may be EITHER representation:
// exec.CloudAccountID and the per-rec CloudAccountID hold the UUID for
// UUID-attributed executions, while a legacy/ambient execution may only carry
// the external number. Match against both so an external-id-only pending row is
// not dropped (the mirror of the SQL dual-column predicate, issue #701/#498).
//
// The external-id match is provider-scoped: an external number matches only when
// it is listed under the execution's own provider, or under the "" key (unknown
// provider, legacy behavior). This mirrors the SQL (provider = $p AND
// account_id = ANY(...)) and keeps a reused external number across providers
// (aws/123 vs azure/123) from matching the wrong execution.
//
// Uses the same two-level account resolution as executionToHistoryRow:
// exec.CloudAccountID first, then the rec-level fallback, so web bulk-purchase
// executions (exec.CloudAccountID == nil) are not silently dropped.
func accountMatchesFilters(exec config.PurchaseExecution, accountIDs []string, externalIDsByProvider map[string][]string) bool {
	var accountID string
	if exec.CloudAccountID != nil {
		accountID = *exec.CloudAccountID
	} else {
		accountID = collapseRecommendationAccount(exec.Recommendations)
	}
	if accountID == "" {
		return false
	}
	if stringInSlice(accountID, accountIDs) {
		return true
	}
	// External-id half: only the execution's own provider group (plus the ""
	// unknown-provider group) may match, so a reused external number across
	// providers can't leak.
	provider := executionProvider(exec)
	if provider != "" && stringInSlice(accountID, externalIDsByProvider[provider]) {
		return true
	}
	return stringInSlice(accountID, externalIDsByProvider[""])
}

// executionProvider returns the execution's provider, taken from its first
// recommendation (all recs in an execution share a provider in practice).
// Returns "" when no recommendation carries one.
func executionProvider(exec config.PurchaseExecution) string {
	for i := range exec.Recommendations {
		r := exec.Recommendations[i]
		if r.Provider != "" {
			return r.Provider
		}
	}
	return ""
}

// executionHasProvider reports whether any of the execution's
// recommendations carries the given provider value.
func executionHasProvider(exec config.PurchaseExecution, provider string) bool {
	for i := range exec.Recommendations {
		r := exec.Recommendations[i]
		if r.Provider == provider {
			return true
		}
	}
	return false
}

// stringInSlice is a tiny linear-search helper for the per-execution
// account filter. The slice is bounded by MaxAccountIDsPerRequest (200) so
// a linear scan is comfortably cheaper than the map allocation a set would
// require, and the call is hot only for executions (DB rows go through SQL).
func stringInSlice(needle string, haystack []string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// fetchPurchaseHistory reads purchases from the store, applying the parsed
// filter set. The store-level filtered method (issue #701) pushes provider /
// account / timestamp range into SQL. The account predicate is dual-column
// (cloud_account_id = ANY(AccountIDs) OR (provider = $p AND account_id =
// ANY(ExternalIDsByProvider[p]))) so a row that carries only one of the two
// account representations is still matched — the original #701/#498/#866 bug was
// that filtering on the sparse cloud_account_id UUID column alone dropped every
// direct-execute/ambient row. Grouping the external ids by provider keeps a
// reused external number across providers from leaking the wrong rows.
//
// It reads one keyset batch of the History stream: up to limit rows after
// `after` (nil starts at the newest row), in stream order (issue #674). An
// empty filter set selects every row. The legacy singular account_id
// (LegacyAccountID) is resolved to the same dual-column inputs by the caller
// (getHistory) and arrives here folded into AccountIDs/ExternalIDsByProvider,
// so there is no longer a separate single-column legacy path to coerce
// identifiers through.
func (h *Handler) fetchPurchaseHistory(ctx context.Context, filters *historyFilters, scope *historyScope, after *config.HistoryPageKey, limit int) ([]config.PurchaseHistoryPageRow, error) {
	filter := filters.purchaseHistoryFilter()
	filter.Limit = limit
	// The scope is a superset of historyScope.allows, which still runs on the
	// batch; the summary aggregate groups by account and applies the scope in
	// Go, so only this read carries it (issue #632).
	filter.Scope = scope.executionScope()
	return h.config.ListPurchaseHistoryPage(ctx, filter, after)
}

// purchaseHistoryFilter maps the parsed filters onto the store filter. Both the
// History row fetch and the summary aggregate use it, so they hand the store
// the same predicate inputs. An empty historyFilters maps to an empty store
// filter, which selects every row.
func (f historyFilters) purchaseHistoryFilter() config.PurchaseHistoryFilter {
	out := config.PurchaseHistoryFilter{
		Provider:              f.Provider,
		PlanID:                f.PlanID,
		AccountIDs:            f.AccountIDs,
		ExternalIDsByProvider: f.ExternalIDsByProvider,
		Limit:                 f.Limit,
	}
	if f.HasDate {
		s, e := f.Start, f.End
		out.Start = &s
		out.End = &e
	}
	return out
}

// resolveHistoryAccountFilter populates filters.AccountIDs /
// filters.ExternalIDsByProvider from the parsed account params so the SQL and
// in-memory paths share one dual-column account set. The plural `account_ids`
// (UUIDs) are resolved to their per-provider external numbers via
// resolveAccountFilterIDs. The singular legacy `account_id` is folded in too: it
// is a top-bar chip UUID for current callers (resolved to UUID + external) or a
// raw external number for pre-UUID callers (grouped under the "" provider key).
// Best-effort: resolution failures leave the UUID-only set in place (no worse
// than the pre-fix behavior), and the per-record allowed_accounts filter still
// enforces scoping downstream.
func (h *Handler) resolveHistoryAccountFilter(ctx context.Context, filters *historyFilters) {
	uuids, externalsByProvider := h.resolveAccountFilterIDs(ctx, filters.AccountIDs)

	if filters.LegacyAccountID != "" {
		lUUIDs, lExternalsByProvider := h.resolveSingleAccountFilterIDs(ctx, filters.LegacyAccountID)
		uuids = appendMissing(uuids, lUUIDs...)
		for provider, exts := range lExternalsByProvider {
			for _, ext := range exts {
				externalsByProvider = addExternalIDForProvider(externalsByProvider, provider, ext)
			}
		}
	}

	filters.AccountIDs = uuids
	filters.ExternalIDsByProvider = externalsByProvider
}

// appendMissing appends each value to dst only if not already present,
// preserving order. Used to merge the legacy single-account ids into the
// plural sets without introducing duplicate ANY() bind values.
func appendMissing(dst []string, vals ...string) []string {
	for _, v := range vals {
		if v == "" || stringInSlice(v, dst) {
			continue
		}
		dst = append(dst, v)
	}
	return dst
}

// filterPurchaseHistoryByAllowedAccounts drops records whose AccountID/Name
// is outside the session's allowed_accounts. Admin/unrestricted sessions pass
// through unchanged.
//
// Rows with an empty AccountID are exempt from the drop for scoped users
// only when the requesting user created the row (issue #1032, regression of
// #621). An empty AccountID means the execution was ambient
// (exec.CloudAccountID == nil) AND its recommendations carry no common
// account. These are in-flight financial actions the user owns that cannot be
// attributed to a specific cloud account; dropping them silently re-introduces
// the #621 disappearance bug. The exemption is now gated on ownership so that
// another user's multi-account in-flight row (including CreatedByUserEmail PII
// and dollar amounts) is not visible to unrelated scoped users.
func (h *Handler) filterPurchaseHistoryByAllowedAccounts(ctx context.Context, session *Session, purchases []config.PurchaseHistoryRecord) ([]config.PurchaseHistoryRecord, error) {
	scope, err := h.loadHistoryScope(ctx, session)
	if err != nil {
		return nil, err
	}
	return scope.filter(purchases), nil
}

// historyRowAccount returns the registered account a history row belongs to,
// or nil when it cannot be determined unambiguously. Without CloudAccountID it
// tries the account UUID first (in-flight rows store the UUID in AccountID),
// then provider plus external id, since external ids are unique only per
// provider. A legacy row without a provider matches only when exactly one
// account across all providers has that external id.
func historyRowAccount(accounts []config.CloudAccount, p config.PurchaseHistoryRecord) *config.CloudAccount {
	if p.CloudAccountID != nil {
		return accountByID(accounts, *p.CloudAccountID)
	}
	if a := accountByID(accounts, p.AccountID); a != nil {
		return a
	}
	if p.Provider != "" {
		return uniqueAccountByExternalID(accounts, p.Provider, p.AccountID)
	}
	var found *config.CloudAccount
	for i := range accounts {
		if accounts[i].ExternalID != p.AccountID {
			continue
		}
		if found != nil {
			return nil
		}
		found = &accounts[i]
	}
	return found
}

func accountByID(accounts []config.CloudAccount, id string) *config.CloudAccount {
	for i := range accounts {
		if accounts[i].ID == id {
			return &accounts[i]
		}
	}
	return nil
}

// historyRowAccountName returns the registered account name for a history row,
// or "" when it cannot be determined unambiguously (see historyRowAccount).
func historyRowAccountName(accounts []config.CloudAccount, p config.PurchaseHistoryRecord) string {
	if a := historyRowAccount(accounts, p); a != nil {
		return a.Name
	}
	return ""
}
