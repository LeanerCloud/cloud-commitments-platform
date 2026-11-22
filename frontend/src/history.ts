/**
 * History module for CUDly
 */


import * as api from './api';
import * as state from './state';
import { formatCurrency, escapeHtml, escapeHtmlAttr, amortizedMonthly, NO_VALUE } from './utils';
import { historyConfirmBody } from './history-confirm-body';
import type { HistoryResponse, HistorySummary, HistoryPurchase } from './types';
import type { RevokePurchaseResult, RevokeQuote } from './api';
import { switchTab } from './navigation';
import { confirmDialog } from './confirmDialog';
import { approvalDetailsUnavailableMessage, buildApprovalDetailsBody, isPermanentDetailsError } from './approval-details';
import { showToast } from './toast';
import { formatPaymentOption, formatTermShort } from './lib/table-format';
import { serviceLabel } from './lib/service-labels';
import { historyTruncationMessage, mergeHistoryPages, nextHistoryCursor, renderLoadMoreControl } from './history-paging';
import { wireApprovalQueueLinks } from './history-queue-link';
import { canAccess } from './permissions';
import { showSkeletonRows, teardownSkeleton } from './lib/skeleton';
import { localDayRangeToUtcDays, toLocalDateInputValue } from './lib/local-date';
import { getAccountName } from './recommendations';
import { applyColumnFilters } from './lib/column-filters';
import { hasNoCommitments, isRealTerm } from './lib/purchase-row-guards';
import { openHistoryColumnPopover, renderHistoryFilterButton, closeOpenHistoryPopover } from './lib/history-filter-popover';
import type { PurchaseHistoryColumnId, ApprovalQueueColumnId } from './state';
import { historyState } from './historyflow/historyState';
import { REVOCATION_PENDING_CONFIRMATION_COPY, AWS_MARKETPLACE_FEE_PERCENT, AWS_MARKETPLACE_NET_FACTOR, AWS_MARKETPLACE_BUYER_DISCOUNT_PERCENT, AWS_MARKETPLACE_BUYER_DISCOUNT } from './historyflow/constants';
import { VALID_PROVIDERS, boughtNothing, noSavingsCell, statusFullTextHtml, normalizeStatus, isInFlightStatus, statusBadgeHTML, buildStatusChipRowHTML, MUTED_DASH, NO_RECS_HTML, termCell, historyDate, providerCell, upfrontCell, roundForDisplay } from './historyflow/cells';
import type { StatusFilter } from './historyflow/cells';
import { refreshFourEyesMode } from './historyflow/rowGuards';
import { renderPendingActionButtons, opsHintHtml, renderActionCell } from './historyflow/rowRender';
import { findHistoryRow, runHistoryRowAction } from './historyflow/rowActions';

let activeStatusFilter: StatusFilter = 'all';

// Load more paging (issue #676). historyLoadGeneration is bumped by every
// first-page load, so a Load more response that lands after the user changed
// a filter (or after a mutation reload) is dropped instead of being appended
// to the new result set. historyPaging holds the filters of the first page and
// the cursor of the last page loaded; null when there is nothing more to load.
let historyLoadGeneration = 0;

let historyPaging: { filters: api.HistoryFilters; cursor: string } | null = null;

let loadMoreInFlight = false;

// readDeepLinkExecutionID returns the value of the ?execution=<id>
// query parameter inside the current location hash, or '' when
// absent. The app uses hash routing ("#history"), so the "query" piece
// sits inside `window.location.hash` — `window.location.search` is
// empty. Parse it manually:
//   #history?execution=abc123 → 'abc123'
// Exported for unit-test coverage.
export function readDeepLinkExecutionID(): string {
  const hash = window.location.hash || '';
  const q = hash.split('?')[1];
  if (!q) return '';
  return new URLSearchParams(q).get('execution') || '';
}

// applyExecutionDeepLink scrolls the history table to the row matching
// the ?execution=<id> hash query, if any, and flashes a highlight
// class on it. Called after each loadHistory render so links from
// the Recommendations badge AND the scheduled-purchase email's
// Review & Edit button land on the right row. Returns true when a
// match was found + highlighted.
//
// Falsy paths:
//   - No execID in URL: return false silently (the common case — no
//     deeplink was requested).
//   - execID present but no matching row in the rendered list (e.g.
//     the user's date filter excludes the execution, or the row hasn't
//     been ingested yet): surface a non-blocking toast so the user
//     understands why the page didn't jump anywhere, and clear the
//     hash so a follow-up loadHistory() doesn't re-toast the same
//     miss on every re-render.
//
// Exported for unit-test coverage.
export function applyExecutionDeepLink(): boolean {
  const execID = readDeepLinkExecutionID();
  if (!execID) return false;
  // A pending purchase has a row in both the approval queue and the list. The
  // queue row carries the approve/cancel buttons (the list row only links to
  // it), so email and badge deep links land there; any other status is only in
  // the list. Explicit order, so it no longer depends on section order in the page.
  const selector = `tr[data-execution-id="${CSS.escape(execID)}"]`;
  const row = document.querySelector<HTMLTableRowElement>(`#purchases-approval-queue ${selector}`)
    ?? document.querySelector<HTMLTableRowElement>(`#history-list ${selector}`)
    ?? document.querySelector<HTMLTableRowElement>(selector);
  if (!row) {
    // Short-prefix the ID so the toast is readable but the user can
    // still cross-reference against the email if needed.
    const shortID = execID.length > 8 ? `${execID.slice(0, 8)}…` : execID;
    showToast({
      message: `Execution ${shortID} isn't in the current view — clear filters or widen the date range to find it.`,
      kind: 'info',
      timeout: 8_000,
    });
    // Drop the ?execution= from the hash so the next loadHistory()
    // (e.g. user changes a filter) doesn't fire this toast again.
    if (window.location.hash) {
      const baseHash = window.location.hash.split('?')[0] ?? '';
      window.history.replaceState({}, '', window.location.pathname + window.location.search + baseHash);
    }
    return false;
  }
  row.classList.add('history-row-highlight');
  row.scrollIntoView({ behavior: 'smooth', block: 'center' });
  // Fade the highlight after a few seconds so the row goes back to
  // normal styling — otherwise it stays yellow forever and a future
  // user click on a different row looks inconsistent.
  window.setTimeout(() => row.classList.remove('history-row-highlight'), 4000);
  return true;
}

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
 * Mount the "Amortize upfront over term" checkbox into a container element
 * (idempotent -- safe to call on every loadHistory).
 *
 * The checkbox is wired to setAmortizeUpfront so a change here is reflected
 * in all other views via the shared localStorage key + subscriber pattern.
 */
