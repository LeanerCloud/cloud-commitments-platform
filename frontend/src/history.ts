/**
 * History module for CUDly
 */


import * as api from './api';
import * as state from './state';
import { formatCurrency, escapeHtml, NO_VALUE } from './utils';
import type { HistoryResponse, HistorySummary, HistoryPurchase } from './types';
import { switchTab } from './navigation';
import { showToast } from './toast';
import { historyTruncationMessage, mergeHistoryPages, nextHistoryCursor, renderLoadMoreControl } from './history-paging';
import { canAccess } from './permissions';
import { showSkeletonRows, teardownSkeleton } from './lib/skeleton';
import { localDayRangeToUtcDays, toLocalDateInputValue } from './lib/local-date';
import { closeOpenHistoryPopover } from './lib/history-filter-popover';
import { historyState } from './historyflow/historyState';
import { VALID_PROVIDERS } from './historyflow/cells';
import { refreshFourEyesMode } from './historyflow/rowGuards';
import { syncAmortizeCheckbox } from './historyflow/amortize';
import { renderHistoryList } from './historyflow/historyList';
import { renderApprovalQueue } from './historyflow/approvalQueue';
import { historyLoadGeneration, historyPaging, loadMoreInFlight, viewPlanHistory, snapDateInputsToPurchases, loadHistory, resetHistoryPaging, updateHistoryPaging, loadMoreHistory, renderTruncationNotices, renderHistorySummary } from './history';
import { setupHistoryHandlers, initHistoryDateRange } from './history';

// Load more paging (issue #676). historyLoadGeneration is bumped by every
// first-page load, so a Load more response that lands after the user changed
// a filter (or after a mutation reload) is dropped instead of being appended
// to the new result set. historyPaging holds the filters of the first page and
// the cursor of the last page loaded; null when there is nothing more to load.
let historyLoadGeneration = 0;

let historyPaging: { filters: api.HistoryFilters; cursor: string } | null = null;

let loadMoreInFlight = false;

/**
 * Setup history filter event handlers.
 *
 * Provider/account filters are global (sourced from state.ts via the
 * topbar chips). The history-section's own controls are just the date range
 * (a change reloads) and the Refresh button; those stay here.
 */
export function setupHistoryHandlers(): void {
  for (const id of ['history-start', 'history-end']) {
    document.getElementById(id)?.addEventListener('change', () => void loadHistory());
  }
  document.getElementById('history-load-more-btn')?.addEventListener('click', () => void loadMoreHistory());
  state.subscribeProvider(() => void loadHistory());
  state.subscribeAccount(() => void loadHistory());
  // Re-render both tables when the amortize toggle flips (issue #1112).
  state.subscribeAmortizeUpfront(() => {
    renderHistoryList(historyState.lastPurchases);
    renderApprovalQueue(historyState.lastPurchases);
    syncAmortizeCheckbox('history-amortize-checkbox');
    syncAmortizeCheckbox('approval-queue-amortize-checkbox');
  });
}

/**
 * Initialize history date range.
 *
 * Defaults to 90 days, the same window as the Savings History card on the
 * same page (`#savings-period` in index.html), so both open to one span
 * (issue #1681).
 */
export function initHistoryDateRange(): void {
  const end = new Date();
  const start = new Date();
  start.setDate(start.getDate() - 90);

  const startInput = document.getElementById('history-start') as HTMLInputElement | null;
  const endInput = document.getElementById('history-end') as HTMLInputElement | null;

  if (startInput && !startInput.value) {
    startInput.value = toLocalDateInputValue(start);
  }
  if (endInput && !endInput.value) {
    endInput.value = toLocalDateInputValue(end);
  }
}

/**
 * View plan history.
 *
 * The plan-history endpoint returns the plan's *full* history regardless
 * of date range — `api.getHistory({ planId })` ignores `start`/`end`.
 * Don't seed the From/To inputs with the generic default range here:
 * they would visibly disagree with the table contents (the tab-level
 * default would suggest the table only covers the last week, while in
 * fact every purchase the plan ever recorded is shown). Instead, after
 * the fetch lands, snap the inputs to the actual min/max purchase
 * timestamps so the date pickers reflect what the user is looking at.
 *
 * If the plan has no purchases yet, leave the inputs untouched — there's
 * no meaningful range to display, and clobbering them with `today`
 * would be misleading.
 */
