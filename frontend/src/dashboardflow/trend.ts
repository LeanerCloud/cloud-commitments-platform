import * as api from '../api';
import * as state from '../state';
import { Chart } from 'chart.js';
import { formatCurrency } from '../utils';
import type { SavingsDataPoint } from '../api';
import { teardownSkeleton } from '../lib/skeleton';
import { formatTrendAxisTick, formatTrendTooltipTitle, formatSparseTrendMessage, MIN_TREND_POINTS, upsertChart } from '../modules/chart-utils';
import { setChartSummary, renderChartTable, removeChartTable } from '../lib/chart-a11y';
import { applyChartDefaults, chartColor, withAlpha } from '../lib/chart-theme';
import { dashboardState } from './dashboardState';
import { SAVINGS_TREND_TABLE_ID, SAVINGS_TREND_LOADING_ID, TREND_LOAD_FAILED_TEXT, showChartSkeleton, buildFilterDesc } from './chartShared';
import { watchTheme } from './themeWatch';
import { attachSparkline } from './summary';

let savingsTrendRange: '7' | '30' | '90' | 'all' = '90';
// Request generation for loadSavingsTrendChart; see the stale guard there (#747).
let savingsTrendGeneration = 0;

/**
 * Load the savings-over-time trend chart for the dashboard. Fetches the
 * history analytics endpoint with the currently-selected range and renders
 * a line chart of cumulative savings spanning the full selected window on
 * the x-axis (QA row 405, step 3.1). When the response is empty (QA 2.3
 * supersedes QA 3.1's earlier empty-axes suggestion), the canvas is hidden
 * and an empty-state banner names the range (and the active provider or
 * account chip): "No savings recorded in the last 90 days." (#1686). A single
 * data point is too sparse to draw a trend, so it gets a "Not enough history
 * yet" message carrying that point's value instead of a lone dot on a blank
 * canvas (#188). Fetch failures hide the canvas and say the load failed.
 */
