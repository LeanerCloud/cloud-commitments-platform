package api

import (
	"context"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// commitmentExpiry returns the moment a purchase's commitment term ends.
// Computed from Timestamp + Term*365d. Extracted from
// calculateCommitmentMetrics so the inventory handler can surface the
// same date in its per-row response without re-deriving the math (and
// so a future tweak to "what counts as expired" lands in exactly one
// place). One year is approximated as 365 days — matches the original
// dashboard arithmetic verbatim; leap-year precision isn't material for
// a multi-year RI/SP/CUD term.
func commitmentExpiry(p config.PurchaseHistoryRecord) time.Time {
	termDuration := time.Duration(p.Term) * 365 * 24 * time.Hour
	return p.Timestamp.Add(termDuration)
}

// isActiveCommitment reports whether the purchase is active: its term has not
// yet expired as of `now`, its status is one of the successful terminal states
// ("" for DB-backed rows where the column is unpersisted, or "completed"), and
// it has not been revoked. Rows synthesized from failed/canceled/expired
// executions carry a non-empty status other than "completed" and are excluded
// so they do not inflate the committed_monthly KPI. The boundary is strict
// (After): a commitment is active right up to the instant its term ends.
//
// Same predicate shared by the dashboard aggregate and the per-commitment
// inventory endpoint. Status values: see PurchaseHistoryRecord.Status doc.
func isActiveCommitment(p config.PurchaseHistoryRecord, now time.Time) bool {
	// Revoked commitments are never active regardless of status or term.
	// GetActivePurchaseHistory already excludes them in SQL; this is a
	// defense-in-depth guard for any caller that passes rows from a different
	// query (e.g. GetPurchaseHistoryFiltered).
	if p.RevokedAt != nil {
		return false
	}
	// Status is unpersisted (dynamodbav:"-"); DB rows always read back as "".
	// Synthesized rows set it to "failed", "expired", "canceled", "pending",
	// "notified", "approved", "running", or "paused". Only "" and "completed"
	// represent a commitment that is actually live on the provider.
	if p.Status != "" && p.Status != "completed" {
		return false
	}
	return !now.After(commitmentExpiry(p))
}

// aggregateActiveCommitmentsPerService sums EstimatedSavings of active
// purchase history rows, grouped by their Service field. It applies the
// shared isActiveCommitment gate (term not expired AND a successful status)
// so both the KPI total and the per-service chart breakdowns use exactly the
// same "active" definition. Legacy-estimate rows (issue #669) stay active but
// add no dollars: committed_monthly, the per-service current_savings and the
// coverage derived from them all skip them.
func aggregateActiveCommitmentsPerService(purchases []config.PurchaseHistoryRecord, now time.Time) map[string]float64 {
	byService := make(map[string]float64)
	for i := range purchases {
		p := purchases[i]
		if !isActiveCommitment(p, now) || p.IsLegacyEstimate() {
			continue
		}
		byService[p.Service] += p.EstimatedSavings
	}
	return byService
}

// fetchCommitmentPurchases loads the active purchase_history rows that
// calculateCommitmentMetrics aggregates, applying the pre-resolved account
// scope and an optional provider filter. Extracted to keep the parent under the
// cyclomatic limit (mirrors the appendAccountPredicate / accountMatchesFilters
// pattern). Returns (nil, nil) for an explicit zero-account scope, so the
// caller emits genuinely-zero KPIs without querying, and a wrapped error on a
// store failure (issue #72: a failed read must never be reported as $0).
//
//   - non-nil but empty accountUUIDs (no external groups): explicit "scoped to
//     zero accounts" sentinel (a restricted session whose allowed_accounts match
//     nothing). Returns (nil, nil) WITHOUT querying so a scoped user never sees
//     all-accounts data (issue #956). A nil/empty filter sent to the store would
//     match every row (no WHERE clause), so this must short-circuit.
//   - otherwise: GetActivePurchaseHistory with the dual-column account predicate
//     (empty scope means all accounts). The active filter runs in SQL with no
//     row cap, so older-but-still-active 1y/3y commitments cannot be silently
//     dropped the way a newest-first LIMIT 1000 page dropped them (issue #1140);
//     the result is bounded by the number of live commitments.
//   - provider: when non-empty, only purchases for that provider are returned,
//     mirroring fetchCommitmentRecords' in-memory provider filter so KPIs match
//     the provider chip selection.
func (h *Handler) fetchCommitmentPurchases(ctx context.Context, asOf time.Time, provider string, accountUUIDs []string, accountExternalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
	if accountUUIDs != nil && len(accountUUIDs) == 0 && len(accountExternalIDsByProvider) == 0 {
		return nil, nil
	}

	purchases, err := h.config.GetActivePurchaseHistory(ctx, asOf, accountUUIDs, accountExternalIDsByProvider)
	if err != nil {
		return nil, fmt.Errorf("failed to get active purchase history: %w", err)
	}

	if provider != "" {
		filtered := purchases[:0]
		for i := range purchases {
			if purchases[i].Provider == provider {
				filtered = append(filtered, purchases[i])
			}
		}
		purchases = filtered
	}

	return purchases, nil
}

// commitmentCounts holds the commitment tallies of calculateCommitmentMetrics:
// Active counts every active commitment, ExcludedLegacy the legacy-estimate
// subset whose dollars are left out of the amounts (issue #669).
type commitmentCounts struct {
	Active, ExcludedLegacy int
}