export async function viewPlanHistory(planId: string): Promise<void> {
  const requestEpoch = historyState.historyMutationEpoch;
  resetHistoryPaging();
  // skipDefaultLoad: the tab's own unscoped fetch would land after the
  // plan-scoped one below and overwrite it, and its date-range seeding is
  // exactly what the doc comment above says not to do here.
  switchTab('purchases', { skipDefaultLoad: true });
  // switchTab renders a no-access placeholder for sessions without
  // view:purchases; don't overwrite it with the plan's purchases.
  if (!canAccess('view', 'purchases')) return;

  try {
    const filters: api.HistoryFilters = { planId };
    const generation = historyLoadGeneration;
    const [data] = await Promise.all([
      api.getHistory(filters) as unknown as Promise<HistoryResponse>,
      refreshFourEyesMode(),
    ]);
    if (requestEpoch !== historyState.historyMutationEpoch) return;
    renderHistorySummary(data.summary ?? null);
    renderTruncationNotices(data);
    const purchases = data.purchases || [];
    renderApprovalQueue(purchases);
    renderHistoryList(purchases);
    snapDateInputsToPurchases(purchases);
    updateHistoryPaging(generation, filters, data);
  } catch (error) {
    if (requestEpoch !== historyState.historyMutationEpoch) return;
    console.error('Failed to load plan history:', error);
    renderTruncationNotices({});
    const err = error as Error;
    const list = document.getElementById('history-list');
    if (list) {
      list.innerHTML = `<p class="error">Failed to load plan history: ${escapeHtml(err.message)}</p>`;
    }
    // Mirror the loadHistory catch: clear the approval queue on error
    // so stale pending rows from a previous render don't sit on screen
    // alongside the failure message and tempt clicks on outdated state.
    const queue = document.getElementById('purchases-approval-queue');
    if (queue) {
      queue.innerHTML = `<p class="error">Failed to load approval queue: ${escapeHtml(err.message)}</p>`;
    }
  }
}

/**
 * Set the From/To inputs to bracket the purchases that just rendered.
 * No-op when the list is empty — keeping the previous values is more
 * honest than seeding a fake "today" range. Timestamps are normalised
 * to the LOCAL calendar day (the day a date input shows and the table's
 * formatted timestamps use), not the UTC day (issue #51).
 */
function snapDateInputsToPurchases(purchases: HistoryPurchase[]): void {
  if (purchases.length === 0) return;
  const epochs = purchases
    .map(p => Date.parse(p.timestamp))
    .filter(n => !Number.isNaN(n));
  if (epochs.length === 0) return;
  const startInput = document.getElementById('history-start') as HTMLInputElement | null;
  const endInput = document.getElementById('history-end') as HTMLInputElement | null;
  const minDate = toLocalDateInputValue(new Date(Math.min(...epochs)));
  const maxDate = toLocalDateInputValue(new Date(Math.max(...epochs)));
  if (startInput) startInput.value = minDate;
  if (endInput) endInput.value = maxDate;
}

/**
 * Load history with filters
 */
