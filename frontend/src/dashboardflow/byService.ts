import { Chart } from 'chart.js';
import { formatCurrency } from '../utils';
import type { ServiceSavings, LocalRecommendation } from '../types';
import { serviceLabel } from '../lib/service-labels';
import { upsertChart } from '../modules/chart-utils';
import { setChartSummary, renderChartTable, removeChartTable } from '../lib/chart-a11y';
import { applyChartDefaults, chartColor } from '../lib/chart-theme';
import { dashboardState } from './dashboardState';
import { SAVINGS_BY_SERVICE_MAX, segmentGap, SERVICE_CHART_COMPACT_PX, SERVICE_CHART_ROW_PX, SERVICE_CHART_CHROME_PX, SAVINGS_UNSCALED_NOTE, SAVINGS_BY_SERVICE_TABLE_ID, savingsLayerColors } from './chartShared';
import { watchTheme } from './themeWatch';
import { computeServiceStatsFromRecs } from './serviceStats';

/**
 * Render the single merged per-service savings chart (issues #769 + #908).
 *
 * Each service bar is a single stack with three layers (bottom to top):
 *   1. Current / Committed (darkest shade): already-realized savings from
 *      active commitments, sourced from by_service[svc].current_savings.
 *   2. Lowest option (solid mid-shade): the lowest available recommendation
 *      option minus what is already committed, i.e. max(0, min(rec.savings) -
 *      current). Together with the Current layer this represents the floor.
 *   3. Upside (lightest / 35% opacity): max(rec.savings) - min(rec.savings),
 *      the variability between the cheapest and most-aggressive option.
 *
 * Services are sorted by (current + max potential) descending so the bar
 * with the most total visible value is leftmost. Services present in
 * byService but absent from recs still appear with only the Current band
 * (no further upside surfaced). Services present in recs but absent from
 * byService default to current = 0 (no committed savings yet).
 *
 * The top SAVINGS_BY_SERVICE_MAX services are shown; the note under the
 * heading says so when truncated (every service is in the table).
 *
 * Empty state: shown when no bar (across all three layers) has any positive
 * value.
 */