function mountAmortizeCheckbox(containerId: string, checkboxId: string): void {
  const container = document.getElementById(containerId);
  if (!container) return;
  if (document.getElementById(checkboxId)) return; // already mounted

  const wrapper = document.createElement('label');
  wrapper.className = 'amortize-toggle-label';
  wrapper.htmlFor = checkboxId;

  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.id = checkboxId;
  cb.checked = state.getAmortizeUpfront();
  cb.addEventListener('change', () => {
    state.setAmortizeUpfront(cb.checked);
  });

  wrapper.appendChild(cb);
  wrapper.appendChild(document.createTextNode(' Amortize upfront over term'));
  container.appendChild(wrapper);
}

/** Keep an already-mounted checkbox in sync when state changes externally. */
function syncAmortizeCheckbox(checkboxId: string): void {
  const cb = document.getElementById(checkboxId) as HTMLInputElement | null;
  if (cb) cb.checked = state.getAmortizeUpfront();
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

// ---------------------------------------------------------------------------
// Per-column filter wiring for the Purchase History table.
//
// The Status chip-row above stays as-is — it's an enum-driven filter with no
// natural fit for the generic set/expr column-filter shape, and the chip-row
// is the more discoverable affordance for status anyway. Column filters here
// cover the row attributes (provider/service/type/region/term/count/upfront/
// savings) — Status is intentionally excluded.
//
// Numeric extractors round to 0 decimal places to match formatCurrency's
// default (CURRENCY_DEFAULT_DIGITS), so a user typing the visible "$123"
// matches the row that displays that exact value (issue #484 contract on
// the recommendations table).
// ---------------------------------------------------------------------------

const PURCHASE_HISTORY_NUMERIC_COLUMNS: ReadonlySet<PurchaseHistoryColumnId> = new Set([
  'count', 'upfront_cost', 'savings',
]);

function purchaseHistoryCategoricalCellValue(
  p: HistoryPurchase,
  col: PurchaseHistoryColumnId,
): string {
  switch (col) {
    case 'provider':       return p.provider ?? '';
    case 'service':        return p.service ?? '';
    case 'resource_type':  return p.resource_type ?? '';
    case 'region':         return p.region ?? '';
    case 'term':           return p.term == null ? '' : String(p.term);
    case 'count':
    case 'upfront_cost':
    case 'savings':        return '';
  }
}

export function purchaseHistoryNumericCellValue(
  p: HistoryPurchase,
  col: PurchaseHistoryColumnId,
): number {
  switch (col) {
    case 'count':         return p.count ?? 0;
    case 'upfront_cost':  return p.upfront_cost ?? Number.NaN;
    case 'savings':       return boughtNothing(p) ? 0 : p.estimated_savings ?? Number.NaN;
    case 'provider':
    case 'service':
    case 'resource_type':
    case 'region':
    case 'term':          return Number.NaN;
  }
}

export function applyPurchaseHistoryColumnFilters(
  purchases: readonly HistoryPurchase[],
  filters: state.PurchaseHistoryColumnFilters,
): HistoryPurchase[] {
  return applyColumnFilters<HistoryPurchase, PurchaseHistoryColumnId>(
    purchases,
    filters,
    {
      categorical: purchaseHistoryCategoricalCellValue,
      numeric: (p, col) => roundForDisplay(purchaseHistoryNumericCellValue(p, col)),
    },
  );
}

const PURCHASE_HISTORY_LABELS: Record<PurchaseHistoryColumnId, string> = {
  provider: 'Provider',
  service: 'Service',
  resource_type: 'Type',
  region: 'Region',
  term: 'Term',
  count: 'Count',
  upfront_cost: 'Upfront Cost',
  savings: 'Monthly Savings',
};

function purchaseHistoryDistinctValues(
  purchases: readonly HistoryPurchase[],
  column: PurchaseHistoryColumnId,
): string[] {
  const seen = new Set<string>();
  for (const p of purchases) {
    seen.add(purchaseHistoryCategoricalCellValue(p, column));
  }
  return Array.from(seen).sort((a, b) => {
    if (a === '' && b !== '') return -1;
    if (a !== '' && b === '') return 1;
    return a.localeCompare(b);
  });
}

function purchaseHistoryDisplayLabel(
  column: PurchaseHistoryColumnId,
  value: string,
): string {
  if (value === '') return '(empty)';
  if (column === 'term') {
    // Match the cell: a 0 term renders as a dash, never "0 Years" (#181).
    const n = Number(value);
    return isRealTerm(n) ? formatTermShort(n) : Number.isFinite(n) ? NO_VALUE : value;
  }
  if (column === 'service') return serviceLabel(value);
  return value;
}

// Per-table namespaces for the shared filter popover (issue #281): both
// tables render buttons with the same bare column ids, so the popover keys
// and resolves its anchor by (scope, column).
const PURCHASE_HISTORY_FILTER_SCOPE = 'history';

const APPROVAL_QUEUE_FILTER_SCOPE = 'queue';

function wirePurchaseHistoryFilterButtons(
  container: HTMLElement,
  // The pre-column-filter slice — popover lists distinct values from
  // every row that survived the status chip, NOT the further-narrowed
  // visible slice (otherwise the popover would lose values the user just
  // unchecked).
  sourceRows: readonly HistoryPurchase[],
): void {
  container.querySelectorAll<HTMLButtonElement>('.history-column-filter-btn').forEach((btn) => {
    const column = btn.dataset['column'] as PurchaseHistoryColumnId | undefined;
    if (!column) return;
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const isNumeric = PURCHASE_HISTORY_NUMERIC_COLUMNS.has(column);
      const filters = state.getPurchaseHistoryColumnFilters();
      openHistoryColumnPopover<PurchaseHistoryColumnId>({
        scope: PURCHASE_HISTORY_FILTER_SCOPE,
        column,
        anchor: btn,
        currentFilter: filters[column],
        headerLabel: PURCHASE_HISTORY_LABELS[column],
        kind: isNumeric ? 'numeric' : 'categorical',
        distinctValues: isNumeric ? undefined : purchaseHistoryDistinctValues(sourceRows, column),
        displayLabel: (v) => purchaseHistoryDisplayLabel(column, v),
        onCommit: (filter) => {
          state.setPurchaseHistoryColumnFilter(column, filter);
          renderHistoryList(historyState.lastPurchases);
        },
      });
    });
  });
}

