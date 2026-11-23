package api

import (
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
