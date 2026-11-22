/**
 * Savings History module - displays historical savings chart
 */

import { getSavingsAnalytics, type SavingsAnalyticsResponse } from '../api';
import * as state from '../state';
import { formatCurrency } from '../utils';
import { removeChartTable } from '../lib/chart-a11y';
import { convertFromMonthly, unitSuffix, unitLabel, getSelectedUnit, getSelectedSeries } from './savings-historyflow/units';
import { renderSavingsChart, dropSavingsChart, SAVINGS_HISTORY_TABLE_ID } from './savings-historyflow/chart';

export { convertFromMonthly, unitSuffix, unitLabel } from './savings-historyflow/units';
export type { SavingsUnit, SavingsSeries } from './savings-historyflow/units';
export { savingsChart } from './savings-historyflow/chart';

/**
 * Load savings history data based on selected period
 */
export async function loadSavingsHistory(): Promise<void> {
    const periodSelect = document.getElementById('savings-period') as HTMLSelectElement;
    const chartContainer = document.getElementById('savings-history-chart')?.parentElement;
    const emptyEl = document.getElementById('savings-history-empty');
    const statsEl = document.getElementById('savings-stats');

    if (!periodSelect) return;

    const period = periodSelect.value;
    const { start, end, interval } = getPeriodDates(period);

    // Honour the global topbar filter chips (issue #503). Every selected
    // account is forwarded as account_ids; the backend scope-checks each id
    // and sums them. The provider chip is forwarded too and the backend now applies it as a provider WHERE filter
    // on the savings-history query (issue #498, QA 2.3).
    const currentProvider = state.getCurrentProvider();
    const currentAccountIDs = state.getCurrentAccountIDs();

    // Build a human-readable description of the active filter so the
    // empty-state message distinguishes "no data yet" from "nothing in
    // the selected scope". Used when the API returns 0 data points.
    const filterDesc = buildFilterDesc(currentProvider, currentAccountIDs);

    try {
        const data = await getSavingsAnalytics({
            start: start.toISOString(),
            end: end.toISOString(),
            interval,
            ...(currentProvider ? { provider: currentProvider } : {}),
            ...(currentAccountIDs.length > 0 ? { account_ids: currentAccountIDs } : {}),
        });

        if (!data.data_points || data.data_points.length === 0) {
            showEmptyState(chartContainer, emptyEl, statsEl, filterDesc);
            return;
        }

        // Show chart, hide empty state
        if (chartContainer) chartContainer.classList.remove('hidden');
        if (emptyEl) emptyEl.classList.add('hidden');
        if (statsEl) statsEl.classList.remove('hidden');

        renderSavingsStats(data, start, end, interval);
        renderSavingsChart(data.data_points, interval, getSelectedUnit(), start, end, getSelectedSeries());
    } catch (error) {
        const msg = error instanceof Error ? error.message : 'Unknown error';
        console.error('Failed to load savings history:', msg);
        showErrorState(chartContainer, emptyEl, statsEl, msg);
    }
}

/**
 * Show empty state when no data is available.
 *
 * filterDesc is a short human-readable description of the active
 * topbar filter (e.g. "AWS" or "account abc-123"). When non-empty
 * the empty-state copy says "no data for the selected filter" so
 * the user understands the chart is hidden because of scoping, not
 * because no purchases exist at all (issue #701).
 */
function showEmptyState(
    chartContainer: HTMLElement | null | undefined,
    emptyEl: HTMLElement | null,
    statsEl: HTMLElement | null,
    filterDesc: string = '',
): void {
    if (chartContainer) chartContainer.classList.add('hidden');
    removeChartTable(SAVINGS_HISTORY_TABLE_ID);
    if (emptyEl) {
        emptyEl.classList.remove('hidden');
        // Update the copy inside the empty-state element so the message
        // reflects whether a filter is active. The element is always
        // present in the DOM (hidden by CSS class, not removed), so we
        // can safely write innerHTML here — this is the only place that
        // mutates it and the content is built from trusted constants.
        const heading = emptyEl.querySelector('p:first-child');
        const help = emptyEl.querySelector('p.help-text');
        if (heading) {
            heading.textContent = filterDesc
                ? `No savings data for the selected filter (${filterDesc}).`
                : 'No savings history data available yet.';
        }
        if (help) {
            help.textContent = filterDesc
                ? 'Try broadening the filter or selecting a different period.'
                : 'Data will be collected hourly once you have active purchases.';
        }
    }
    if (statsEl) statsEl.classList.add('hidden');

    // Clear any existing chart
    dropSavingsChart();
}

/**
 * Show an explicit error state when the API call fails. Reuses the empty-state
 * DOM element but sets copy that makes clear this is a fetch failure, not
 * simply a lack of data (issue #701 CR finding).
 */