export async function loadSavingsTrendChart(): Promise<void> {
  const canvas = document.getElementById('savings-trend-chart') as HTMLCanvasElement | null;
  const empty = document.getElementById('savings-trend-empty');
  if (!canvas) return;
  // #747: quick range or filter changes overlap requests; only the latest
  // may paint, or an older response landing last would show the wrong range.
  const generation = ++savingsTrendGeneration;
  const loadingEl = showChartSkeleton(SAVINGS_TREND_LOADING_ID, 'savings-trend-chart', 'savings-trend-empty', dashboardState.savingsTrendChart);

  const now = new Date();
  const nowMs = now.getTime();
  const isAllRange = savingsTrendRange === 'all';
  const days = isAllRange ? null : parseInt(savingsTrendRange, 10);
  // For 'all', use a rolling ~365-day window — the maximum history the
  // analytics API allows (~366-day cap in handler_analytics.go). Sending a
  // 1970 epoch sentinel caused HTTP 400 "date range too large" which the
  // catch block rendered as an empty-state error (QA 3.1). Because it is
  // capped, the button is labeled "1y" ("Last 365 days"), not "All" (#52).
  // The axisMinMs anchor-to-earliest-point logic below still applies so
  // sparse data fills the full chart width correctly.
  const windowStartMs = isAllRange ? nowMs - 365 * 86400_000 : nowMs - (days as number) * 86400_000;
  const intervalDays = isAllRange ? 365 : (days as number);
  const interval: 'hourly' | 'daily' | 'weekly' = intervalDays <= 7 ? 'hourly' : intervalDays <= 90 ? 'daily' : 'weekly';

  try {
    // Always forward account_ids AND provider to the chart so its data scope
    // matches the KPI tiles above it. The backend /history/analytics handler
    // reads the plural `account_ids` and sums every selected account. The
    // backend applies the provider chip as a provider WHERE filter on the
    // savings-history query, so forward it to scope the chart (issue #498,
    // QA 2.3).
    const accountIDs = state.getCurrentAccountIDs();
    const provider = state.getCurrentProvider();
    const data = await api.getSavingsAnalytics({
      start: new Date(windowStartMs).toISOString(),
      end: now.toISOString(),
      interval,
      ...(provider ? { provider } : {}),
      ...(accountIDs.length > 0 ? { account_ids: accountIDs } : {}),
    });
    if (generation !== savingsTrendGeneration) return;

    const points = data.data_points ?? [];

    // Determine the left edge of the x-axis.
    // For 'all': anchor to the earliest purchase so the line fills the
    // chart rather than clustering at the right end. Fall back to now-365d
    // when there are no purchases.
    let axisMinMs = windowStartMs;
    if (savingsTrendRange === 'all' && points.length > 0) {
      const earliest = points.reduce(
        (min: number, p: SavingsDataPoint) => Math.min(min, new Date(p.timestamp).getTime()),
        Infinity,
      );
      axisMinMs = earliest;
    }

    // Map each data point to {x: timestamp_ms, y: cumulative_savings} so
    // Chart.js positions it by its real date, not by label index.
    const chartData = points.map((p: SavingsDataPoint) => ({
      x: new Date(p.timestamp).getTime(),
      y: p.cumulative_savings || 0,
    }));

    // Policy (QA 2.3, supersedes QA 3.1 empty-axis approach):
    // - No data + active account filter: show empty-state with the filter
    //   name so the user understands why the chart is blank.
    // - No data + no filter active: name the empty range rather than show
    //   blank axes; it says nothing about older history (#1686).
    // - Data present: always show the chart (hide empty-state).
    // The stub is also shown on fetch errors (catch block below).
    if (points.length === 0) {
      canvas.classList.add('hidden');
      if (empty) {
        // Both provider and account are forwarded to the backend (see call
        // above), so naming the active filter in the empty-state is accurate
        // (issue #498). buildFilterDesc returns '' when no filter is active.
        const filterDesc = buildFilterDesc(provider, accountIDs);
        const rangeDesc = isAllRange ? 'the last year' : `the last ${days} days`;
        empty.textContent = `No savings recorded in ${rangeDesc}${filterDesc ? ` for ${filterDesc}` : ''}.`
          + (isAllRange ? '' : ' Older savings may show in the 1y range.');
        empty.classList.remove('hidden');
      }
      if (dashboardState.savingsTrendChart) { dashboardState.savingsTrendChart.destroy(); dashboardState.savingsTrendChart = null; }
      removeChartTable(SAVINGS_TREND_TABLE_ID);
      return;
    }
    // Sparse data (#188): one point would paint a lone dot on a blank canvas,
    // so show "not enough history yet" with the value we have instead.
    if (points.length < MIN_TREND_POINTS) {
      canvas.classList.add('hidden');
      if (empty) {
        empty.textContent = formatSparseTrendMessage(points[0] as SavingsDataPoint);
        empty.classList.remove('hidden');
      }
      if (dashboardState.savingsTrendChart) { dashboardState.savingsTrendChart.destroy(); dashboardState.savingsTrendChart = null; }
      // The sparse message carries the one value; drop any table left from
      // an earlier render, as the empty state does (#744).
      removeChartTable(SAVINGS_TREND_TABLE_ID);
      return;
    }
    canvas.classList.remove('hidden');
    empty?.classList.add('hidden');

    // #744: summarize the plotted series and list every point as text.
    const rangeText = isAllRange ? 'the last year' : `the last ${days} days`;
    const latest = chartData[chartData.length - 1]!;
    setChartSummary(canvas,
      `Line chart of cumulative savings over ${rangeText}: ${formatCurrency(latest.y)} `
      + `as of ${formatTrendAxisTick(latest.x, interval)}, ${chartData.length} data point${chartData.length === 1 ? '' : 's'}. `
      + 'Values are in the table below the chart.');
    renderChartTable(canvas, SAVINGS_TREND_TABLE_ID, {
      caption: `Cumulative savings over ${rangeText}`,
      columns: ['Date', 'Cumulative savings'],
      rows: chartData.map(p => [formatTrendAxisTick(p.x, interval), formatCurrency(p.y)]),
    });

    const trendColor = chartColor('--cudly-chart-1');
    applyChartDefaults(Chart.defaults);
    // #747: update in place so a range click doesn't replay the entry animation.
    watchTheme();
    dashboardState.savingsTrendChart = upsertChart(dashboardState.savingsTrendChart, canvas, {
      type: 'line',
      data: {
        datasets: [{
          label: 'Cumulative savings',
          data: chartData,
          borderColor: trendColor,
          backgroundColor: withAlpha(trendColor, 0.1),
          fill: true,
          tension: 0.25,
          pointRadius: 2,
        }],
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
          legend: { display: false },
          tooltip: {
            callbacks: {
              // Format the x timestamp as a human-readable date so the tooltip
              // header shows a date string rather than the raw 13-digit
              // millisecond value (QA 3.2). Unlike the compact axis ticks it
              // includes the year, so points a year apart don't read the same (#53).
              title: (items) => {
                const raw = items[0]?.raw as { x: number; y: number } | undefined;
                return raw?.x != null ? formatTrendTooltipTitle(raw.x, interval) : '';
              },
              label: (ctx) => `Cumulative savings: ${formatCurrency((ctx.raw as { x: number; y: number }).y)}`,
            },
          },
        },
        scales: {
          x: {
            type: 'linear',
            min: axisMinMs,
            max: nowMs,
            // Date gridlines add nothing to a cumulative line; keep the value ones, faint.
            grid: { display: false },
            ticks: {
              maxTicksLimit: 6,
              callback: (value) => formatTrendAxisTick(value as number, interval),
            },
          },
          y: {
            beginAtZero: true,
            grid: { color: chartColor('--cudly-chart-grid-faint') },
            border: { display: false },
            ticks: { callback: (v) => formatCurrency(v as number), maxTicksLimit: 6 },
          },
        },
      },
    });
  } catch (err) {
    if (generation !== savingsTrendGeneration) return;
    // Analytics is Postgres-backed now, but the endpoint still guards with
    // a 503 when the analytics client isn't wired. Don't break the rest of
    // the dashboard — hide the widget and say the load failed, so it does not
    // read as "no history" (#1686).
    console.warn('Savings trend chart unavailable:', err);
    if (dashboardState.savingsTrendChart) { dashboardState.savingsTrendChart.destroy(); dashboardState.savingsTrendChart = null; }
    removeChartTable(SAVINGS_TREND_TABLE_ID);
    canvas.classList.add('hidden');
    if (empty) {
      empty.textContent = TREND_LOAD_FAILED_TEXT;
      empty.classList.remove('hidden');
    }
  } finally {
    if (loadingEl && generation === savingsTrendGeneration) teardownSkeleton(loadingEl);
  }
}

