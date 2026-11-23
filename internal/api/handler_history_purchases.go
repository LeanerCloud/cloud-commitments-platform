package api

import (
	"context"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

func summarizePurchaseHistory(purchases []config.PurchaseHistoryRecord) HistorySummary {
	summary := HistorySummary{TotalUpfront: new(float64)}
	for i := range purchases {
		addHistoryRowToSummary(&summary, &purchases[i], 1)
	}
	summary.TotalAnnualSavings = summary.TotalMonthlySavings * 12
	return summary
}

// addHistoryRowToSummary adds n rows shaped like p to the summary. The row
// list (n == 1 per row) and the SQL aggregate (one call per group, with p
// carrying the group's dollar sums) both go through it, so the totals use one
// set of bucketing rules.
func addHistoryRowToSummary(summary *HistorySummary, p *config.PurchaseHistoryRecord, n int) {
	summary.TotalPurchases += n
	// Revoked commitments are excluded from dollar KPIs regardless of their
	// status: the provider has cancelled the commitment, so UpfrontCost and
	// EstimatedSavings no longer represent active spend or realized savings.
	// They count toward TotalPurchases (for auditability) and TotalRevoked
	// (so the UI can surface the count), but not TotalCompleted.
	// GetActivePurchaseHistory already excludes revoked rows via SQL; this
	// guard handles the GetPurchaseHistoryFiltered path used by History.
	if p.RevokedAt != nil {
		summary.TotalRevoked += n
		return
	}
	if addNonCompletedRowToSummary(summary, p.Status, n) {
		return
	}
	summary.TotalCompleted += n
	// Audit-gap completed rows (issue #621) are synthesized execution rows
	// whose purchase_history write failed. Count them as completed (the
	// money WAS committed and they must stay visible) but exclude their
	// execution-level dollars: a partially-saved multi-rec execution can
	// have BOTH some purchase_history rows AND this synthesized row, and
	// adding the full execution total here would double-count the recs that
	// did save. The dollars are surfaced via the individual purchase_history
	// rows that succeeded; the synthesized row is the audit flag, not a
	// money source. IsAuditGap is the explicit marker: real purchase_history
	// rows loaded from the DB always leave it false, so a future change that
	// annotates completed DB rows can't silently drop them from the totals.
	if p.IsAuditGap {
		return
	}
	// Legacy-estimate rows (issue #669) are completed purchases whose dollar
	// amounts were estimated, so they add no dollars. The SQL aggregate
	// FILTERs the same rows out of its sums.
	if p.IsLegacyEstimate() {
		return
	}
	summary.TotalUpfront = sumKnownCosts(summary.TotalUpfront, p.UpfrontCost)
	summary.TotalMonthlySavings += p.EstimatedSavings
}

// addNonCompletedRowToSummary counts n rows in their non-completed bucket and
// reports whether status was one. Non-completed rows count toward
// TotalPurchases and their specific bucket (pending / in-progress / failed /
// expired / canceled) but are excluded from the dollar totals: the money
// hasn't been committed for any of those states. "completed" and unset
// (legacy DB rows that pre-date the status field) both count as completed.
func addNonCompletedRowToSummary(summary *HistorySummary, status string, n int) bool {
	switch status {
	case "pending", "notified", "scheduled":
		// "scheduled" (issue #239) is a pre-purchase state like
		// pending/notified (the cloud SDK has not been called yet;
		// scheduled is revoked, not canceled). It must not
		// fall through to TotalCompleted, or the
		// "Total Upfront Spent" card counts money not yet spent.
		summary.TotalPending += n
	case "approved", "running", "paused":
		// In-flight (issue #621). Not yet confirmed final — keep out of the
		// committed dollar totals so an interrupted approval can't inflate
		// reported spend/savings.
		summary.TotalInProgress += n
	case "failed":
		summary.TotalFailed += n
	case "expired":
		summary.TotalExpired += n
	case config.StatusCanceled:
		// A canceled purchase represents zero committed spend and zero
		// realized savings (issue #736). Exclude from all dollar KPIs and
		// from TotalCompleted -- the money was never committed.
	default:
		return false
	}
	return true
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