function showErrorState(
    chartContainer: HTMLElement | null | undefined,
    emptyEl: HTMLElement | null,
    statsEl: HTMLElement | null,
    message: string,
): void {
    if (chartContainer) chartContainer.classList.add('hidden');
    removeChartTable(SAVINGS_HISTORY_TABLE_ID);
    if (statsEl) statsEl.classList.add('hidden');
    if (emptyEl) {
        emptyEl.classList.remove('hidden');
        const heading = emptyEl.querySelector('p:first-child');
        const help = emptyEl.querySelector('p.help-text');
        if (heading) heading.textContent = 'Failed to load savings history.';
        if (help) help.textContent = `Please retry. (${message})`;
    }
    dropSavingsChart();
}

/**
 * Build a short human-readable description of the active topbar filter
 * for use in the Savings History empty-state message. Returns '' when
 * no filter is active (no chip selected) so callers can distinguish
 * "unfiltered empty" from "filtered empty".
 */
function buildFilterDesc(provider: string, accountIDs: readonly string[]): string {
    const parts: string[] = [];
    if (provider && provider.toLowerCase() !== 'all') parts.push(provider.toUpperCase());
    if (accountIDs.length > 0) parts.push(accountIDs[0] ?? '');
    return parts.join(', ');
}

/**
 * Get start/end dates and interval based on period selection
 */
function getPeriodDates(period: string): { start: Date; end: Date; interval: 'hourly' | 'daily' } {
    const end = new Date();
    const start = new Date(end);
    let interval: 'hourly' | 'daily' = 'hourly';

    switch (period) {
        case '24h':
            start.setHours(start.getHours() - 24);
            interval = 'hourly';
            break;
        case '7d':
            start.setDate(start.getDate() - 7);
            interval = 'hourly';
            break;
        case '30d':
            start.setDate(start.getDate() - 30);
            interval = 'daily';
            break;
        case '90d':
            start.setDate(start.getDate() - 90);
            interval = 'daily';
            break;
        default:
            start.setDate(start.getDate() - 7);
            interval = 'hourly';
    }

    return { start, end, interval };
}

/**
 * Render savings statistics
 */
function renderSavingsStats(data: SavingsAnalyticsResponse, start: Date, end: Date, interval: 'hourly' | 'daily'): void {
    const periodSavingsEl = document.getElementById('period-savings');
    // The unit indicator belongs below the value, not inside the label.
    // Period Savings is the window's monthly-rate figure converted to the chosen
    // unit, so it carries the same unit suffix as the neighbouring tiles; the
    // sub-line (#period-savings-unit) names the active view mode.
    const periodSavingsLabelEl = document.getElementById('period-savings-label');
    const periodSavingsUnitEl = document.getElementById('period-savings-unit');
    const avgSavingsEl = document.getElementById('avg-hourly-savings');
    const peakSavingsEl = document.getElementById('peak-savings');
    const avgLabelEl = document.getElementById('avg-savings-label');

    const unit = getSelectedUnit();
    const suffix = unitSuffix(unit);
    const adjective = unitLabel(unit);

    const summary = data.summary;
    const dataPoints = data.data_points || [];

    // Calculate totals from data points when summary is absent.
    // Each data point's total_savings is the sum of estimated_savings
    // (a monthly figure) bucketed by the chosen interval.
    let totalSavings = 0;
    let peakSavings = 0;

    for (const dp of dataPoints) {
        // M-6: the type contract says total_savings is non-nullable, but a
        // future API change could introduce null; guard with ?? 0 only for the
        // aggregation where 0 is the correct absent interpretation (a missing
        // bucket contributes nothing to the total/peak -- not the same as a
        // fabricated $0 on a money display path).
        const savings = dp.total_savings ?? 0;
        totalSavings += savings;
        if (savings > peakSavings) {
            peakSavings = savings;
        }
    }

    // The server's window total (HistorySummary.total_monthly_savings) is
    // authoritative; the client-side bucket sum is only a fallback for a
    // missing summary. Average and peak have no wire counterpart (#276): the
    // average spreads the total over the whole requested window (empty
    // intervals included) and the peak comes from data_points. All monthly.
    const monthlyTotal = summary?.total_monthly_savings ?? totalSavings;
    const intervalMs = interval === 'hourly' ? 3600000 : 86400000;
    const periodCount = (end.getTime() - start.getTime()) / intervalMs;
    const monthlyAvg = monthlyTotal / periodCount;
    const monthlyPeak = peakSavings;

    // Convert to the user-chosen display unit.
    const displayTotal = convertFromMonthly(monthlyTotal, unit);
    const displayAvg = convertFromMonthly(monthlyAvg, unit);
    const displayPeak = convertFromMonthly(monthlyPeak, unit);

    if (periodSavingsEl) {
        periodSavingsEl.textContent = `${formatCurrency(displayTotal, '$', 2)}${suffix}`;
    }
    if (periodSavingsLabelEl) {
        periodSavingsLabelEl.textContent = 'Period savings';
    }
    if (periodSavingsUnitEl) {
        periodSavingsUnitEl.textContent = `shown in ${adjective.toLowerCase()} equivalents`;
    }
    if (avgLabelEl) {
        avgLabelEl.textContent = `Avg ${adjective.toLowerCase()} savings`;
    }
    if (avgSavingsEl) {
        avgSavingsEl.textContent = `${formatCurrency(displayAvg, '$', 2)}${suffix}`;
    }
    if (peakSavingsEl) {
        peakSavingsEl.textContent = `${formatCurrency(displayPeak, '$', 2)}${suffix}`;
    }
}

