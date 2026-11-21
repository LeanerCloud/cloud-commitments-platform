/**
 * Savings Plans capacity sizing (#1959). The backend sizes a Savings Plans
 * recommendation to capacity_percent by flooring its hourly commitment to
 * whole cents (planstep.SizeSavingsPlan, minimum $0.01/hr) and keeps Count 1.
 * This mirrors that rule so the confirmation UI shows the commitment that
 * will be bought; the backend stays the source of truth.
 */

import type { LocalRecommendation } from './types';

const DECIMAL_RE = /^(\d+)(?:\.(\d+))?(?:e([+-]?\d+))?$/;

/** details.hourly_commitment of a Savings Plans rec, or null when absent or not positive. */
export function savingsPlanHourly(rec: Pick<LocalRecommendation, 'details'>): number | null {
  const details = rec.details;
  if (typeof details !== 'object' || details === null) return null;
  const hourly = (details as Record<string, unknown>)['hourly_commitment'];
  return typeof hourly === 'number' && Number.isFinite(hourly) && hourly > 0 ? hourly : null;
}

/**
 * floor(hourly * capacityPercent / 100) in whole cents, computed exactly on
 * the shortest decimal form of `hourly` (what the Go sizer parses) so values
 * like 0.58 at 50% give 29 cents, not 28 from float error.
 */
export function savingsPlanSizedCents(hourly: number, capacityPercent: number): number {
  if (!Number.isInteger(capacityPercent) || capacityPercent < 0) {
    throw new Error(`capacity percent must be a whole number, got ${capacityPercent}`);
  }
  const m = DECIMAL_RE.exec(String(hourly));
  if (!m) throw new Error(`unsupported hourly commitment ${hourly}`);
  const frac = m[2] ?? '';
  const mantissa = BigInt(m[1]! + frac);
  const scale = Number(m[3] ?? '0') - frac.length; // hourly = mantissa * 10^scale
  // cents = hourly * 100 * pct / 100 = hourly * pct
  const num = mantissa * BigInt(capacityPercent);
  const cents = scale >= 0 ? num * 10n ** BigInt(scale) : num / 10n ** BigInt(-scale);
  return Number(cents);
}

/** The hourly commitment (USD) the backend will buy, or null below the $0.01/hr minimum. */
export function savingsPlanSizedHourly(hourly: number, capacityPercent: number): number | null {
  const cents = savingsPlanSizedCents(hourly, capacityPercent);
  return cents < 1 ? null : cents / 100;
}

/**
 * Display scaling of a Savings Plans rec: Count stays 1 (the backend sizes
 * the hourly commitment, never the count) and the money fields follow the
 * sized/recommended hourly ratio. Returns null when the sized commitment is
 * below the minimum, or when the rec carries no hourly commitment to size.
 */
export function scaleSavingsPlanForCapacity(r: LocalRecommendation, capacityPercent: number): LocalRecommendation | null {
  // recommended_count carries the pre-scaling count, as for every other row.
  if (capacityPercent >= 100) return { ...r, recommended_count: r.count };
  const hourly = savingsPlanHourly(r);
  if (hourly === null) return null;
  const sized = savingsPlanSizedHourly(hourly, capacityPercent);
  if (sized === null) return null;
  const ratio = sized / hourly;
  return {
    ...r,
    recommended_count: r.count,
    upfront_cost: r.upfront_cost * ratio,
    monthly_cost: r.monthly_cost != null ? r.monthly_cost * ratio : null,
    savings: r.savings * ratio,
    on_demand_cost: r.on_demand_cost != null ? r.on_demand_cost * ratio : r.on_demand_cost,
  };
}

/**
 * One-line confirmation text for the Savings Plans rows among `recs`: the
 * hourly commitment the backend will buy at this capacity against the
 * recommended one. Null when no row is a sizable Savings Plan.
 */
export function savingsPlanHourlySummary(
  recs: readonly LocalRecommendation[],
  capacityPercent: number,
  isSavingsPlan: (service: string) => boolean,
): string | null {
  let sized = 0;
  let recommended = 0;
  let rows = 0;
  for (const r of recs) {
    if (!isSavingsPlan(r.service)) continue;
    const hourly = savingsPlanHourly(r);
    if (hourly === null) continue;
    rows++;
    recommended += hourly;
    // At 100% the backend buys the stored hourly as is; below that it floors to whole cents.
    sized += capacityPercent >= 100 ? hourly : savingsPlanSizedCents(hourly, capacityPercent) / 100;
  }
  if (rows === 0) return null;
  // Up to five decimals (the precision the purchase client sends), never fewer than two.
  const usd = (x: number): string => `$${x.toFixed(5).replace(/0{1,3}$/, '')}/hr`;
  return `Savings Plans hourly commitment: ${usd(sized)} (${capacityPercent}% of ${usd(recommended)} recommended)`;
}