export async function loadHistory(): Promise<void> {
  const requestEpoch = historyState.historyMutationEpoch;
  resetHistoryPaging();
  const generation = historyLoadGeneration;
  // Issue #344 T3: skeleton rows for the purchase-history table. 8
  // rows matches the typical first-page row count so the skeleton
  // doesn't shrink dramatically when real data arrives. Column count
  // (12) mirrors the rendered table headers in renderHistoryList:
  // Status / Date / Provider / Service / Type / Region / Count /
  // Term / Upfront Cost / Monthly Cost / Monthly Savings / Plan.
  const listEl = document.getElementById('history-list');
  if (listEl) showSkeletonRows(listEl, 8, 12);
  // Pending-approval queue card (issue #340 sub-task): 3 rows x 12
  // cols matches the queue table shape (Date / Account / Provider /
  // Service / Count / Term / Payment / Monthly Cost / Upfront Cost /
  // Monthly Savings / Created by / Actions). The queue is typically
  // much shorter than the full history list, so 3 rows is a sensible
  // skeleton size.
  const queueEl = document.getElementById('purchases-approval-queue');
  if (queueEl) showSkeletonRows(queueEl, 3, 12);

  // Close any open History column-filter popover before re-rendering so the
  // popover doesn't sit anchored to a stale <th> button after the table is
  // innerHTML-rewritten. The next render with active filters rebinds fresh
  // triggers; the popover stays opt-in (user clicks again to re-open).
  closeOpenHistoryPopover();

  try {
    // Provider/account filters live in state.ts now (mutated by topbar chips).
    const rawProvider = state.getCurrentProvider();
    const provider: api.Provider | undefined = (VALID_PROVIDERS as string[]).includes(rawProvider)
      ? (rawProvider as api.Provider)
      : undefined;

    const stateAccountIDs = state.getCurrentAccountIDs();
    const accountIDs: string[] | undefined = stateAccountIDs.length > 0 ? stateAccountIDs : undefined;

    // The From/To inputs hold LOCAL calendar days, but /history filters on
    // whole UTC days. Widen to the UTC days covering the local range so an
    // off-UTC user never loses rows from the edges of the days they picked
    // (issue #51), without pushing a range the server accepts as typed
    // over its 366-day cap.
    const range = localDayRangeToUtcDays(
      (document.getElementById('history-start') as HTMLInputElement | null)?.value,
      (document.getElementById('history-end') as HTMLInputElement | null)?.value,
    );
    const filters: api.HistoryFilters = {
      start: range.start,
      end: range.end,
      provider,
      account_ids: accountIDs
    };
    const [data] = await Promise.all([
      api.getHistory(filters) as unknown as Promise<HistoryResponse>,
      refreshFourEyesMode(),
    ]);
    if (requestEpoch !== historyState.historyMutationEpoch || generation !== historyLoadGeneration) return;
    renderHistorySummary(data.summary ?? null);
    renderTruncationNotices(data);
    const purchases = data.purchases || [];
    renderApprovalQueue(purchases);
    renderHistoryList(purchases);
    updateHistoryPaging(generation, filters, data);
  } catch (error) {
    if (requestEpoch !== historyState.historyMutationEpoch || generation !== historyLoadGeneration) return;
    console.error('Failed to load history:', error);
    renderTruncationNotices({});
    const err = error as Error;
    const list = document.getElementById('history-list');
    if (list) {
      teardownSkeleton(list);
      list.innerHTML = `<p class="error">Failed to load history: ${escapeHtml(err.message)}</p>`;
    }
    const queue = document.getElementById('purchases-approval-queue');
    if (queue) {
      teardownSkeleton(queue);
      queue.innerHTML = `<p class="error">Failed to load approval queue: ${escapeHtml(err.message)}</p>`;
    }
  }
}

/**
 * Forget the previous result set's cursor and hide Load more. Called at the
 * start of every first-page load; bumping the generation also invalidates a
 * Load more request still in flight for the previous result set.
 */
function resetHistoryPaging(): void {
  historyLoadGeneration++;
  historyPaging = null;
  renderLoadMoreControl(false);
}

/** Remember where the next page starts and show Load more when there is one. */
function updateHistoryPaging(generation: number, filters: api.HistoryFilters, data: HistoryResponse): void {
  if (generation !== historyLoadGeneration) return;
  const cursor = nextHistoryCursor(data);
  historyPaging = cursor ? { filters, cursor } : null;
  renderLoadMoreControl(historyPaging !== null);
}

/**
 * Fetch the page after the last one loaded, with the first page's filters,
 * and append it (issue #676). Both tables re-render from every loaded row, so
 * the Approval Queue is built from all loaded pages. The summary cards keep
 * the first page's totals: a later page's summary covers only that page on
 * servers without whole-scope totals (#675).
 */
