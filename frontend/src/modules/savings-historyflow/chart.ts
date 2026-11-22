/**
 * Savings History chart rendering (Chart.js).
 */

import { Chart, registerables } from 'chart.js';
import type { SavingsDataPoint } from '../../api';
import { formatTrendAxisTick, upsertChart } from '../chart-utils';
import { formatCompactCurrency, formatCurrency } from '../../utils';
import { setChartSummary, renderChartTable } from '../../lib/chart-a11y';
import { applyChartDefaults, chartColor, onThemeChange, withAlpha } from '../../lib/chart-theme';
import { unitSuffix, unitLabel, type SavingsUnit, type SavingsSeries } from './units';

// Register Chart.js components
Chart.register(...registerables);

// Chart instance
let savingsChart: Chart | null = null;
// Replays the last render when the color scheme changes (#699).
let stopThemeWatch: (() => void) | null = null;

export function dropSavingsChart(): void {
    stopThemeWatch?.();
    stopThemeWatch = null;
    savingsChart?.destroy();
    savingsChart = null;
}
// Id of the chart's "View as table" text alternative (#744).
export const SAVINGS_HISTORY_TABLE_ID = 'savings-history-table';

/**
 * Render savings chart using Chart.js.
 *
 * Issue #749: one series on one y-axis. Period savings are bars (each bar is
 * one interval's projected monthly additions, converted to the chosen unit);
 * the running total is a separate view picked with #savings-series. The old
 * chart drew both on two y-axes, so the points where the lines crossed had
 * no meaning. Ticks and tooltips use the shared formatters ("$1.5K" on the
 * axis, "$1,500.00" in the tooltip) instead of `$${x.toFixed(2)}`.
 *
 * BUG FIX (QA 2.2): the x-axis is `type:'linear'` anchored to the selected
 * period [start, end] so the leftmost tick is the period start, not the first
 * data point. Mirrors the approach used by the Home dashboard chart (PR #746).
 *
 * BUG FIX (QA 2.4/2.5): the y-axis has maxTicksLimit:6 to prevent tick
 * instability; the compact formatter keeps up to two fraction digits so
 * close float ticks stay distinct.
 */
