import type { CostPeriod } from '../state';
import { formatCurrency, NO_VALUE } from '../utils';
import type { LocalRecommendation } from '../types';
import { HOURS_PER_MONTH } from '../lib/hours-per-month';

/**
 * Returns the effective monthly savings for a recommendation.
 *
 * All three providers (AWS, Azure, GCP) report `savings` as the already-net
 * monthly savings figure -- i.e. the on-demand/recurring delta AFTER the
 * amortized upfront cost has been factored in. Subtracting upfront again
 * would double-count it and drive the result negative for high-upfront RIs.
 */
export function effectiveMonthlySavings(r: LocalRecommendation): number {
  return r.savings;
}

/**
 * Computes effective savings as a percentage of the equivalent on-demand
 * monthly cost. Returns null when the result would require division by zero
 * (on_demand_monthly === 0).
 *
 * All three providers (AWS, Azure, GCP) report `savings` as already-net
 * monthly savings -- the on-demand/recurring delta AFTER the amortized
 * upfront cost is factored in. The numerator is therefore `savings` directly;
 * subtracting amortized upfront again would double-count it and drive the
 * percentage negative for high-upfront RIs (issue #1103).
 *
 * Denominator source (closes #274):
 *   1. If `r.on_demand_cost` is populated (non-null, > 0), use it directly
 *      -- it's the canonical baseline straight from the cloud provider
 *      (Azure CostWithNoReservedInstances, AWS
 *      EstimatedMonthlyOnDemandCost).
 *   2. Otherwise fall back to reconstructing from
 *      `monthly_cost + savings + amortized_upfront`. This is what the
 *      frontend always did before #274 plumbed `on_demand_cost` through;
 *      it stays correct for cleanly-shaped data, but for Azure all-upfront
 *      recs where `monthly_cost = $0` the reconstructed denominator
 *      collapses to `savings + amortized` and inflates the percentage well
 *      past realistic ceilings (~30% real to 86% shown).
 *
 * Formula:
 *   amortized_upfront_per_month = upfront_cost / (term * 12)   [denominator only]
 *   effective_savings_pct       = (savings / on_demand_monthly) * 100
 */
export function effectiveSavingsPct(r: LocalRecommendation): number | null {
  // Per acceptance criteria: term=0 is a data anomaly -- render as em-dash.
  if (!r.term) return null;
  // monthly_cost === null means the provider API did not return a recurring
  // monthly breakdown. Without it we can only compute the formula when the
  // provider also gave us an explicit on_demand_cost; otherwise render as
  // em-dash rather than collapsing the denominator to savings alone (which
  // produces 100% / neg%).
  const hasOnDemand = r.on_demand_cost != null && r.on_demand_cost > 0;
  if (r.monthly_cost == null && !hasOnDemand) return null;
  // #323: for AWS rows the on_demand_cost field is the provider-canonical
  // denominator (EstimatedMonthlyOnDemandCost from Cost Explorer). When it
  // is absent the reconstruction formula (monthly_cost + savings + amortized)
  // diverges from the true on-demand baseline for RI/SP recs, producing
  // misleadingly high percentages. Return null so the UI renders NO_VALUE rather
  // than a silently-wrong value.
  if (r.provider === 'aws' && !hasOnDemand) return null;
  const monthsInTerm = r.term * 12;
  const amortized = r.upfront_cost / monthsInTerm;
  const onDemand = hasOnDemand
    ? (r.on_demand_cost as number)
    : (r.monthly_cost as number) + r.savings + amortized;
  if (onDemand === 0) return null;
  return (r.savings / onDemand) * 100;
}

/**
 * Returns the effective savings percentage to DISPLAY (and sort) for a
 * recommendation, preferring the provider-authoritative value over the
 * client-side reconstruction.
 *
 * CLI/GUI parity: the CLI/reporter prints `rec.SavingsPercentage` straight
 * from the provider (AWS EstimatedMonthlySavingsPercentage, Azure/GCP
 * converter SavingsPercentage), but that figure used to be dropped at the API
 * boundary, forcing the GUI to re-derive the % via effectiveSavingsPct. The
 * recomputation could drift from the authoritative number and returned null
 * (an em-dash) whenever on_demand_cost was missing even though the provider reported
 * a real % (the #323 case). Now that `savings_percentage` is plumbed through,
 * the GUI shows the same number the CLI shows.
 *
 * Preference rule: use `r.savings_percentage` when it is a finite, non-null
 * number; otherwise fall back to the (fixed, net-savings) effectiveSavingsPct.
 * Returns null only when both are unavailable (renders as an em-dash).
 */
export function displaySavingsPct(r: LocalRecommendation): number | null {
  const provided = r.savings_percentage;
  if (provided != null && Number.isFinite(provided)) {
    return provided;
  }
  return effectiveSavingsPct(r);
}

