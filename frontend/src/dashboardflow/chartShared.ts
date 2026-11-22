import { Chart, registerables } from 'chart.js';
import { showSkeletonBlock, isSkeletonActive } from '../lib/skeleton';
import { chartColor, surfaceColor } from '../lib/chart-theme';

// Register Chart.js components
Chart.register(...registerables);

// Maximum number of services to show in the bar chart before truncating.
export const SAVINGS_BY_SERVICE_MAX = 10;

// #744: a 2px card-coloured gap at the end of every stacked segment, so
// adjacent layers are separated by an edge as well as by their layer colour.
// The bars are horizontal, so a segment ends on its right edge. The colour is
// the --cudly-surface token (#699), read at render time.
export function segmentGap() {
  return { borderColor: surfaceColor(), borderWidth: { top: 0, right: 2, bottom: 0, left: 0 } } as const;
}

// Below this chart width the category labels use the compact names.
export const SERVICE_CHART_COMPACT_PX = 560;

// Row height of one horizontal bar plus the legend and value axis, so the
// chart grows with the number of services instead of being a fixed 560px.
export const SERVICE_CHART_ROW_PX = 36;
export const SERVICE_CHART_CHROME_PX = 84;

// Ids of the "View as table" text alternatives for the Home charts (#744).
// #1946: shared caption for the Home savings range figures (headline and chart).
export const SAVINGS_UNSCALED_NOTE = 'Unscaled by the coverage cap: sums each cell\'s known-savings variants.';
export const SAVINGS_BY_SERVICE_TABLE_ID = 'savings-by-service-table';
export const SAVINGS_TREND_TABLE_ID = 'savings-trend-table';
export const SAVINGS_TREND_LOADING_ID = 'savings-trend-loading';
export const TREND_LOAD_FAILED_TEXT = 'Savings history could not be loaded. Reload the page to try again.';

/**
 * #1686: put a skeleton where a chart card's canvas will be, but only before
 * its first chart is drawn; later reloads keep the current chart on screen.
 * The card's renderer un-hides the canvas once it has data.
 */
export function showChartSkeleton(loadingId: string, canvasId: string, emptyId: string, chart: Chart | null): HTMLElement | null {
  const loadingEl = document.getElementById(loadingId);
  if (!loadingEl || chart || isSkeletonActive(loadingEl)) return loadingEl;
  document.getElementById(canvasId)?.classList.add('hidden');
  // A previous range's empty-state text must not sit next to this load.
  document.getElementById(emptyId)?.classList.add('hidden');
  showSkeletonBlock(loadingEl, '100%', '12rem');
  return loadingEl;
}

// Shown in a chart card whose data never arrived because the dashboard load
// failed, so the card is not left as a bare heading (#1686).
export function showChartLoadFailed(emptyId: string, text: string): void {
  const empty = document.getElementById(emptyId);
  if (!empty) return;
  empty.textContent = text;
  empty.classList.remove('hidden');
}

// One colour per stacked layer, the same on every bar, so the legend swatches
// are exactly the colours drawn (#189). The x axis already names each service,
// so hue encodes the layer, not the service: no colour can change with rank
// (#700) and the layers differ by hue rather than by shades of one (#744).
// Defaults are slots 1-3 of the validated categorical palette, CVD-distinct in
// every pair; Upside (aqua) sits below 3:1 on white, so the tooltip carries its
// value. The colours are the --cudly-chart-layer-* design tokens (issue #699,
// lib/chart-theme.ts), read at render time so a theme reaches the chart.
export function savingsLayerColors(): { current: string; lowestOption: string; upside: string } {
  return {
    current: chartColor('--cudly-chart-layer-current'),
    lowestOption: chartColor('--cudly-chart-layer-lowest'),
    upside: chartColor('--cudly-chart-layer-upside'),
  };
}

/**
 * Build a short human-readable description of the active topbar filter
 * for use in empty-state messages on the Home charts. Returns '' when no
 * filter is active so callers can distinguish "unfiltered empty" from
 * "filtered empty". Mirrors buildTrendFilterDesc used in loadSavingsTrendChart.
 */
export function buildFilterDesc(provider: string, accountIDs: readonly string[]): string {
  const parts: string[] = [];
  if (provider && provider.toLowerCase() !== 'all') parts.push(provider.toUpperCase());
  if (accountIDs.length > 0) parts.push(accountIDs[0] ?? '');
  return parts.join(', ');
}