/**
 * True when the Purchases tab is the currently-visible tab. The reload-on-
 * filter-change subscriptions below skip the fetch when this is false so we
 * don't burn an API call (and a skeleton flash) for a section the user isn't
 * looking at: `switchTab('purchases')` runs loadSavingsHistory() on next
 * entry anyway.
 */
function isPurchasesTabActive(): boolean {
    return document.getElementById('purchases-tab')?.classList.contains('active') === true;
}

/**
 * Initialize savings history event listeners (issue #503).
 *
 * Wires the period dropdown + refresh button, and subscribes to the global
 * topbar filter chips so a provider/account change re-queries this chart.
 * Previously only the local controls were wired, so changing the Account
 * chip did nothing until the Purchases tab was left and re-entered.
 *
 * Mirrors the recommendations.ts pattern from PR #488:
 *   - Active-tab guard: only fire loadSavingsHistory() when the Purchases
 *     tab is active.
 *   - Coalesce duplicate reloads via queueMicrotask: the provider-change
 *     handler in topbar-filters.ts updates BOTH state slots (clear accounts
 *     then set provider, per the #185 ordering rule), firing the account-
 *     AND provider-subscribers from one user action. Without coalescing we'd
 *     kick off two loadSavingsHistory() calls back-to-back: extra API load
 *     plus a stale-overwrite risk if the first response lands after the
 *     second.
 *   - Re-check active-tab inside the microtask: a tab switch between the
 *     chip change and the microtask flush cancels the now-unneeded fetch.
 */
export function initSavingsHistory(): void {
    const periodSelect = document.getElementById('savings-period');
    const refreshBtn = document.getElementById('refresh-savings-btn');
    const unitSelect = document.getElementById('savings-unit');

    if (periodSelect) {
        periodSelect.addEventListener('change', loadSavingsHistory);
    }

    if (refreshBtn) {
        refreshBtn.addEventListener('click', loadSavingsHistory);
    }

    // Series toggle (issue #749): same dedup pattern as the unit dropdown.
    const seriesSelect = document.getElementById('savings-series') as (HTMLSelectElement & { _seriesChangeHandler?: () => void }) | null;
    if (seriesSelect) {
        if (seriesSelect._seriesChangeHandler) {
            seriesSelect.removeEventListener('change', seriesSelect._seriesChangeHandler);
        }
        seriesSelect._seriesChangeHandler = (): void => { void loadSavingsHistory(); };
        seriesSelect.addEventListener('change', seriesSelect._seriesChangeHandler);
    }

    // Wire unit dropdown. Using a named handler stored as a property so that
    // repeated calls to initSavingsHistory() don't stack duplicate listeners
    // (feedback_event_listener_dedup pattern).
    if (unitSelect) {
        const prevHandler = (unitSelect as HTMLSelectElement & { _unitChangeHandler?: () => void })._unitChangeHandler;
        if (prevHandler) {
            unitSelect.removeEventListener('change', prevHandler);
        }
        const unitChangeHandler = (): void => { void loadSavingsHistory(); };
        (unitSelect as HTMLSelectElement & { _unitChangeHandler?: () => void })._unitChangeHandler = unitChangeHandler;
        unitSelect.addEventListener('change', unitChangeHandler);
    }

    let reloadQueued = false;
    const scheduleReload = (): void => {
        if (!isPurchasesTabActive() || reloadQueued) return;
        reloadQueued = true;
        queueMicrotask(() => {
            reloadQueued = false;
            if (isPurchasesTabActive()) void loadSavingsHistory();
        });
    };
    state.subscribeProvider(scheduleReload);
    state.subscribeAccount(scheduleReload);
}
