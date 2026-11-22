import { formatCurrency, NO_VALUE } from '../utils';
import type { DashboardSummary, LocalRecommendation } from '../types';
import { groupRecsByCell, pageLevelRange, formatSavingsRange } from '../recommendations';
import { buildKpiTile } from '../lib/kpi-tile';
import { SAVINGS_UNSCALED_NOTE } from './chartShared';
import { computeServiceStats } from './serviceStats';

export function renderDashboardSummary(
  data: DashboardSummary,
  recs: readonly LocalRecommendation[],
  recsCount: number | null,
): void {
  const summary = document.getElementById('summary');
  if (!summary) return;

  // Compute per-cell savings range from recs. pageLevelRange sums per-cell
  // min/max savings (best and worst variant per physical resource cell),
  // avoiding the ~6x inflation of summing every variant of every cell that
  // the flat summary.potential_monthly_savings carries.
  // Falls back to NO_VALUE (an em dash) when recs is empty or fetch failed; never fabricates $0
  // for an absent/failed computation (finding H-3: no silent $0 fallback).
  // Soft-fail (#304): wrap in try/catch so an unexpected non-iterable shape
  // that slips past the Array.isArray guard in loadDashboard (e.g. if
  // groupRecsByCell is called from a path that bypasses the guard) cannot
  // blank the entire dashboard -- the savings card shows NO_VALUE instead.
  let savingsDisplay: string;
  try {
    const groups = groupRecsByCell(recs);
    const range = pageLevelRange(groups);
    savingsDisplay = range.cellCount > 0
      ? formatSavingsRange(range.savingsMin, range.savingsMax)
      : NO_VALUE;
  } catch (recErr) {
    console.warn('Dashboard: failed to compute savings range from recommendations:', recErr);
    savingsDisplay = NO_VALUE;
  }

  // When no recommendations and no commitments exist, "100% coverage" is
  // misleading -- nothing is being tracked. Show a dash instead.
  const nothingTracked = !data.total_recommendations && !data.active_commitments;
  // #978: round to 1 decimal place to match sibling coverage formatting (inventory.ts)
  // and prevent overflow at narrower screen widths.
  // H-3: use null-safe checks so absent coverage/target are shown as NO_VALUE
  // rather than fabricating '0%'/'80%'. The hardcoded 80 default is removed
  // because target_coverage is a user-configured value from the API -- the
  // frontend must not substitute a business default when the field is absent.
  const currentCoverage = data.current_coverage ?? null;
  const targetCoverage = data.target_coverage ?? null;
  const coverageValue = nothingTracked
    ? '—'
    : currentCoverage !== null ? `${currentCoverage.toFixed(1)}%` : NO_VALUE;
  const coverageDetail = nothingTracked
    ? 'No services tracked'
    : targetCoverage !== null ? `Target: ${targetCoverage.toFixed(1)}%` : `Target: ${NO_VALUE}`;

  // Render KPI tiles via DOM construction (textContent / appendChild)
  // rather than an innerHTML template literal, per the issue #340 plan's
  // XSS constraint. The values are all backend-sourced numbers/strings,
  // but the safe-by-default pattern is cheap and removes the question.
  while (summary.firstChild) summary.removeChild(summary.firstChild);
  const tiles: ReadonlyArray<{
    kpi: string;
    title: string;
    value: string;
    valueSavings?: boolean;
    detail: string;
  }> = [
    { kpi: 'savings',     title: 'Potential Monthly Savings', value: savingsDisplay, valueSavings: true,
      // #180: count the same rows the range sums (and the Opportunities page
      // counts); the backend total ignores the Enabled Providers setting.
      detail: `${recsCount ?? data.total_recommendations ?? NO_VALUE} recommendations` },
    // #1709: committed_monthly is the active commitments' estimated monthly
    // savings, not what they cost.
    { kpi: 'commitments', title: 'Active Commitments', value: data.active_commitments != null ? String(data.active_commitments) : NO_VALUE,
      detail: `${formatCurrency(data.committed_monthly)}/mo estimated savings` },
    { kpi: 'coverage',    title: 'Current Coverage', value: coverageValue, detail: coverageDetail },
    // #1686: monthly estimated savings times elapsed months, not recorded savings.
    { kpi: 'ytd',         title: 'YTD Savings', value: formatCurrency(data.ytd_savings), valueSavings: true,
      detail: 'Estimated from active commitments' },
  ];
  // Shared with the Opportunities summary (issue #743). buildKpiTile keeps an
  // absent (NO_VALUE) figure neutral rather than green (issue #739).
  for (const t of tiles) {
    const tile = buildKpiTile({ ...t, spark: true });
    // #1946: the range is not scaled by the coverage cap the by_service figure applies.
    if (t.kpi === 'savings') tile.title = SAVINGS_UNSCALED_NOTE;
    summary.appendChild(tile);
  }
}

/**
 * Build a small SVG polyline path string from a series of numeric values,
 * normalized into a width × height viewport. Returns the points string for
 * a <polyline points="..."> element. Pure helper — no DOM access, no I/O.
 */
function sparklinePoints(values: readonly number[], width: number, height: number): string {
  if (values.length < 2) return '';
  const min = Math.min(...values);
  const max = Math.max(...values);
  const range = max - min || 1;
  const stepX = width / (values.length - 1);
  return values.map((v, i) => {
    const x = i * stepX;
    const y = height - ((v - min) / range) * height;
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  }).join(' ');
}

/**
 * Attach an SVG sparkline to a single KPI tile, keyed by its data-spark-key.
 * Silently no-ops when the placeholder isn't in the DOM (e.g. a different
 * card layout is rendered) or when there aren't enough data points to draw
 * a meaningful line. Uses DOM methods only — no innerHTML.
 */
export function attachSparkline(key: string, values: readonly number[]): void {
  const svg = document.querySelector<SVGSVGElement>(`.kpi-tile-spark[data-spark-key="${key}"]`);
  if (!svg) return;
  while (svg.firstChild) svg.removeChild(svg.firstChild);
  if (values.length < 2) {
    svg.classList.add('hidden');
    return;
  }
  svg.classList.remove('hidden');

  const width = 80;
  const height = 24;
  svg.setAttribute('viewBox', `0 0 ${width} ${height}`);
  svg.setAttribute('preserveAspectRatio', 'none');

  const points = sparklinePoints(values, width, height);
  if (!points) {
    svg.classList.add('hidden');
    return;
  }

  const polyline = document.createElementNS('http://www.w3.org/2000/svg', 'polyline');
  polyline.setAttribute('points', points);
  polyline.setAttribute('fill', 'none');
  polyline.setAttribute('stroke', 'currentColor');
  polyline.setAttribute('stroke-width', '1.5');
  polyline.setAttribute('stroke-linecap', 'round');
  polyline.setAttribute('stroke-linejoin', 'round');
  svg.appendChild(polyline);
}

export const __test__ = { sparklinePoints, attachSparkline, computeServiceStats };