export function renderSavingsChart(
    dataPoints: SavingsDataPoint[],
    interval: string,
    unit: SavingsUnit = 'monthly',
    periodStart?: Date,
    periodEnd?: Date,
    series: SavingsSeries = 'period',
): void {
    const ctx = document.getElementById('savings-history-chart') as HTMLCanvasElement;

    if (!ctx) {
        console.error('Canvas element not found: savings-history-chart');
        return;
    }

    const cumulative = series === 'cumulative';
    // The running total is a dollar amount, not a rate, so it takes no
    // unit suffix (its unit conversion is tracked separately in #134).
    const suffix = cumulative ? '' : unitSuffix(unit);

    // Determine the interval hint for the x-axis tick formatter.
    // The interval parameter can be 'hourly', 'daily', 'weekly', or 'monthly';
    // formatTrendAxisTick accepts 'hourly' | 'daily' | 'weekly' -- treat
    // 'monthly' the same as 'daily' (date-only label).
    const tickIntervalHint: 'hourly' | 'daily' | 'weekly' =
        interval === 'hourly' ? 'hourly' : interval === 'weekly' ? 'weekly' : 'daily';

    // Map each data point to {x: timestamp_ms, y: value} so Chart.js positions
    // each point at its real date on the linear axis instead of by label index.
    // M-6: use ?? 0 (not || 0) to treat a genuine 0-saving bucket correctly.
    const points = dataPoints.map(dp => ({
        x: new Date(dp.timestamp).getTime(),
        y: cumulative ? dp.cumulative_savings ?? 0 : convertFromMonthly(dp.total_savings ?? 0, unit),
    }));

    // Axis bounds: anchor to the selected period so the leftmost tick is the
    // period start, not the first data point (QA 2.2).
    const nowMs = Date.now();
    const axisMinMs = periodStart ? periodStart.getTime() : nowMs - 7 * 86400_000;
    const axisMaxMs = periodEnd ? periodEnd.getTime() : nowMs;

    const label = cumulative ? 'Cumulative Savings' : 'Period Savings';
    const periodColor = chartColor('--cudly-chart-2');
    const cumulativeColor = chartColor('--cudly-chart-cumulative');
    const dataset = cumulative
        ? {
            type: 'line' as const,
            label,
            data: points,
            borderColor: cumulativeColor,
            backgroundColor: withAlpha(cumulativeColor, 0.1),
            fill: true,
            tension: 0.3,
            pointRadius: dataPoints.length > 50 ? 0 : 3,
            pointHoverRadius: 5,
        }
        : {
            type: 'bar' as const,
            label,
            data: points,
            backgroundColor: withAlpha(periodColor, 0.7),
            borderColor: periodColor,
            borderWidth: 1,
            maxBarThickness: 24,
        };

    // #744: a value summary for the role="img" canvas plus every point as a
    // table. The table goes after the sizing container, not inside it.
    // Merged with #749: the summary names the series currently drawn, and the
    // table keeps both columns (period in the chosen unit, running total)
    // whichever series the chart shows, so no value is hidden by the toggle.
    const latest = dataPoints[dataPoints.length - 1];
    setChartSummary(ctx,
        `${cumulative ? 'Line chart of cumulative savings' : 'Bar chart of savings per period'}: `
        + `${dataPoints.length} data point${dataPoints.length === 1 ? '' : 's'}, `
        + `cumulative savings ${formatCurrency(latest?.cumulative_savings ?? 0, '$', 2)} at the latest point. `
        + 'Values are in the table below the chart.');
    renderChartTable(ctx.parentElement ?? ctx, SAVINGS_HISTORY_TABLE_ID, {
        caption: 'Savings history',
        columns: ['Date', `Period savings (${unitSuffix(unit)})`, 'Cumulative savings'],
        rows: dataPoints.map((dp) => [
            formatTrendAxisTick(new Date(dp.timestamp).getTime(), tickIntervalHint),
            formatCurrency(convertFromMonthly(dp.total_savings ?? 0, unit), '$', 2),
            formatCurrency(dp.cumulative_savings ?? 0, '$', 2),
        ]),
    });

    applyChartDefaults(Chart.defaults);
    stopThemeWatch?.();
    stopThemeWatch = onThemeChange(() => renderSavingsChart(dataPoints, interval, unit, periodStart, periodEnd, series));
    // #747: update in place so a period or unit change doesn't replay the
    // entry animation. Switching the series (#749) changes the chart type,
    // which upsertChart handles by recreating the chart.
    savingsChart = upsertChart(savingsChart, ctx, {
        type: cumulative ? 'line' : 'bar',
        data: {
            datasets: [dataset],
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            interaction: {
                intersect: false,
                mode: 'index',
            },
            scales: {
                x: {
                    type: 'linear',
                    display: true,
                    min: axisMinMs,
                    max: axisMaxMs,
                    // A bar chart pads the x-axis by default, which would
                    // push the period start off the axis edge (QA 2.2).
                    offset: false,
                    grid: {
                        display: false,
                    },
                    ticks: {
                        maxTicksLimit: 8,
                        maxRotation: 0,
                        callback: (value) => formatTrendAxisTick(value as number, tickIntervalHint),
                    },
                },
                y: {
                    type: 'linear',
                    display: true,
                    position: 'left',
                    beginAtZero: true,
                    grid: {
                        color: chartColor('--cudly-chart-grid-faint'),
                    },
                    ticks: {
                        maxTicksLimit: 6,
                        callback: function(value: number | string) {
                            const numValue = typeof value === 'string' ? parseFloat(value) : value;
                            return formatCompactCurrency(numValue);
                        },
                    },
                    title: {
                        display: true,
                        text: cumulative ? 'Cumulative savings' : `Savings per period (${unitLabel(unit)})`,
                    },
                },
            },
            plugins: {
                legend: {
                    display: false,
                },
                tooltip: {
                    callbacks: {
                        label: function(context) {
                            const raw = context.raw as ({ x: number; y: number } | number) | null | undefined;
                            const value = (raw != null && typeof raw === 'object' ? raw.y : raw as number) || 0;
                            return `${context.dataset.label}: ${formatCurrency(value, '$', 2)}${suffix}`;
                        },
                    },
                },
            },
        },
    });
}

// Export for use in other modules
export { savingsChart };
