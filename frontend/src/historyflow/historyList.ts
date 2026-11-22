import * as state from '../state';
import { formatCurrency, escapeHtml, escapeHtmlAttr, amortizedMonthly, NO_VALUE } from '../utils';
import type { HistoryPurchase } from '../types';
import { serviceLabel } from '../lib/service-labels';
import { wireApprovalQueueLinks } from '../history-queue-link';
import { canAccess } from '../permissions';
import { hasNoCommitments } from '../lib/purchase-row-guards';
import { openHistoryColumnPopover, renderHistoryFilterButton } from '../lib/history-filter-popover';
import type { PurchaseHistoryColumnId } from '../state';
import { historyState } from './historyState';
import { noSavingsCell, statusFullTextHtml, normalizeStatus, isInFlightStatus, statusBadgeHTML, buildStatusChipRowHTML, MUTED_DASH, NO_RECS_HTML, termCell, historyDate, providerCell, upfrontCell } from './cells';
import type { StatusFilter } from './cells';
import { opsHintHtml, renderActionCell } from './rowRender';
import { wireRowActionHandlers } from './rowHandlers';
import { mountAmortizeCheckbox } from './amortize';
import { applyExecutionDeepLink } from './deepLink';
import { PURCHASE_HISTORY_NUMERIC_COLUMNS, applyPurchaseHistoryColumnFilters, PURCHASE_HISTORY_LABELS, purchaseHistoryDistinctValues, purchaseHistoryDisplayLabel, PURCHASE_HISTORY_FILTER_SCOPE } from './historyColumns';

let activeStatusFilter: StatusFilter = 'all';

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

export function renderHistoryList(purchases: HistoryPurchase[]): void {
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