function renderHistoryList(purchases: HistoryPurchase[]): void {
  const container = document.getElementById('history-list');
  if (!container) return;

  historyState.lastPurchases = purchases;

  // Issue #923: inject a read-only notice for sessions that lack the
  // carved-out spending verbs the buttons in this table need. Approve /
  // Retry are gated by canApprovePendingRow / canRetryFailedRow which
  // call canAccess('approve-any','purchases') and
  // canAccess('retry-any','purchases'); use the same predicate here so
  // the banner stays in lockstep with the visible buttons (a user who
  // holds either verb via a custom group sees no contradictory
  // notice).
  const canApproveAny = canAccess('approve-any', 'purchases');
  const canRetryAny = canAccess('retry-any', 'purchases');
  const hasAnyCarvedOut = canApproveAny || canRetryAny;
  const existingBanner = document.getElementById('history-no-purchaser-banner');
  if (!existingBanner && !hasAnyCarvedOut) {
    const banner = document.createElement('div');
    banner.id = 'history-no-purchaser-banner';
    banner.className = 'info-banner';
    banner.setAttribute('role', 'note');
    banner.textContent =
      'You can view and plan, but not execute purchases directly. ' +
      'Ask an admin to add you to the Purchaser group (Admin → Users) to execute purchases.';
    container.parentElement?.insertBefore(banner, container);
  } else if (existingBanner && hasAnyCarvedOut) {
    existingBanner.remove();
  }

  // Reset the filter when the dataset changes so the user isn't stuck on an
  // empty "Cancelled" slice after reloading with a fresh query.
  if (activeStatusFilter !== 'all' && !purchases.some(p => {
    const s = normalizeStatus(p).toLowerCase();
    if (activeStatusFilter === 'pending') return s === 'pending' || s === 'notified' || isInFlightStatus(s);
    if (activeStatusFilter === 'completed') return s === 'completed' || s === 'partially_completed' || !p.status;
    // Migration 000089: the Cancelled chip key is 'cancelled' (British, kept
    // stable for URL/state compatibility) but it must surface BOTH spellings
    // during the expand-contract deploy window so new 'canceled' rows aren't
    // hidden from the filter.
    if (activeStatusFilter === 'cancelled') return s === 'cancelled' || s === 'canceled';
    return s === activeStatusFilter;
  })) {
    activeStatusFilter = 'all';
  }

  if (!purchases || purchases.length === 0) {
    container.innerHTML = '<p class="empty">No purchase history found for the selected period.</p>';
    mountAmortizeCheckbox('history-controls', 'history-amortize-checkbox');
    applyExecutionDeepLink();
    return;
  }

  const statusFiltered = purchases.filter(p => {
    if (activeStatusFilter === 'all') return true;
    const s = normalizeStatus(p).toLowerCase();
    if (activeStatusFilter === 'pending') return s === 'pending' || s === 'notified' || isInFlightStatus(s);
    if (activeStatusFilter === 'completed') return s === 'completed' || s === 'partially_completed' || !p.status;
    // Migration 000089: surface BOTH spellings under the Cancelled chip during
    // the expand-contract deploy window (see the equivalent guard above).
    if (activeStatusFilter === 'cancelled') return s === 'cancelled' || s === 'canceled';
    return s === activeStatusFilter;
  });

  // Apply the per-column filters AFTER the status chip filter so the
  // categorical popover lists only values present in the active status
  // slice (e.g. filtering by "Failed" only shows providers/services that
  // have failed rows).
  const colFilters = state.getPurchaseHistoryColumnFilters();
  const visible = applyPurchaseHistoryColumnFilters(statusFiltered, colFilters);

  const amortize = state.getAmortizeUpfront();
  const tableRows = visible.map(p => {
    const statusCell = (() => {
      const badge = statusBadgeHTML(normalizeStatus(p));
      const s = normalizeStatus(p).toLowerCase();
      if ((s === 'pending' || s === 'notified') && p.approver) {
        return `${badge}<div class="history-approver">awaiting approval from <strong>${escapeHtml(p.approver)}</strong></div>`;
      }
      if (p.status_description) {
        return `${badge}<div class="history-approver history-status-desc" title="${escapeHtmlAttr(p.status_description)}">${escapeHtml(p.status_description)}</div>${statusFullTextHtml(p.status_description)}${opsHintHtml(p)}`;
      }
      return `${badge}${opsHintHtml(p)}`;
    })();
    const execIdAttr = p.purchase_id ? ` data-execution-id="${escapeHtmlAttr(p.purchase_id)}"` : '';
    const planCellContent = renderActionCell(p);
    const rawMonthly = p.monthly_cost != null ? p.monthly_cost : null;
    const displayMonthly = (rawMonthly != null && amortize)
      ? (p.upfront_cost == null ? null : amortizedMonthly(rawMonthly, p.upfront_cost, p.term))
      : rawMonthly;
    const monthlyCostCell = displayMonthly != null
      ? formatCurrency(displayMonthly)
      : `<span class="muted">${NO_VALUE}</span>`;
    const empty = hasNoCommitments(p);
    const dashOr = (html: string): string => (empty ? MUTED_DASH : html);
    return `
      <tr${execIdAttr}>
        <td data-label="Status">${statusCell}</td>
        <td data-label="Date">${historyDate(p.timestamp)}</td>
        <td data-label="Provider">${providerCell(p)}</td>
        <td data-label="Service">${dashOr(escapeHtml(p.service ? serviceLabel(p.service) : p.service))}</td>
        <td data-label="Type">${empty ? NO_RECS_HTML : escapeHtml(p.resource_type)}</td>
        <td data-label="Region">${dashOr(escapeHtml(p.region))}</td>
        <td class="num" data-label="Count">${dashOr(escapeHtml(String(p.count ?? '')))}</td>
        <td data-label="Term">${termCell(p)}</td>
        ${empty ? `<td class="num" data-label="Upfront Cost">${MUTED_DASH}</td>` : upfrontCell(p, amortize)}
        <td class="num" data-label="${amortize ? 'Monthly Cost (amortized)' : 'Monthly Cost'}">${dashOr(monthlyCostCell)}</td>
        ${noSavingsCell(p, empty)}
        <td class="history-plan-cell" data-label="Plan">${planCellContent}</td>
      </tr>
    `;
  }).join('');

  const monthlyColHeader = amortize ? 'Monthly Cost (amortized)' : 'Monthly Cost';
  const fbtn = (col: PurchaseHistoryColumnId): string => renderHistoryFilterButton(
    col, PURCHASE_HISTORY_LABELS[col], colFilters[col] != null, PURCHASE_HISTORY_FILTER_SCOPE,
  );
  const markup = `
    ${buildStatusChipRowHTML(purchases, activeStatusFilter)}
    <table class="table-compact table-sticky-end table-cards-mobile">
      <thead>
        <tr>
          <th>Status</th>
          <th>Date</th>
          <th><span>Provider</span>${fbtn('provider')}</th>
          <th><span>Service</span>${fbtn('service')}</th>
          <th><span>Type</span>${fbtn('resource_type')}</th>
          <th><span>Region</span>${fbtn('region')}</th>
          <th class="num"><span>Count</span>${fbtn('count')}</th>
          <th><span>Term</span>${fbtn('term')}</th>
          <th class="num"><span>Upfront Cost</span>${fbtn('upfront_cost')}</th>
          <th class="num">${escapeHtml(monthlyColHeader)}</th>
          <th class="num"><span>Monthly Savings</span>${fbtn('savings')}</th>
          <th>Plan</th>
        </tr>
      </thead>
      <tbody>
        ${statusFiltered.length > 0 && visible.length === 0 ? `<tr><td colspan="12" class="empty">No rows match the active column filters. Clear a filter to see them.</td></tr>` : ''}
        ${tableRows}
      </tbody>
    </table>
  `;
  container.innerHTML = markup;

  // Mount the amortize checkbox into the controls area (idempotent).
  mountAmortizeCheckbox('history-controls', 'history-amortize-checkbox');
  wirePurchaseHistoryFilterButtons(container, statusFiltered);

  container.querySelectorAll<HTMLButtonElement>('.status-chip[data-history-status]').forEach(btn => {
    btn.addEventListener('click', () => {
      const next = btn.dataset['historyStatus'] as StatusFilter | undefined;
      if (!next || next === activeStatusFilter) return;
      activeStatusFilter = next;
      renderHistoryList(historyState.lastPurchases);
    });
  });

  wireRowActionHandlers(container);
  wireApprovalQueueLinks(container);

  // Scroll + flash the deep-link target if the URL hash carries a
  // ?execution=<id>. The suppression badge on the Recommendations
  // view links here so the user lands on the relevant row without
  // scrolling through the whole list.
  applyExecutionDeepLink();
}