export function renderSavingsByService(
  recs: readonly LocalRecommendation[],
  byService: Record<string, ServiceSavings> = {},
  filterDesc = '',
): void {
  const canvas = document.getElementById('savings-by-service-chart') as HTMLCanvasElement | null;
  const emptyEl = document.getElementById('savings-by-service-empty');
  const section = document.getElementById('savings-by-service-section');
  if (!canvas) return;
  dashboardState.lastSavingsByServiceArgs = [recs, byService, filterDesc];

  const stats = computeServiceStatsFromRecs(recs);
  const heading = section?.querySelector('h3');
  const note = document.getElementById('savings-by-service-note');

  // Build the union of services from both recs and byService. Services in
  // byService-only (commitment exists, no new rec) still show a Current band.
  const allServices = new Set<string>([
    ...stats.keys(),
    ...Object.keys(byService),
  ]);

  // Per-service totals used for sort and empty-state detection.
  type SvcEntry = {
    svc: string;
    current: number;
    minRec: number;
    maxRec: number;
    lowestOption: number;
    upside: number;
    visibleTotal: number;
  };
  const entries: SvcEntry[] = Array.from(allServices).map(svc => {
    const s = stats.get(svc);
    const current = byService[svc]?.current_savings ?? 0;
    const minRec = s?.min ?? 0;
    const maxRec = s?.max ?? 0;
    const lowestOption = Math.max(0, minRec - current);
    const upside = Math.max(0, maxRec - minRec);
    return {
      svc,
      current,
      minRec,
      maxRec,
      lowestOption,
      upside,
      visibleTotal: current + lowestOption + upside,
    };
  });

  // Keep only services with some positive value in any layer.
  const positive = entries.filter(e => e.visibleTotal > 0);

  // Sort by visible total desc so the most-valuable bar is leftmost.
  positive.sort((a, b) => b.visibleTotal - a.visibleTotal);

  if (positive.length === 0) {
    if (heading) heading.textContent = 'Potential savings range per service';
    note?.classList.add('hidden');
    dashboardState.savingsByServiceChart?.destroy();
    dashboardState.savingsByServiceChart = null;
    canvas.classList.add('hidden');
    removeChartTable(SAVINGS_BY_SERVICE_TABLE_ID);
    if (emptyEl) {
      // When a filter is active and excluded all results, surface it so the
      // user understands why the chart is blank (mirrors #747 pattern).
      emptyEl.textContent = filterDesc
        ? `No recommendations for the selected filter (${filterDesc}).`
        : 'No positive potential savings found for current recommendations.';
      emptyEl.classList.remove('hidden');
    }
    return;
  }

  canvas.classList.remove('hidden');
  emptyEl?.classList.add('hidden');

  // Cap at maximum; the note under the heading says how many are drawn.
  const truncated = positive.length > SAVINGS_BY_SERVICE_MAX;
  const visible = positive.slice(0, SAVINGS_BY_SERVICE_MAX);
  if (heading) heading.textContent = 'Potential savings range per service';
  if (note) {
    note.textContent = truncated
      ? `Monthly savings. Top ${SAVINGS_BY_SERVICE_MAX} of ${positive.length} services shown; the table lists all. ${SAVINGS_UNSCALED_NOTE}`
      : `Monthly savings. ${SAVINGS_UNSCALED_NOTE}`;
    note.classList.remove('hidden');
  }
  // Height follows the bar count (both bounds, so Chart.js sizes to it).
  const chartHeight = `${visible.length * SERVICE_CHART_ROW_PX + SERVICE_CHART_CHROME_PX}px`;
  canvas.style.minHeight = chartHeight;
  canvas.style.maxHeight = chartHeight;

  const labels = visible.map(e => e.svc);

  // Layer 1 (bottom): Current / Committed savings.
  const currentData = visible.map(e => e.current);

  // Layer 2: Lowest option -- the gap between the cheapest rec and the already-
  // committed amount. Clamped to 0 to avoid negative segments when commitments
  // exceed the current lowest-option savings figure.
  const lowestOptionData = visible.map(e => Math.max(0, e.minRec - e.current));

  // Layer 3 (top): Upside -- the spread between cheapest and most-aggressive option.
  const upsideData = visible.map(e => Math.max(0, e.maxRec - e.minRec));

  const totalSavings = positive.reduce((acc, e) => acc + e.visibleTotal, 0);

  // #744: the canvas is role="img", so its label and a table are the only
  // way a screen-reader user can read these values. Every service is listed,
  // including any beyond the SAVINGS_BY_SERVICE_MAX bars drawn.
  const top = positive[0]!;
  setChartSummary(canvas,
    `Stacked bar chart of monthly savings for ${positive.length} service${positive.length === 1 ? '' : 's'}. `
    + `The bars add up to ${formatCurrency(totalSavings)}: current committed savings plus each service's `
    + 'highest-savings option, so this is not the Potential Monthly Savings range. '
    + `Largest: ${serviceLabel(top.svc)} at ${formatCurrency(top.visibleTotal)}. `
    + 'Layers: Current / Committed, Lowest option, Upside. Values are in the table below the chart.');
  renderChartTable(canvas, SAVINGS_BY_SERVICE_TABLE_ID, {
    caption: 'Potential savings range per service (monthly)',
    columns: ['Service', 'Current / Committed', 'Lowest option', 'Upside', 'Total'],
    rows: positive.map(e => [serviceLabel(e.svc), formatCurrency(e.current), formatCurrency(e.lowestOption),
      formatCurrency(e.upside), formatCurrency(e.visibleTotal)]),
  });

  const layerColors = savingsLayerColors();
  const gap = segmentGap();
  applyChartDefaults(Chart.defaults);
  // #747: update in place so a filter change doesn't replay the entry animation.
  watchTheme();
  dashboardState.savingsByServiceChart = upsertChart(dashboardState.savingsByServiceChart, canvas, {
    type: 'bar',
    data: {
      labels,
      datasets: [
        // Current sits at the base of the stack so already-realized savings
        // are visually "under" the potential range -- dataset order in Chart.js
        // stacked bars determines bottom-to-top rendering.
        {
          label: 'Current / Committed',
          data: currentData,
          backgroundColor: layerColors.current,
          borderRadius: { topLeft: 4, topRight: 0, bottomLeft: 4, bottomRight: 0 },
          ...gap,
          stack: 'savings',
        },
        {
          label: 'Lowest option',
          data: lowestOptionData,
          backgroundColor: layerColors.lowestOption,
          borderRadius: { topLeft: 0, topRight: 0, bottomLeft: 0, bottomRight: 0 },
          ...gap,
          stack: 'savings',
        },
        {
          label: 'Upside',
          data: upsideData,
          backgroundColor: layerColors.upside,
          borderRadius: { topLeft: 0, topRight: 4, bottomLeft: 0, bottomRight: 4 },
          ...gap,
          stack: 'savings',
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      // Horizontal bars: service names read left to right at full length
      // instead of rotated slugs under narrow columns.
      indexAxis: 'y',
      plugins: {
        legend: {
          display: true,
          position: 'top',
          align: 'start',
          // Default swatches: each one is its dataset's single bar colour.
          labels: { boxWidth: 12 },
        },
        tooltip: {
          callbacks: {
            title: (items) => serviceLabel(items[0]?.label ?? ''),
            label: (ctx) => {
              const svc = ctx.label ?? '';
              const s = stats.get(svc);
              // The current-savings dataset gets a focused, single-line
              // tooltip; the range datasets share the full breakdown. This
              // runs before the stats guard: committed services without a
              // recommendation have no stats but still have a bar.
              if (ctx.dataset?.label === 'Current / Committed') {
                const current = byService[svc]?.current_savings ?? 0;
                return `Current / Committed: ${formatCurrency(current)}`;
              }
              if (!s) return '';
              const current = byService[svc]?.current_savings ?? 0;
              const maxRec = s?.max ?? 0;
              const minRec = s?.min ?? 0;
              const lowestOption = Math.max(0, minRec - current);
              const upside = Math.max(0, maxRec - minRec);
              const total = current + lowestOption + upside;
              const pct = totalSavings > 0 ? ((total / totalSavings) * 100).toFixed(1) : '0.0';
              const lines = [
                `Service: ${serviceLabel(svc)}`,
                `Total: ${formatCurrency(total)} (${pct}% of all services)`,
                `Current / Committed: ${formatCurrency(current)}`,
                `Lowest option: ${formatCurrency(lowestOption)}`,
                `Upside: ${formatCurrency(upside)}`,
              ];
              if (s?.minLabel) lines.push(`Min option: ${s.minLabel}`);
              if (s?.maxLabel) lines.push(`Max option: ${s.maxLabel}`);
              if (s?.unknownCells) lines.push(`Excluded: ${s.unknownCells} cell(s) with unknown savings`);
              return lines;
            },
          },
        },
      },
      scales: {
        // Value axis. Its unit ("Monthly savings") is the note under the
        // heading, so no axis title; light gridlines only.
        x: {
          stacked: true,
          beginAtZero: true,
          grid: { color: chartColor('--cudly-chart-grid-faint') },
          border: { display: false },
          // Skip ticks rather than rotate them on narrow screens.
          ticks: { callback: (v) => formatCurrency(v as number), maxTicksLimit: 8, maxRotation: 0, autoSkipPadding: 12 },
        },
        // Category axis: one service per row, every label kept (#189).
        y: {
          stacked: true,
          grid: { display: false },
          ticks: {
            autoSkip: false,
            callback(value) {
              return serviceLabel(this.getLabelForValue(value as number), this.chart.width < SERVICE_CHART_COMPACT_PX);
            },
          },
        },
      },
    },
  });
}
