import * as state from '../state';
import { formatCurrency, escapeHtml, escapeHtmlAttr, amortizedMonthly, NO_VALUE } from '../utils';
import type { HistoryPurchase } from '../types';
import { formatPaymentOption, formatTermShort } from '../lib/table-format';
import { serviceLabel } from '../lib/service-labels';
import { getAccountName } from '../recommendations';
import { applyColumnFilters } from '../lib/column-filters';
import { hasNoCommitments, isRealTerm } from '../lib/purchase-row-guards';
import { openHistoryColumnPopover, renderHistoryFilterButton } from '../lib/history-filter-popover';
import type { ApprovalQueueColumnId } from '../state';
import { historyState } from './historyState';
import { MUTED_DASH, NO_RECS_HTML, termCell, historyDate, providerCell, upfrontCell, roundForDisplay } from './cells';
import { renderPendingActionButtons } from './rowRender';
import { wireRowActionHandlers } from './rowHandlers';
import { mountAmortizeCheckbox } from './amortize';

// isPendingRow returns true when a history row represents a purchase
// awaiting approval. The Approval queue card filters on this predicate.
// Mirrors the badge / button-eligibility logic elsewhere in this file:
// "pending" is the freshly-created state, "notified" is the post-SES
// state; both render the same Approve / Cancel affordances.
function isPendingRow(p: HistoryPurchase): boolean {
  const s = (p.status || '').toLowerCase();
  return s === 'pending' || s === 'notified';
}

const APPROVAL_QUEUE_FILTER_SCOPE = 'queue';

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