export async function loadMoreHistory(): Promise<void> {
  const paging = historyPaging;
  if (!paging || loadMoreInFlight) return;
  const generation = historyLoadGeneration;
  const requestEpoch = historyState.historyMutationEpoch;
  loadMoreInFlight = true;
  renderLoadMoreControl(true, true);
  try {
    const data = await (api.getHistory({ ...paging.filters, cursor: paging.cursor }) as unknown as Promise<HistoryResponse>);
    if (generation !== historyLoadGeneration) return;
    // A mutation landed mid-flight and its reload will replace the rows; keep
    // the cursor so the button still works if that reload has not started.
    if (requestEpoch !== historyState.historyMutationEpoch) {
      renderLoadMoreControl(true);
      return;
    }
    const purchases = mergeHistoryPages(historyState.lastPurchases, data.purchases || []);
    renderTruncationNotices(data);
    renderApprovalQueue(purchases);
    renderHistoryList(purchases);
    updateHistoryPaging(generation, paging.filters, data);
  } catch (error) {
    if (generation !== historyLoadGeneration) return;
    console.error('Failed to load more history:', error);
    showToast({ message: `Failed to load more history: ${(error as Error).message}`, kind: 'error' });
    renderLoadMoreControl(true);
  } finally {
    loadMoreInFlight = false;
  }
}

/**
 * Show or hide the "results capped" banner above the approval queue and the
 * history table. The API sets `truncated` when a fetch hit its row cap, in
 * which case pending approvals and the summary totals can under-report. When
 * the API can page (#674), Load more replaces the banner for rows it can reach.
 */
function renderTruncationNotices(data: HistoryResponse): void {
  const message = historyTruncationMessage(data);
  for (const id of ['purchases-truncation-notice', 'history-truncation-notice']) {
    const el = document.getElementById(id);
    if (!el) continue;
    el.textContent = message;
    el.hidden = message === '';
  }
}

function renderHistorySummary(summary: HistorySummary | null): void {
  const container = document.getElementById('history-summary');
  if (!container) return;

  // When the API omits the summary field entirely (older deploy, partial
  // response, or an error absorbed upstream), render an explicit unknown
  // state on each card rather than fabricating all-zero values that look
  // like real financial aggregates.
  if (summary === null || summary === undefined) {
    container.innerHTML = `
      <div class="card">
        <h3>Total Purchases</h3>
        <p class="value">${NO_VALUE}</p>
      </div>
      <div class="card">
        <h3>Total Upfront Spent</h3>
        <p class="value">${NO_VALUE}</p>
      </div>
      <div class="card">
        <h3>Monthly Savings</h3>
        <p class="value">${NO_VALUE}</p>
      </div>
      <div class="card">
        <h3>Annual Savings</h3>
        <p class="value">${NO_VALUE}</p>
      </div>
    `;
    return;
  }

  const total = summary.total_purchases ?? null;
  // Failed, cancelled and expired rows bought nothing, so the card counts
  // completed purchases only (matches Total Upfront Spent). An API that
  // omits total_completed (older deploy) shows the unknown marker: falling
  // back to total_purchases would count pending and failed rows as bought.
  const completed = summary.total_completed ?? null;
  const completedDisplay = completed !== null ? String(completed) : NO_VALUE;
  const pending = summary.total_pending ?? 0;
  const detail = (total !== null && pending > 0)
    ? `<p class="detail">${pending} pending</p>`
    : '';

  container.innerHTML = `
    <div class="card">
      <h3>Total Purchases</h3>
      <p class="value">${completedDisplay}</p>
      ${detail}
    </div>
    <div class="card">
      <h3>Total Upfront Spent</h3>
      <p class="value">${formatCurrency(summary.total_upfront ?? null)}</p>
    </div>
    <div class="card">
      <h3>Monthly Savings</h3>
      <p class="value savings">${formatCurrency(summary.total_monthly_savings ?? null)}</p>
    </div>
    <div class="card">
      <h3>Annual Savings</h3>
      <p class="value savings">${formatCurrency(summary.total_annual_savings ?? null)}</p>
    </div>
  `;
}

export { readDeepLinkExecutionID, applyExecutionDeepLink } from './historyflow/deepLink';
export { purchaseHistoryNumericCellValue, applyPurchaseHistoryColumnFilters } from './historyflow/historyColumns';
export { applyApprovalQueueColumnFilters, renderApprovalQueue } from './historyflow/approvalQueue';
