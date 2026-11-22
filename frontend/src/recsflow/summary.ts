import * as state from '../state';
import { formatCurrency, NO_VALUE } from '../utils';
import type { LocalRecommendation, RecommendationsSummary } from '../types';
import { renderKpiTiles } from '../lib/kpi-tile';
import { recsState } from './recsState';
import { scaleCost, formatCostForPeriod, periodSuffix, formatScaledRange } from './money';
import { pageLevelRange, formatSavingsRange, formatPaybackRange } from './ranges';
import { columnLabel } from './columnDefs';
import { categoricalDisplayLabel } from './cellValues';
import { groupRecsByCell } from './groups';

/**
 * #180: the savings card's scope line, or null when the card covers every
 * recommendation (the same scope as Home). "Filtered" is decided by whether a
 * column filter is active or the fetch itself was narrowed server-side, not by
 * comparing counts: a pushed-down service/region filter returns only matching
 * rows, so the visible and loaded counts are equal. In that case the
 * unfiltered total isn't known, so the line names the server-side filter
 * instead of an "of Y" figure.
 */
function savingsScopeText(visibleCount: number, loadedCount: number): string | null {
  const active = Object.keys(state.getRecommendationsColumnFilters());
  if (recsState.loadedServerFilters.length > 0) {
    const pushed = recsState.loadedServerFilters.map((f) => `${columnLabel(f.column)}: ${categoricalDisplayLabel(f.column, f.value)}`);
    const others = active.filter((col) => !recsState.loadedServerFilters.some((f) => f.column === col)).length;
    if (others > 0) pushed.push(`+${others} more ${others === 1 ? 'filter' : 'filters'}`);
    return `Filtered: ${visibleCount} recommendations (${pushed.join(', ')})`;
  }
  if (active.length === 0 && visibleCount >= loadedCount) return null;
  return `Filtered: ${visibleCount} of ${loadedCount} recommendations`;
}

export function renderRecommendationsSummary(
  _summary: RecommendationsSummary | null,
  recommendations: readonly LocalRecommendation[],
  loadedCount: number = recommendations.length,
): void {
  const container = document.getElementById('recommendations-summary');
  if (!container) return;

  // All four summary cards (Total Recommendations, Potential Monthly
  // Savings, Total Upfront Cost, Payback Period) recompute client-side
  // from the same source on every list rerender (closes #279). The API-
  // derived `summary` is no longer read — it summed every (term, payment)
  // variant of every cell and overstated achievable totals by ~6× on a
  // typical fan-out, the same bug class #272 closed for the savings card
  // alone. Now extended to the full header.
  //
  // Selection narrowing: when the user has ticked ≥1 checkbox visible in
  // the table, the cards narrow to `selected ∩ visible`. Cleared selection
  // → cards snap back to the full visible set. The cards therefore reflect
  // exactly what a Purchase / Plan click would commit to.
  //
  // The unused `_summary` arg is retained so the call sites in
  // loadRecommendations / renderRecommendationsList don't have to change
  // shape; it can be removed when the field is also dropped from
  // RecommendationsSummary.
  const selected = state.getSelectedRecommendationIDs();
  // Narrow selection to visible rows only — if the user has ticked rows that
  // are currently hidden by a column filter, those rows must not drive the
  // summary cards (the cards should reflect what the user can actually see
  // and act on). An empty visible-intersection means no effective selection.
  const selectedVisible = recommendations.filter((r) => selected.has(r.id));
  const target: readonly LocalRecommendation[] = selectedVisible.length > 0
    ? selectedVisible
    : recommendations;
  const groups = groupRecsByCell(target);
  const plr = pageLevelRange(groups);
  const isSelectionView = selectedVisible.length > 0 && plr.cellCount > 0;

  // issue #319: scale savings by the active cost period.
  // M-1: use ?? null so absent savings propagate to NO_VALUE via formatCostForPeriod
  // rather than silently collapsing to $0 (scaleCost returns null only when input
  // is null/undefined; a genuine $0 saving returns 0, not null).
  const period = state.getCostPeriod();
  const scaledSavingsMin = scaleCost(plr.savingsMin, period) ?? null;
  const scaledSavingsMax = scaleCost(plr.savingsMax, period) ?? null;
  // Use target.length (variant count) for the guard conditions so they
  // stay consistent with the new KPI value below (closes #748).
  const hasRecs = target.length > 0;
  const savingsText = hasRecs && plr.savingsMax > 0 && scaledSavingsMin !== null && scaledSavingsMax !== null
    ? formatScaledRange(scaledSavingsMin, scaledSavingsMax, period)
    : formatCostForPeriod(0, period);
  const upfrontText = hasRecs && plr.upfrontMax > 0
    ? formatSavingsRange(plr.upfrontMin, plr.upfrontMax)
    : formatCurrency(0);
  // L-2: use NO_VALUE for absent/inapplicable payback rather than '0 months'
  // which falsely implies instantaneous payback.
  const paybackText = hasRecs && plr.paybackMonthsMax > 0
    ? formatPaybackRange(plr.paybackMonthsMin, plr.paybackMonthsMax)
    : NO_VALUE;
  // issue #748: count VARIANTS (= target.length), not cells (= plr.cellCount).
  // "Showing X of X" in the filter-status bar also counts variants, so both
  // numbers now agree on the same dataset.
  const variantCount = target.length;
  const countLabel = isSelectionView ? 'Selected Recommendations' : 'Total Recommendations';
  const savingsLabel = (() => {
    if (period === 'monthly') {
      return isSelectionView ? 'Selected Monthly Savings' : 'Potential Monthly Savings';
    }
    const sfx = periodSuffix(period);
    return isSelectionView ? `Selected Savings ${sfx}` : `Potential Savings ${sfx}`;
  })();
  // #180: Home's Potential Monthly Savings always covers every
  // recommendation, so when column filters narrow this page the card must say
  // so; otherwise the two same-named KPIs disagree with no visible reason.
  const scopeText = isSelectionView ? null : savingsScopeText(variantCount, loadedCount);
  const upfrontLabel = isSelectionView ? 'Selected Upfront Cost' : 'Total Upfront Cost';
  const paybackLabel = 'Payback Period';

  // Issue #743: the same .kpi-tile component as Home's summary, so the two
  // pages' KPI cards match. The #180 scope line is the savings tile's detail.
  renderKpiTiles(container, [
    { kpi: 'count', title: countLabel, value: String(variantCount) },
    {
      kpi: 'savings', title: savingsLabel, value: savingsText, valueSavings: true,
      ...(scopeText ? { detail: scopeText } : {}),
    },
    { kpi: 'upfront', title: upfrontLabel, value: upfrontText },
    { kpi: 'payback', title: paybackLabel, value: paybackText },
  ]);
  const scopeDetail = container.querySelector<HTMLElement>('[data-kpi="savings"] .kpi-tile-detail');
  if (scopeDetail) scopeDetail.dataset['testid'] = 'savings-scope';
}