// wireRowActionHandlers binds the Approve / Cancel / Retry click handlers
// against the buttons inside `container`. Scoped to the container (NOT
// the document) so multiple mounted lists (Purchase History table +
// Approval queue card) don't cross-fire: clicking the queue's Approve
// button binds and dispatches against the queue's button instance only.
//
// All three handlers terminate with a `loadHistory()` reload on success,
// which re-renders BOTH the history table and the queue card from the
// same fetched dataset. That means a successful approve from the queue
// card removes the row from BOTH views in one shot.
function wireRowActionHandlers(container: HTMLElement): void {
  // Wire the inline Approve button on pending rows the current session
  // may approve (issue #286). One flow: confirmDialog → POST /approve
  // (no token — bearer-session auth on apiRequest) → reload + toast.
  // Backend may still 409 on a status race (concurrent cancel landed
  // first); the catch surfaces the structured detail.
  container.querySelectorAll<HTMLButtonElement>('.history-approve-btn[data-approve-id]').forEach(btn => {
    const id = btn.dataset['approveId'];
    if (!id) return;
    btn.addEventListener('click', () => runHistoryRowAction(btn, id, async markMutationSucceeded => {
      // Issue #374: show the per-rec details (service / engine /
      // resource / region / count / term + payment / costs) in the
      // modal so the user has informed consent before authorising a
      // financial commitment. Without them, Approve is not offered (#247).
      let detailsBody: HTMLElement;
      try {
        detailsBody = await buildApprovalDetailsBody(id);
      } catch (detailsError) {
        console.error('Failed to load purchase details for approval:', detailsError);
        const permanent = isPermanentDetailsError(detailsError);
        showToast({
          message: permanent ? approvalDetailsUnavailableMessage(detailsError) : `${approvalDetailsUnavailableMessage(detailsError)} Try again.`,
          kind: 'error',
          timeout: null,
        });
        return;
      }
      const ok = await confirmDialog({
        title: 'Approve this pending purchase?',
        body: detailsBody,
        confirmLabel: 'Approve purchase',
        destructive: false,
      });
      if (!ok) return;
      try {
        await api.approvePurchase(id);
        markMutationSucceeded();
      } catch (approveError) {
        console.error('Failed to approve pending purchase:', approveError);
        const err = approveError as Error;
        showToast({ message: `Failed to approve: ${err.message || 'unknown error'}`, kind: 'error', timeout: null });
        return;
      }
      showToast({ message: 'Purchase approved', kind: 'success', timeout: 5_000 });
    }));
  });

  // Wire the inline Cancel button on pending/notified rows the current
  // session may cancel (issue #46). confirmDialog → POST → reload. The
  // backend remains the security boundary; the canCancelPendingRow
  // helper above is a UX gate that hides the button when the call would
  // 403, but a stale cache could still surface a 403 — handle it the
  // same way as any other failure.
  //
  // The cancel POST and the follow-up reload are split into separate
  // try/catch blocks: a successful cancel + failed reload must not show
  // a "Failed to cancel" toast (the purchase IS cancelled), and the
  // user should see success-toast first so they don't think their
  // click was lost while we re-fetch the table.
  container.querySelectorAll<HTMLButtonElement>('.history-cancel-btn[data-cancel-id]').forEach(btn => {
    const id = btn.dataset['cancelId'];
    if (!id) return;
    btn.addEventListener('click', () => runHistoryRowAction(btn, id, async markMutationSucceeded => {
      const ok = await confirmDialog({
        title: 'Cancel this pending purchase?',
        // Issue #728: name the purchase so the right row is confirmed.
        body: historyConfirmBody(findHistoryRow(id), 'This will permanently abort the approval flow. The pending email approval link will stop working. This action cannot be undone.'),
        confirmLabel: 'Cancel purchase',
        destructive: true,
      });
      if (!ok) return;
      try {
        await api.cancelPurchase(id);
        markMutationSucceeded();
      } catch (cancelError) {
        console.error('Failed to cancel pending purchase:', cancelError);
        const err = cancelError as Error;
        showToast({ message: `Failed to cancel: ${err.message || 'unknown error'}`, kind: 'error', timeout: null });
        return;
      }
      showToast({ message: 'Purchase cancelled', kind: 'success', timeout: 5_000 });
    }));
  });

  // Wire the inline Retry button on failed rows the current session
  // may retry (issue #47). Two flows:
  //   * normal:    confirmDialog → POST /retry → reload + toast.
  //   * over-threshold: confirmDialog with stronger warning → POST
  //     with ?force=true → reload + toast.
  // The backend may still 409 with an ops_hint or threshold response;
  // the catch block surfaces the structured detail when present.
  container.querySelectorAll<HTMLButtonElement>('.history-retry-btn[data-retry-id]').forEach(btn => {
    const id = btn.dataset['retryId'];
    if (!id) return;
    btn.addEventListener('click', () => runHistoryRowAction(btn, id, async markMutationSucceeded => {
      const overThreshold = btn.classList.contains('history-retry-over-threshold');
      const ok = await confirmDialog({
        title: overThreshold ? 'Retry past threshold?' : 'Retry this failed purchase?',
        body: historyConfirmBody(findHistoryRow(id), overThreshold
          ? 'The same recommendations have already failed multiple times. Are you sure you want to retry again? This may not succeed.'
          : 'This will create a new purchase execution from the same recommendations. The original failed row will be linked to the new attempt.'),
        confirmLabel: overThreshold ? 'Retry anyway' : 'Retry purchase',
        destructive: false,
      });
      if (!ok) return;
      let retryResult: Awaited<ReturnType<typeof api.retryPurchase>>;
      try {
        retryResult = await api.retryPurchase(id, overThreshold ? { force: true } : undefined);
        markMutationSucceeded();
      } catch (retryError) {
        console.error('Failed to retry purchase:', retryError);
        // Surface structured retry hints from the backend (issue #47):
        //   * ops_hint — operator-actionable reason; takes priority
        //   * retry_attempt_n + threshold — soft-block message
        //   * else — fall back to the raw error message
        const err = retryError as Error & { details?: Record<string, unknown> };
        const opsHint = typeof err.details?.['ops_hint'] === 'string' ? err.details['ops_hint'] : '';
        const retryAttemptN = typeof err.details?.['retry_attempt_n'] === 'number' ? err.details['retry_attempt_n'] : undefined;
        const threshold = typeof err.details?.['threshold'] === 'number' ? err.details['threshold'] : undefined;
        // A Redshift purchase that failed minutes ago may still be settling at AWS (not overridable by force).
        const retryAfterSeconds = typeof err.details?.['retry_after_seconds'] === 'number' ? err.details['retry_after_seconds'] : undefined;
        if (!opsHint && retryAfterSeconds != null) {
          const minutes = Math.max(1, Math.ceil(retryAfterSeconds / 60));
          showToast({
            message: `Retry is not possible yet: AWS may still be settling this purchase. Try again in ${minutes} minute${minutes === 1 ? '' : 's'}.`,
            kind: 'warning',
          });
          return;
        }
        let detailMessage = '';
        if (opsHint) {
          detailMessage = opsHint;
        } else if (retryAttemptN != null && threshold != null) {
          detailMessage = `already retried ${retryAttemptN} times (threshold ${threshold}) — confirm the override prompt to force`;
        }
        const finalMessage = detailMessage || err.message || 'unknown error';
        showToast({ message: `Failed to retry: ${finalMessage}`, kind: 'error', timeout: null });
        return;
      }
      // Gate the toast on the approval email outcome reported by the backend.
      // email_sent===false is an explicit failure signal that overrides any
      // status-based inference; show a warning even when status==='pending'.
      // email_sent===true or a pending/notified status (with email_sent absent)
      // means the approval request is in the queue.
      const emailExplicitlyFailed = retryResult.email_sent === false;
      const emailOk = !emailExplicitlyFailed && (
        retryResult.email_sent === true
        || retryResult.status === 'pending'
        || retryResult.status === 'notified'
      );
      if (emailOk) {
        showToast({ message: 'Purchase request sent for approval', kind: 'success', timeout: 5_000 });
      } else {
        showToast({ message: 'Retry created but approval email failed - check your notification settings', kind: 'warning', timeout: 8_000 });
      }
    }));
  });

  // Wire the inline Revoke button on completed Azure rows within the
  // free-cancel window (issue #290) and on scheduled rows of any provider
  // (#924). quote -> confirmDialog -> POST -> reload.
  // Completed rows fetch the Azure refund quote first so the user consents to
  // a concrete amount, which the backend re-checks before issuing the refund.
  // The backend is the security boundary; canRevokeCompletedRow is a
  // UX gate that hides the button when the call would fail, but a stale
  // cache can still surface a 4xx -- handle it like any other failure.
  container.querySelectorAll<HTMLButtonElement>('.history-revoke-btn[data-revoke-id]').forEach(btn => {
    const id = btn.dataset['revokeId'];
    if (!id) return;
    btn.addEventListener('click', () => runHistoryRowAction(btn, id, async markMutationSucceeded => {
      let quote: RevokeQuote | undefined;
      if (btn.dataset['revokeScheduled'] !== 'true') {
        try {
          quote = await api.getRevokeQuote(id);
        } catch (quoteError) {
          console.error('Failed to get refund quote:', quoteError);
          const err = quoteError as Error;
          showToast({ message: `Failed to get refund quote: ${err.message || 'unknown error'}`, kind: 'error' });
          return;
        }
      }
      // A scheduled row has not reached any cloud yet, so its copy must not
      // promise an Azure return: it may be an AWS or GCP purchase (#924).
      const body = quote
        ? `This will request an Azure reservation return. Azure will refund ${quote.refund_amount.toFixed(2)} ${quote.refund_currency}.`
        : 'This will cancel the scheduled purchase. It has not been submitted yet, so nothing will be charged.';
      const ok = await confirmDialog({
        title: 'Revoke this purchase within the free-cancel window?',
        body: `${body} This action cannot be undone.`,
        confirmLabel: 'Revoke purchase',
        destructive: true,
      });
      if (!ok) return;
      let result: RevokePurchaseResult;
      try {
        result = await api.revokePurchase(id, quote);
        markMutationSucceeded();
      } catch (revokeError) {
        console.error('Failed to revoke purchase:', revokeError);
        const err = revokeError as Error;
        showToast({ message: `Failed to revoke: ${err.message || 'unknown error'}`, kind: 'error', timeout: null });
        return;
      }
      if (result.code === 'REVOCATION_PENDING_CONFIRMATION') {
        // Not proven either way: say so, and do not call it a success (#1522).
        showToast({ message: REVOCATION_PENDING_CONFIRMATION_COPY, kind: 'info' });
        return;
      }
      if (result.code === 'RECONCILE_PENDING') {
        showToast({ message: result.message || 'Refund issued. We will reconcile your audit record shortly.', kind: 'warning' });
        return;
      }
      showToast({ message: 'Purchase revocation submitted', kind: 'success', timeout: 5_000 });
    }));
  });

  // Wire Sell on Marketplace button (issue #292).
  // Flow: pricing/schedule modal (RI summary + default price + 12% fee) →
  // user confirms → createMarketplaceListing. We never skip the pricing
  // modal (CR finding: going straight from confirmDialog to the API call
  // denies the user informed consent about the price and fee).
  container.querySelectorAll<HTMLButtonElement>('.history-marketplace-sell-btn[data-marketplace-sell-id]').forEach(btn => {
    btn.addEventListener('click', async () => {
      const id = btn.dataset['marketplaceSellId'];
      if (!id) return;

      // Look up the purchase record so we can show a meaningful price summary.
      const purchase = historyState.lastPurchases.find(p => p.purchase_id === id);

      if (!purchase || purchase.upfront_cost == null) {
        btn.disabled = true;
        showToast({ message: 'Upfront cost is unknown; marketplace pricing is unavailable.', kind: 'error' });
        return;
      }

      const upfront = purchase.upfront_cost;
      return runHistoryRowAction(btn, id, async markMutationSucceeded => {
        // Build a pricing modal body with RI summary and fee breakdown.
        const bodyEl = document.createElement('div');
        bodyEl.className = 'marketplace-pricing-modal-body';

        if (purchase) {
          // purchase_history.term is stored in YEARS (1 or 3); convert to months
          // before computing the remaining term and residual so the price summary
          // shown to the user reflects real remaining value rather than ~1/3 of it
          // (a 3-year RI was previously treated as 3 months). Mirrors the
          // row.Term * 12 conversion in internal/api/handler_marketplace.go.
          const termYears = typeof purchase.term === 'number' ? purchase.term : Number(purchase.term) || 0;
          const termMonths = termYears > 0 ? termYears * 12 : 0;
          const purchaseMs = new Date(purchase.timestamp).getTime();
          const elapsedMonths = Number.isFinite(purchaseMs)
            ? (Date.now() - purchaseMs) / (1000 * 60 * 60 * 24 * 30.4375)
            : 0;
          // Must match computeRemainingMonths in internal/api/handler_marketplace.go
          // EXACTLY: floor (not round), floored at 1 (not 0), so the number
          // shown here is the same number the backend actually lists at.
          const remainingMonths = Math.max(1, Math.floor(termMonths - elapsedMonths));
          const count = purchase.count > 0 ? purchase.count : 1;
          // Mirror marketplaceResidualPerUnit + resolveMarketplacePriceSchedule's
          // default branch in internal/api/handler_marketplace.go EXACTLY, so
          // this preview can never diverge from what the backend actually lists:
          //   - upfront-only: recurring (monthly) cost is deliberately excluded
          //     because the buyer assumes the recurring obligation post-transfer;
          //   - per instance: upfront_cost is the row total for `count` instances,
          //     but the AWS Marketplace price is per instance, so divide by count;
          //   - prorated: the upfront residual is scaled by remaining/original
          //     term (a 36-month RI at month 6 retains only 30/36 of its value);
          //   - zero when unpriceable: a no-upfront RI (upfront <= 0) or an
          //     unknown term (termMonths <= 0) has no residual to prorate, which
          //     is exactly when the backend now rejects the default schedule
          //     with an error instead of silently listing at $0.
          const perUnitResidual = termMonths > 0 && upfront > 0
            ? (upfront * (remainingMonths / termMonths)) / count
            : 0;
          const listPricePerUnit = perUnitResidual * AWS_MARKETPLACE_BUYER_DISCOUNT;
          const listPriceTotal = listPricePerUnit * count;
          const netProceedsTotal = listPriceTotal * AWS_MARKETPLACE_NET_FACTOR;

          const summaryEl = document.createElement('dl');
          summaryEl.className = 'marketplace-pricing-summary';
          const addRow = (label: string, value: string): void => {
            const dt = document.createElement('dt');
            dt.textContent = label;
            const dd = document.createElement('dd');
            dd.textContent = value;
            summaryEl.appendChild(dt);
            summaryEl.appendChild(dd);
          };
          addRow('RI ID', id);
          addRow('Region', purchase.region || '-');
          addRow('Resource type', purchase.resource_type || '-');
          addRow('Remaining term', remainingMonths === 1 ? '1 month' : `${remainingMonths} months`);
          if (listPricePerUnit > 0) {
            // Show cents, and derive the fee from the two rounded totals, so
            // the three lines always reconcile (fee + net = list price). The
            // dashboard's whole-dollar default rounded each line on its own,
            // e.g. $101 = $12 + $88 for a $100.50 listing (#318).
            const listCents = Math.round(listPriceTotal * 100);
            const netCents = Math.round(netProceedsTotal * 100);
            addRow('Default list price', count > 1
              ? `${formatCurrency(listPricePerUnit, '$', 2)}/unit (${formatCurrency(listCents / 100, '$', 2)} total for ${count} units)`
              : formatCurrency(listCents / 100, '$', 2));
            addRow(`AWS fee (${AWS_MARKETPLACE_FEE_PERCENT}%)`, formatCurrency((listCents - netCents) / 100, '$', 2));
            addRow('Estimated net proceeds', formatCurrency(netCents / 100, '$', 2));
          } else {
            // No default price can be computed (no upfront cost or unknown
            // term) -- listing will be rejected server-side unless a custom
            // price_schedule is supplied. Say so instead of showing a
            // misleading $0 or fabricated price.
            addRow('Default list price', 'unavailable (no upfront cost or unknown term)');
          }
          bodyEl.appendChild(summaryEl);
        }

        const noteEl = document.createElement('p');
        noteEl.className = 'marketplace-pricing-note';
        noteEl.textContent = `AWS charges a ${AWS_MARKETPLACE_FEE_PERCENT}% transaction fee on proceeds. The default schedule prices the listing at ${AWS_MARKETPLACE_BUYER_DISCOUNT_PERCENT}% below remaining value. You can adjust pricing by contacting your administrator or modifying the schedule via the API. This action cannot be undone without cancelling the listing.`;
        bodyEl.appendChild(noteEl);

        const ok = await confirmDialog({
          title: 'List this RI on the AWS Marketplace?',
          body: bodyEl,
          confirmLabel: 'Confirm listing',
          destructive: false,
        });
        if (!ok) return;

        try {
          await api.createMarketplaceListing(id);
          markMutationSucceeded();
        } catch (sellError) {
          console.error('Failed to list RI on Marketplace:', sellError);
          const err = sellError as Error;
          showToast({ message: `Failed to list on Marketplace: ${err.message || 'unknown error'}`, kind: 'error' });
          return;
        }
        showToast({ message: 'RI listed on Marketplace successfully', kind: 'success', timeout: 5_000 });
      });
    });
  });

  // Wire Cancel listing button (issue #292)
  container.querySelectorAll<HTMLButtonElement>('.history-marketplace-cancel-btn[data-marketplace-cancel-id]').forEach(btn => {
    const id = btn.dataset['marketplaceCancelId'];
    if (!id) return;
    btn.addEventListener('click', () => runHistoryRowAction(btn, id, async markMutationSucceeded => {
      const ok = await confirmDialog({
        title: 'Cancel this Marketplace listing?',
        body: 'This will remove the listing from the AWS Marketplace. Any existing buyer negotiations will be cancelled. You can relist the RI at any time.',
        confirmLabel: 'Cancel listing',
        destructive: true,
      });
      if (!ok) return;
      try {
        await api.cancelMarketplaceListing(id);
        markMutationSucceeded();
      } catch (cancelError) {
        console.error('Failed to cancel Marketplace listing:', cancelError);
        const err = cancelError as Error;
        showToast({ message: `Failed to cancel listing: ${err.message || 'unknown error'}`, kind: 'error' });
        return;
      }
      showToast({ message: 'Marketplace listing cancelled', kind: 'success', timeout: 5_000 });
    }));
  });
}

