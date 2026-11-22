import type { LocalRecommendation } from '../types';
import type { SavingsDataPoint } from '../api';
import { groupRecsByCell } from '../recommendations';

/**
 * Per-service savings statistics derived from a window of data points or
 * from a recommendations list.
 * Exported for unit testing only.
 */
export interface ServiceSavingsStats {
  min: number;
  max: number;
  sum: number;
  count: number;
  samples: number[];
  /** Label of the recommendation option that produced the minimum savings (e.g. "1yr no-upfront"). */
  minLabel?: string;
  /** Label of the recommendation option that produced the maximum savings (e.g. "3yr all-upfront"). */
  maxLabel?: string;
  /** Cells skipped because none of their variants has known savings. */
  unknownCells?: number;
}

/**
 * Compute per-service savings stats from an array of data points. Each
 * data point carries a by_service map of { serviceName: savingsValue }.
 * Points with no by_service entry (omitempty from backend) are skipped.
 * Exported for unit testing.
 *
 * @deprecated Used only by historical-data tests. New production code uses
 * computeServiceStatsFromRecs for forward-looking potential savings.
 */
export function computeServiceStats(
  dataPoints: readonly SavingsDataPoint[],
): Map<string, ServiceSavingsStats> {
  const stats = new Map<string, ServiceSavingsStats>();
  for (const dp of dataPoints) {
    if (!dp.by_service) continue;
    for (const [svc, val] of Object.entries(dp.by_service)) {
      if (typeof val !== 'number') continue;
      const existing = stats.get(svc);
      if (existing) {
        existing.min = Math.min(existing.min, val);
        existing.max = Math.max(existing.max, val);
        existing.sum += val;
        existing.count += 1;
        existing.samples.push(val);
      } else {
        stats.set(svc, { min: val, max: val, sum: val, count: 1, samples: [val] });
      }
    }
  }
  return stats;
}

/** Label shown when the per-cell min/max picks differ in term or payment. */
export const MIXED_VARIANT_LABEL = 'varies by account/region';

function variantLabel(rec: LocalRecommendation): string {
  const paymentLabel = rec.payment && rec.payment.trim().length > 0 ? rec.payment : 'unspecified';
  return `${rec.term}yr ${paymentLabel}`;
}

/** One label when every chosen variant shares it, else the neutral label. */
function consistentLabel(picks: readonly LocalRecommendation[]): string {
  const labels = new Set(picks.map(variantLabel));
  return labels.size === 1 ? [...labels][0]! : MIXED_VARIANT_LABEL;
}

/**
 * Compute per-service potential-savings stats from a recommendations list.
 *
 * Rows are grouped into cells (groupRecsByCell, the grouping Opportunities
 * uses). One cell's rows are mutually exclusive term/payment variants, so a
 * service's range is the sum over its cells of each cell's lowest-savings
 * variant (min) and of each cell's highest (max), matching pageLevelRange
 * (#1770). The min/max labels name a term/payment only when every cell's
 * chosen variant shares it, else MIXED_VARIANT_LABEL.
 *
 * Rows with unknown savings (not a finite number) are never counted as 0: a
 * cell whose variants are all unknown is skipped and counted in
 * unknownCells; a service with no known cell has no entry.
 *
 * `sum`, `count` and `samples` stay row-level (every known variant row).
 *
 * Exported for unit testing.
 */
export function computeServiceStatsFromRecs(
  recs: readonly LocalRecommendation[],
): Map<string, ServiceSavingsStats> {
  const byService = new Map<string, LocalRecommendation[]>();
  for (const rec of recs) {
    const rows = byService.get(rec.service);
    if (rows) rows.push(rec);
    else byService.set(rec.service, [rec]);
  }
  const stats = new Map<string, ServiceSavingsStats>();
  for (const [svc, rows] of byService) {
    const minPicks: LocalRecommendation[] = [];
    const maxPicks: LocalRecommendation[] = [];
    const samples: number[] = [];
    let min = 0;
    let max = 0;
    let unknownCells = 0;
    for (const variants of groupRecsByCell(rows).values()) {
      const known = variants.filter(v => typeof v.savings === 'number' && Number.isFinite(v.savings));
      if (known.length === 0) {
        unknownCells += 1;
        continue;
      }
      let lo = known[0]!;
      let hi = known[0]!;
      for (const v of known) {
        if (v.savings < lo.savings) lo = v;
        if (v.savings > hi.savings) hi = v;
        samples.push(v.savings);
      }
      min += lo.savings;
      max += hi.savings;
      minPicks.push(lo);
      maxPicks.push(hi);
    }
    if (minPicks.length === 0) continue;
    stats.set(svc, {
      min,
      max,
      sum: samples.reduce((a, b) => a + b, 0),
      count: samples.length,
      samples,
      minLabel: consistentLabel(minPicks),
      maxLabel: consistentLabel(maxPicks),
      ...(unknownCells > 0 ? { unknownCells } : {}),
    });
  }
  return stats;
}