/**
 * On-demand-weighted average of the per-row Eff. % the purchase modal shows
 * (displaySavingsPct), so the Totals row can never print a figure the rows
 * refused to: a row that renders an em-dash (e.g. AWS without on_demand_cost,
 * #323) contributes nothing. Each row is weighted by its on-demand baseline:
 * on_demand_cost when known, otherwise the baseline its own % implies
 * (savings / pct). Returns null when no row contributes.
 */
export function weightedDisplaySavingsPct(recs: LocalRecommendation[]): number | null {
  let num = 0;
  let den = 0;
  for (const r of recs) {
    const pct = displaySavingsPct(r);
    if (pct === null) continue;
    const base = r.on_demand_cost != null && r.on_demand_cost > 0
      ? r.on_demand_cost
      : pct !== 0 ? (r.savings * 100) / pct : null;
    if (base === null || !(base > 0)) continue;
    num += (pct * base) / 100;
    den += base;
  }
  return den > 0 ? (num / den) * 100 : null;
}

/**
 * Returns the provider-reported on-demand monthly cost (`r.on_demand_cost`)
 * directly. Issue #330 — earlier behaviour reconstructed the value from
 * `monthly_cost + savings + amortized_upfront`, which drifted from the
 * provider's billed price for rounding edge cases (Azure all-upfront RIs,
 * AWS Capacity Reservation discounts, partial-day proration). Aligning this
 * with `effectiveSavingsPct`'s `hasOnDemand` branch makes the column show
 * the same denominator the percentage column uses, so the two never disagree.
 *
 * Returns `null` when `on_demand_cost` is missing, undefined, or `0` —
 * cell renders `—` (same em-dash convention as the existing Monthly Cost
 * column for null `monthly_cost`).
 */
export function onDemandMonthly(r: LocalRecommendation): number | null {
  if (r.on_demand_cost != null && r.on_demand_cost > 0) {
    return r.on_demand_cost;
  }
  return null;
}

//
// Cost-period scaling (issue #319)
//

/**
 * Conversion factors relative to a monthly base. Hourly uses the 730-hour
 * month the backend uses (issues #337, #394); daily stays a 30-day month.
 */
const PERIOD_FACTOR: Record<CostPeriod, number> = {
  hourly:  1 / HOURS_PER_MONTH,
  daily:   1 / 30,
  monthly: 1,
  yearly:  12,
};

/**
 * Scale a monthly cost value to the requested display period.
 * Returns null when input is null or undefined (preserves "—" rendering).
 * Exported for tests.
 */
export function scaleCost(monthly: number | null | undefined, period: CostPeriod): number | null {
  if (monthly == null) return null;
  return monthly * PERIOD_FACTOR[period];
}

/** Number of decimal places to use when formatting a scaled cost value. */
export const PERIOD_DECIMALS: Record<CostPeriod, number> = {
  hourly:  4,
  daily:   2,
  monthly: 2,
  yearly:  0,
};

// The non-monthly periods use explicit per-period precision (PERIOD_DECIMALS);
// monthly keeps formatCurrency's default so it matches the rest of the
// dashboard. Both go through formatCurrency so a yearly figure prints
// "$123,456", not "$123456" (issue #739).
function formatScaled(scaled: number, period: CostPeriod): string {
  return period === 'monthly' ? formatCurrency(scaled) : formatCurrency(scaled, '$', PERIOD_DECIMALS[period]);
}

/**
 * Format a monthly cost value scaled to the requested display period.
 * Returns "—" for null/undefined input.
 * Uses period-appropriate decimal precision.
 * Exported for tests.
 */
export function formatCostForPeriod(monthly: number | null | undefined, period: CostPeriod): string {
  const scaled = scaleCost(monthly, period);
  if (scaled === null) return NO_VALUE;
  return formatScaled(scaled, period);
}

/** Human-readable suffix label for a period (used in column headers). */
export function periodSuffix(period: CostPeriod): string {
  switch (period) {
    case 'hourly':  return '/ hr';
    case 'daily':   return '/ day';
    case 'monthly': return '/ mo';
    case 'yearly':  return '/ yr';
  }
}

/** Suffix appended directly to a value ("$20/mo"); periodSuffix spaces it for labels. */
export function periodUnit(period: CostPeriod): string {
  return periodSuffix(period).replace('/ ', '/');
}

/**
 * Format a period-scaled savings range using period-appropriate precision.
 * Used for the summary cards and cell-level range displays (issue #319).
 * Collapses "$X – $X" to "$X".
 */
export function formatScaledRange(min: number, max: number, period: CostPeriod): string {
  const lo = formatScaled(min, period);
  const hi = formatScaled(max, period);
  return lo === hi ? lo : `${lo} – ${hi}`;
}