// isPendingRow returns true when a history row represents a purchase
// awaiting approval. The Approval queue card filters on this predicate.
// Mirrors the badge / button-eligibility logic elsewhere in this file:
// "pending" is the freshly-created state, "notified" is the post-SES
// state; both render the same Approve / Cancel affordances.
function isPendingRow(p: HistoryPurchase): boolean {
  const s = (p.status || '').toLowerCase();
  return s === 'pending' || s === 'notified';
}

// renderApprovalQueue paints the pending-approval card at the top of the
// Purchases tab (issue #340 sub-task). Filters the full history slice
// down to pending|notified rows and renders a compact action-focused
// table. When the filtered slice is empty, the card shows a friendly
// "No pending approvals" message so the section stays visible (stable
// layout, screen-reader-discoverable) without rendering an empty table.
//
// The row-action buttons reuse renderPendingActionButtons and the click
// handlers are wired by the shared wireRowActionHandlers helper, so
// approving from the queue card runs the exact same flow as approving
// from the history table (confirmDialog → API → toast → reload). The
// reload re-renders both views from one fetch, which removes the
// approved row from BOTH lists in one shot.
// ---------------------------------------------------------------------------
// Per-column filter wiring for the Approval Queue table.
//
// The queue scope is already narrow (pending|notified rows only); column
// filters add inline narrowing on the queue's own columns. As with the
// Purchase History wiring, Status is excluded because the queue's row set
// is status-defined and the parent loadHistory loop is the authoritative
// status source.
//
// Numeric extractors round to 0 decimal places (CURRENCY_DEFAULT_DIGITS)
// so a "$X" filter targets the same value the cell renders.
// ---------------------------------------------------------------------------

