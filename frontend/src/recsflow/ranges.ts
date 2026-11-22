import { formatCurrency, NO_VALUE } from '../utils';
import type { LocalRecommendation } from '../types';
import { formatTermShort } from '../lib/table-format';

/** Summary metrics for a single cell's variants. Exported for tests. */
export interface CellRangeSummary {
  savingsMin: number;
  savingsMax: number;
  upfrontMin: number;
  upfrontMax: number;
  termMin: number;
  termMax: number;
}

/**
 * Compute the min/max envelope across all variants of a cell.
 * Returns zeroed summary for an empty array (defensive; should not occur).
 * Exported for tests.
 */
export function cellSummary(variants: readonly LocalRecommendation[]): CellRangeSummary {
  if (variants.length === 0) {
    return { savingsMin: 0, savingsMax: 0, upfrontMin: 0, upfrontMax: 0, termMin: 0, termMax: 0 };
  }
  let savingsMin = variants[0]!.savings;
  let savingsMax = variants[0]!.savings;
  let upfrontMin = variants[0]!.upfront_cost;
  let upfrontMax = variants[0]!.upfront_cost;
  let termMin = variants[0]!.term;
  let termMax = variants[0]!.term;
  for (let i = 1; i < variants.length; i++) {
    const v = variants[i]!;
    if (v.savings < savingsMin) savingsMin = v.savings;
    if (v.savings > savingsMax) savingsMax = v.savings;
    if (v.upfront_cost < upfrontMin) upfrontMin = v.upfront_cost;
    if (v.upfront_cost > upfrontMax) upfrontMax = v.upfront_cost;
    if (v.term < termMin) termMin = v.term;
    if (v.term > termMax) termMax = v.term;
  }
  return { savingsMin, savingsMax, upfrontMin, upfrontMax, termMin, termMax };
}

/** Page-level totals for the summary header (closes #279). All ranges are
 * the cell-by-cell min/max sums — the user buys at most one variant per
 * cell, so the achievable totals are bounded by `sum(cell.{min,max})` not
 * by `sum(every variant)`. */
export interface PageLevelRange {
  savingsMin: number;
  savingsMax: number;
  upfrontMin: number;
  upfrontMax: number;
  /** payback months at the min-savings end of the range; clamped to 0
   * when both savings and upfront are 0 (nothing to pay back). */
  paybackMonthsMin: number;
  /** payback months at the max-savings end. */
  paybackMonthsMax: number;
  cellCount: number;
}

/**
 * Compute the page-level summary by summing per-cell min/max envelopes.
 * Used by both the summary cards and the (now-removed) banner; lives at
 * module scope so dashboard.ts can reuse it for the cross-page parity in
 * #279.
 *
 *   savingsMin       = sum of cellSummary.savingsMin
 *   savingsMax       = sum of cellSummary.savingsMax
 *   upfrontMin       = sum of cellSummary.upfrontMin (each cell's lowest
 *                      upfront, which is NOT paired with the variant that
 *                      gave savingsMin)
 *   upfrontMax       = sum of cellSummary.upfrontMax (each cell's highest
 *                      upfront, not paired with the savingsMax variant)
 *   paybackMonthsMin = sum of per-cell best-payback variant upfronts /
 *                      sum of their savings.  Each cell independently
 *                      picks the variant with the lowest upfront/savings
 *                      ratio. Using independent cross-extrema
 *                      (upfrontMin / savingsMax) is NOT attainable when a
 *                      cell's lowest-upfront variant ≠ its highest-savings
 *                      variant — it would imply buying one impossible
 *                      combination.  Full convolution (trying every
 *                      cross-cell combination) is O(variants^cells) and
 *                      impractical for pages with 30+ cells.  Per-cell
 *                      paired choice is O(cells × variants) and produces
 *                      bounds that match what a min-payback / max-payback
 *                      per-cell selection would actually commit to.
 *   paybackMonthsMax = same logic, each cell picks the worst-payback
 *                      variant.
 *
 * Exported for tests.
 */
export function pageLevelRange(groups: Map<string, LocalRecommendation[]>): PageLevelRange {
  let savingsMin = 0;
  let savingsMax = 0;
  let upfrontMin = 0;
  let upfrontMax = 0;
  // Payback accumulators: per-cell paired variant sums.
  let paybackBestUpfront = 0;
  let paybackBestSavings = 0;
  let paybackWorstUpfront = 0;
  let paybackWorstSavings = 0;
  for (const variants of groups.values()) {
    const s = cellSummary(variants);
    savingsMin += s.savingsMin;
    savingsMax += s.savingsMax;
    upfrontMin += s.upfrontMin;
    upfrontMax += s.upfrontMax;

    // For payback: pick the variant with the best (lowest) payback ratio for
    // the min-end, and the worst (highest) for the max-end. Treat savings ≤ 0
    // as Infinity payback so zero-savings variants sort to the worst end.
    let bestRatio = Infinity;
    let bestUpfront = 0;
    let bestSavings = 0;
    let worstRatio = -Infinity;
    let worstUpfront = 0;
    let worstSavings = 0;
    for (const v of variants) {
      const ratio = v.savings > 0 ? v.upfront_cost / v.savings : Infinity;
      if (ratio < bestRatio) {
        bestRatio = ratio;
        bestUpfront = v.upfront_cost;
        bestSavings = v.savings;
      }
      if (ratio > worstRatio) {
        worstRatio = ratio;
        worstUpfront = v.upfront_cost;
        worstSavings = v.savings;
      }
    }
    paybackBestUpfront += bestUpfront;
    paybackBestSavings += bestSavings;
    paybackWorstUpfront += worstUpfront;
    paybackWorstSavings += worstSavings;
  }
  // Clamp to 0 when total savings is non-positive to avoid Infinity / NaN.
  const paybackMonthsMin = paybackBestSavings > 0 ? paybackBestUpfront / paybackBestSavings : 0;
  const paybackMonthsMax = paybackWorstSavings > 0 ? paybackWorstUpfront / paybackWorstSavings : 0;
  return {
    savingsMin, savingsMax,
    upfrontMin, upfrontMax,
    paybackMonthsMin, paybackMonthsMax,
    cellCount: groups.size,
  };
}

/** Format a savings range, collapsing "$X – $X" to "$X". Exported for use in dashboard.ts. */
export function formatSavingsRange(min: number, max: number): string {
  const lo = formatCurrency(min);
  const hi = formatCurrency(max);
  return lo === hi ? lo : `${lo} – ${hi}`;
}

/** Format a payback-months range, collapsing identical endpoints to a
 * single value and rounding to 1 decimal. Used by the Payback Period
 * summary card after #279 broadened it to a range. A 0 lower bound is a
 * no-upfront choice, which has no payback (shown as NO_VALUE on its own),
 * so the range reads "Up to X months" instead of "0.0 – X" (issue #1684). */
export function formatPaybackRange(min: number, max: number): string {
  const lo = min.toFixed(1);
  const hi = max.toFixed(1);
  if (lo === hi) return `${lo} months`;
  // Decide on the rounded value: a positive bound under 0.05 prints "0.0" and a
  // negative one "-0.0" or below, and none of them is a payback period (#1705).
  return Number(lo) <= 0 ? `Up to ${hi} months` : `${lo} – ${hi} months`;
}

/** Format a term range, collapsing "1 yr – 1 yr" to "1 yr". */
export function formatTermRange(min: number, max: number): string {
  const lo = formatTermShort(min);
  const hi = formatTermShort(max);
  if (!lo || !hi) return lo || hi || NO_VALUE;
  return lo === hi ? lo : `${lo} – ${hi}`;
}