let ytdSparklineGeneration = 0;

/**
 * Sparkline for the YTD Savings tile. It used to reuse the trend chart's
 * points, whose window follows the 7d/30d/90d/All toggle, so the line under
 * "YTD" changed shape with a control that does not change the number. It now
 * plots its own series from 1 January (UTC, the same year start the backend
 * uses for ytd_savings) to now, scoped by the same provider/account chips.
 */
export async function loadYtdSparkline(): Promise<void> {
  if (!document.querySelector('.kpi-tile-spark[data-spark-key="ytd"]')) return;
  const generation = ++ytdSparklineGeneration;
  const now = new Date();
  const yearStartMs = Date.UTC(now.getUTCFullYear(), 0, 1);
  const days = (now.getTime() - yearStartMs) / 86400_000;
  const interval: 'hourly' | 'daily' | 'weekly' = days <= 7 ? 'hourly' : days <= 90 ? 'daily' : 'weekly';
  const accountIDs = state.getCurrentAccountIDs();
  const provider = state.getCurrentProvider();
  try {
    const data = await api.getSavingsAnalytics({
      start: new Date(yearStartMs).toISOString(),
      end: now.toISOString(),
      interval,
      ...(provider ? { provider } : {}),
      ...(accountIDs.length > 0 ? { account_ids: accountIDs } : {}),
    });
    if (generation !== ytdSparklineGeneration) return;
    attachSparkline('ytd', (data.data_points ?? []).map((p: SavingsDataPoint) => p.cumulative_savings || 0));
  } catch (err) {
    if (generation !== ytdSparklineGeneration) return;
    console.warn('YTD sparkline unavailable:', err);
    attachSparkline('ytd', []);
  }
}

// Wire up the range-toggle buttons. Called once during dashboard setup.
export function setupSavingsTrendHandlers(): void {
  document.querySelectorAll<HTMLButtonElement>('.trend-range').forEach((btn) => {
    btn.addEventListener('click', () => {
      const range = btn.dataset['range'];
      if (range !== '7' && range !== '30' && range !== '90' && range !== 'all') return;
      savingsTrendRange = range;
      document.querySelectorAll('.trend-range').forEach((b) => b.classList.remove('active'));
      btn.classList.add('active');
      void loadSavingsTrendChart();
    });
  });
}
