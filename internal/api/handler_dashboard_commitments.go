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

// calculateCommitmentMetrics aggregates active-commitment counts and monthly
// savings from purchase history. The account scope arrives pre-resolved as the
// dual-column filter inputs (see resolveDashboardAccountScope); the
// scope-to-query mapping (including the zero-account short-circuit for issue
// #956) lives in fetchCommitmentPurchases.
//
// provider, when non-empty, restricts the result set to purchases for that
// cloud provider so the KPIs match the provider chip selection. This mirrors
// the recommendations half which passes the same provider param to
// ListRecommendations.
//
// EstimatedSavings on purchase_history rows is always written in monthly units
// (populated from PurchaseExecution.EstimatedSavings which derives from
// recommendation monthly savings at purchase time, see SavePurchaseHistory
// and the purchase manager). No unit normalisation is needed.
//
// The fourth return value is the per-service breakdown of active
// EstimatedSavings, derived from aggregateActiveCommitmentsPerService so both
// this KPI path (committedMonthly) and the per-service chart use exactly the
// same gate.
//
// counts.ExcludedLegacy counts the active commitments whose amounts are legacy
// estimates (issue #669): they are in activeCommitments but add nothing to
// committedMonthly, savingsByService or ytdSavings. It is counted in the same
// loop, under the same isActiveCommitment gate and provider/account scope.
//
// A store error is returned, never reported as zeroed metrics (issue #72).
func (h *Handler) calculateCommitmentMetrics(ctx context.Context, provider string, accountUUIDs []string, accountExternalIDsByProvider map[string][]string) (counts commitmentCounts, committedMonthly, ytdSavings float64, savingsByService map[string]float64, err error) {
	currentTime := time.Now()
	purchases, err := h.fetchCommitmentPurchases(ctx, currentTime, provider, accountUUIDs, accountExternalIDsByProvider)
	if err != nil {
		return commitmentCounts{}, 0, 0, nil, err
	}

	yearStart := time.Date(currentTime.Year(), 1, 1, 0, 0, 0, 0, time.UTC)

	// Derive the per-service breakdown from the shared primitive so the
	// active-commitment gate is applied consistently everywhere.
	savingsByService = aggregateActiveCommitmentsPerService(purchases, currentTime)
	for _, v := range savingsByService {
		committedMonthly += v
	}

	for i := range purchases {
		p := purchases[i]
		if !isActiveCommitment(p, currentTime) {
			continue
		}

		counts.Active++
		if p.IsLegacyEstimate() {
			counts.ExcludedLegacy++
			continue
		}

		// committedMonthly is derived from aggregateActiveCommitmentsPerService
		// above (same gate), so it is intentionally NOT summed here to avoid
		// double-counting. EstimatedSavings is in monthly units (see doc above).
		//
		// YTD savings: count whole calendar months from year start or from the
		// purchase date, whichever is later. Use AddDate-stepping rather than
		// dividing by a 30-day constant so January (31 days) and February (28/29
		// days) are handled correctly and the result never truncates a partial
		// month that spans a 29-30-31-day boundary.
		countFrom := yearStart
		if p.Timestamp.After(yearStart) {
			countFrom = p.Timestamp
		}
		ytdSavings += p.EstimatedSavings * float64(elapsedWholeMonths(countFrom, currentTime))
	}

	return counts, committedMonthly, ytdSavings, savingsByService, nil
}

// elapsedWholeMonths returns the number of complete calendar months between
// from and to, counted by AddDate-stepping rather than dividing by a fixed
// 30-day constant. This avoids truncation errors for months of varying length
// (e.g. a 28-day gap in February scores 0 under the 30-day divisor, 1 here).
// Returns 0 when to is before or equal to from.
func elapsedWholeMonths(from, to time.Time) int {
	if !to.After(from) {
		return 0
	}
	months := 0
	for from.AddDate(0, months+1, 0).Before(to) || from.AddDate(0, months+1, 0).Equal(to) {
		months++
	}
	return months
}

// calculateCurrentCoverage returns committed / (committed + potential) as a
// percentage, or nil when both are zero and there is nothing to measure.
//
// It used to return a hard-coded 100% whenever potentialSavings was 0, even
// with nothing committed, so an empty or zeroed commitment half rendered as a
// reassuring "100% coverage, $0 committed" (issue #72). Committed savings with
// no remaining recommendations still compute to 100% through the ratio; only
// the 0/0 case is now reported as unknown (JSON null) rather than invented.
func (h *Handler) calculateCurrentCoverage(potentialSavings, committedMonthly float64) *float64 {
	totalPossible := potentialSavings + committedMonthly
	if totalPossible == 0 {
		return nil
	}

	coverage := (committedMonthly / totalPossible) * 100
	return &coverage
}