const APPROVAL_QUEUE_NUMERIC_COLUMNS: ReadonlySet<ApprovalQueueColumnId> = new Set([
  'count', 'monthly_cost', 'upfront_cost', 'savings',
]);

const APPROVAL_QUEUE_LABELS: Record<ApprovalQueueColumnId, string> = {
  provider: 'Provider',
  account: 'Account',
  service: 'Service',
  term: 'Term',
  payment: 'Payment',
  created_by: 'Created By',
  count: 'Count',
  monthly_cost: 'Monthly Cost',
  upfront_cost: 'Upfront Cost',
  savings: 'Monthly Savings',
};

function approvalQueueCategoricalCellValue(
  p: HistoryPurchase,
  col: ApprovalQueueColumnId,
): string {
  switch (col) {
    case 'provider':    return p.provider ?? '';
    case 'account':     return p.account_id ?? '';
    case 'service':     return p.service ?? '';
    case 'term':        return p.term == null ? '' : String(p.term);
    case 'payment':     return p.payment ?? '';
    case 'created_by':  return p.created_by_user_email ?? p.created_by_user_id ?? '';
    case 'count':
    case 'monthly_cost':
    case 'upfront_cost':
    case 'savings':     return '';
  }
}

function approvalQueueNumericCellValue(
  p: HistoryPurchase,
  col: ApprovalQueueColumnId,
): number {
  switch (col) {
    case 'count':        return p.count ?? 0;
    // Return NaN for null monthly_cost so numeric predicates (e.g. "= 0")
    // don't match rows where the provider didn't report a monthly cost.
    case 'monthly_cost': return p.monthly_cost == null ? Number.NaN : p.monthly_cost;
    case 'upfront_cost': return p.upfront_cost ?? Number.NaN;
    case 'savings':      return p.estimated_savings ?? Number.NaN;
    case 'provider':
    case 'account':
    case 'service':
    case 'term':
    case 'payment':
    case 'created_by':   return Number.NaN;
  }
}

export function applyApprovalQueueColumnFilters(
  purchases: readonly HistoryPurchase[],
  filters: state.ApprovalQueueColumnFilters,
): HistoryPurchase[] {
  return applyColumnFilters<HistoryPurchase, ApprovalQueueColumnId>(
    purchases,
    filters,
    {
      categorical: approvalQueueCategoricalCellValue,
      numeric: (p, col) => roundForDisplay(approvalQueueNumericCellValue(p, col)),
    },
  );
}

function approvalQueueDistinctValues(
  purchases: readonly HistoryPurchase[],
  column: ApprovalQueueColumnId,
): string[] {
  const seen = new Set<string>();
  for (const p of purchases) {
    seen.add(approvalQueueCategoricalCellValue(p, column));
  }
  return Array.from(seen).sort((a, b) => {
    if (a === '' && b !== '') return -1;
    if (a !== '' && b === '') return 1;
    return a.localeCompare(b);
  });
}

function approvalQueueDisplayLabel(
  column: ApprovalQueueColumnId,
  value: string,
): string {
  if (value === '') return '(empty)';
  if (column === 'term') {
    // Match the cell: a 0 term renders as a dash, never "0 Years" (#181).
    const n = Number(value);
    return isRealTerm(n) ? formatTermShort(n) : Number.isFinite(n) ? NO_VALUE : value;
  }
  if (column === 'service') return serviceLabel(value);
  if (column === 'account') {
    // Account cells render via getAccountName() — mirror the same display.
    return getAccountName(value);
  }
  if (column === 'payment') return formatPaymentOption(value);
  return value;
}

function wireApprovalQueueFilterButtons(
  container: HTMLElement,
  // Source for the popover's distinct-values list — the pre-column-filter
  // pending slice. Same reasoning as Purchase History: the popover must
  // list every value that exists in the broader (un-narrowed) set so
  // the user can re-check a value after unchecking it.
  sourceRows: readonly HistoryPurchase[],
): void {
  container.querySelectorAll<HTMLButtonElement>('.history-column-filter-btn').forEach((btn) => {
    const column = btn.dataset['column'] as ApprovalQueueColumnId | undefined;
    if (!column) return;
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const isNumeric = APPROVAL_QUEUE_NUMERIC_COLUMNS.has(column);
      const filters = state.getApprovalQueueColumnFilters();
      openHistoryColumnPopover<ApprovalQueueColumnId>({
        scope: APPROVAL_QUEUE_FILTER_SCOPE,
        column,
        anchor: btn,
        currentFilter: filters[column],
        headerLabel: APPROVAL_QUEUE_LABELS[column],
        kind: isNumeric ? 'numeric' : 'categorical',
        distinctValues: isNumeric ? undefined : approvalQueueDistinctValues(sourceRows, column),
        displayLabel: (v) => approvalQueueDisplayLabel(column, v),
        onCommit: (filter) => {
          state.setApprovalQueueColumnFilter(column, filter);
          renderApprovalQueue(historyState.lastPendingForQueue);
        },
      });
    });
  });
}

export function renderApprovalQueue(purchases: HistoryPurchase[]): void {
  const container = document.getElementById('purchases-approval-queue');
  if (!container) return;

  const pending = (purchases || []).filter(isPendingRow);
  historyState.lastPendingForQueue = pending;

  if (pending.length === 0) {
    container.innerHTML = '<p class="empty">No pending approvals.</p>';
    return;
  }

  const colFilters = state.getApprovalQueueColumnFilters();
  const visible = applyApprovalQueueColumnFilters(pending, colFilters);

  const amortize = state.getAmortizeUpfront();
  const rows = visible.map(p => {
    const actions = renderPendingActionButtons(p);
    const actionsCell = actions || `<span class="muted">${NO_VALUE}</span>`;
    // Show email when resolved; fall back to UUID so the cancel-own gate still
    // has something human-readable to show. Fall back to "-" for scheduler rows.
    const createdBy = p.created_by_user_email
      ? escapeHtml(p.created_by_user_email)
      : p.created_by_user_id
        ? escapeHtml(p.created_by_user_id)
        : `<span class="muted">${NO_VALUE}</span>`;
    const accountCell = p.account_id
      ? `<span class="cell-truncate" title="${escapeHtmlAttr(getAccountName(p.account_id))}">${escapeHtml(getAccountName(p.account_id))}</span>`
      : `<span class="muted">${NO_VALUE}</span>`;
    const empty = hasNoCommitments(p);
    const dashOr = (html: string): string => (empty ? MUTED_DASH : html);
    const paymentCell = p.payment ? escapeHtml(formatPaymentOption(p.payment)) : `<span class="muted">${NO_VALUE}</span>`;
    const rawMonthly = p.monthly_cost != null ? p.monthly_cost : null;
    const displayMonthly = (rawMonthly != null && amortize)
      ? (p.upfront_cost == null ? null : amortizedMonthly(rawMonthly, p.upfront_cost, p.term))
      : rawMonthly;
    const monthlyCostCell = displayMonthly != null
      ? formatCurrency(displayMonthly)
      : `<span class="muted">${NO_VALUE}</span>`;
    const execIdAttr = p.purchase_id ? ` data-execution-id="${escapeHtmlAttr(p.purchase_id)}"` : '';
    return `
      <tr${execIdAttr}>
        <td data-label="Date">${historyDate(p.timestamp)}</td>
        <td data-label="Account">${accountCell}</td>
        <td data-label="Provider">${providerCell(p)}</td>
        <td data-label="Service">${empty ? NO_RECS_HTML : escapeHtml(p.service ? serviceLabel(p.service) : p.service)}</td>
        <td class="num" data-label="Count">${dashOr(escapeHtml(String(p.count ?? '')))}</td>
        <td data-label="Term">${termCell(p)}</td>
        <td data-label="Payment">${dashOr(paymentCell)}</td>
        <td class="num" data-label="${amortize ? 'Monthly Cost (amortized)' : 'Monthly Cost'}">${dashOr(monthlyCostCell)}</td>
        ${empty ? `<td class="num" data-label="Upfront Cost">${MUTED_DASH}</td>` : upfrontCell(p, amortize)}
        <td class="num savings" data-label="Monthly Savings">${dashOr(formatCurrency(p.estimated_savings))}</td>
        <td data-label="Created By">${createdBy}</td>
        <td data-label="Actions">${actionsCell}</td>
      </tr>
    `;
  }).join('');

  const monthlyColHeader = amortize ? 'Monthly Cost (amortized)' : 'Monthly Cost';
  // monthlyColHeader is a hardcoded constant string (no user data), so
  // interpolating it directly into the template is safe.
  const fbtn = (col: ApprovalQueueColumnId): string => renderHistoryFilterButton(
    col, APPROVAL_QUEUE_LABELS[col], colFilters[col] != null, APPROVAL_QUEUE_FILTER_SCOPE,
  );
  container.innerHTML = `
    <table class="approval-queue-table table-compact table-sticky-end table-cards-mobile">
      <thead>
        <tr>
          <th>Date</th>
          <th><span>Account</span>${fbtn('account')}</th>
          <th><span>Provider</span>${fbtn('provider')}</th>
          <th><span>Service</span>${fbtn('service')}</th>
          <th class="num"><span>Count</span>${fbtn('count')}</th>
          <th><span>Term</span>${fbtn('term')}</th>
          <th><span>Payment</span>${fbtn('payment')}</th>
          <th class="num"><span>${monthlyColHeader}</span>${fbtn('monthly_cost')}</th>
          <th class="num"><span>Upfront Cost</span>${fbtn('upfront_cost')}</th>
          <th class="num"><span>Monthly Savings</span>${fbtn('savings')}</th>
          <th><span>Created By</span>${fbtn('created_by')}</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        ${pending.length > 0 && visible.length === 0 ? `<tr><td colspan="12" class="empty">No rows match the active column filters. Clear a filter to see them.</td></tr>` : ''}
        ${rows}
      </tbody>
    </table>
  `;

  // Mount the amortize checkbox into the approval queue section (idempotent).
  mountAmortizeCheckbox('purchases-approval-queue-section', 'approval-queue-amortize-checkbox');

  wireRowActionHandlers(container);
  wireApprovalQueueFilterButtons(container, pending);
}
