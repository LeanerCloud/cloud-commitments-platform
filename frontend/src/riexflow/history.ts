import * as api from '../api';
import { formatDateTime, escapeHtml } from '../utils';
import { confirmDialog } from '../confirmDialog';
import { buildApproveConfirmBody } from '../riexchange-confirm';
import type { RIExchangeHistoryRecord, RIExchangeDirectExecution } from '../api';
import { showSkeletonRows } from '../lib/skeleton';
import { statusBadgeHtml } from '../lib/status-badge';
import { buildDirectExchangeSection } from '../riexchange-direct-history';
import { canAccess } from '../permissions';
import { showToast } from '../toast';
import { approveResultToast } from '../riexchange-approve-outcome';
import { getCurrentUser } from '../state';
import { formatDecimalStringCurrency } from '../lib/table-format';
import { riexState } from './riexState';
import { exchangePaymentPattern } from './shared';

// ──────────────────────────────────────────────
// Exchange History
// ──────────────────────────────────────────────

// canApproveRIExchangeRow returns true when the current session may approve
// the given pending RI exchange via the inline Approve button (issue #300).
// UX gate only — the backend authorizeSessionApproveRIExchange remains the
// security boundary; a false-positive here surfaces as a 403 toast on click.
//
// Heuristic mirrors canApprovePendingRow in history.ts:
//   * status must be "pending";
//   * admin -> always yes;
//   * non-admin matching created_by_user_id -> yes (approve-own);
//   * legacy rows without created_by_user_id -> no (email-link path only).
function canApproveRIExchangeRow(rec: RIExchangeHistoryRecord): boolean {
  if ((rec.status || '').toLowerCase() !== 'pending') return false;
  const user = getCurrentUser();
  if (!user) return false;
  if (canAccess('admin', '*') || canAccess('approve-any', 'purchases')) return true;
  if (!rec.created_by_user_id) return false;
  return canAccess('approve-own', 'purchases') && rec.created_by_user_id === user.id;
}

export async function loadExchangeHistory(generation: number = riexState.scopeGeneration): Promise<void> {
  const container = document.getElementById('ri-exchange-history-list');
  if (!container) return;

  // Issue #186: skeleton rows shaped like the 7-column history table.
  showSkeletonRows(container, 3, 7);

  try {
    const { records, direct_executions } = await api.getRIExchangeHistoryResponse();
    if (generation !== riexState.scopeGeneration) return;
    renderExchangeHistory(container, records, direct_executions);
  } catch (error) {
    if (generation !== riexState.scopeGeneration) return;
    const err = error as Error;
    container.textContent = '';
    const errorP = document.createElement('p');
    errorP.className = 'error';
    errorP.textContent = 'Failed to load exchange history: ' + err.message;
    container.appendChild(errorP);
  }
}

function renderDirectExchanges(container: HTMLElement, direct: RIExchangeDirectExecution[]): void {
  if (direct.length > 0) {
    container.appendChild(buildDirectExchangeSection(direct));
  }
}

function renderExchangeHistory(
  container: HTMLElement,
  records: RIExchangeHistoryRecord[],
  direct: RIExchangeDirectExecution[] = [],
): void {
  const hasDirect = direct.length > 0;
  if ((!records || records.length === 0) && !hasDirect) {
    container.textContent = '';
    const emptyP = document.createElement('p');
    emptyP.className = 'empty';
    emptyP.textContent = "You haven't exchanged any RIs yet. When you exchange, it appears here.";
    container.appendChild(emptyP);
    return;
  }

  // Build table rows — all dynamic values go through escapeHtml
  const rowsHTML = (records ?? []).map(rec => {
    const exchangeIdCell = rec.exchange_id
      ? '<span class="monospace">' + escapeHtml(rec.exchange_id) + '</span>'
      : '&mdash;';
    // A failed exchange carries the AWS reason in rec.error; show it under the
    // badge so the operator can see why without reading backend logs.
    const errorLine = rec.error
      ? '<div class="status-detail">' + escapeHtml(rec.error) + '</div>'
      : '';
    const approveBtn = canApproveRIExchangeRow(rec)
      ? '<button type="button" class="btn-link riexchange-approve-btn" data-approve-id="' + escapeHtml(rec.id) + '">Approve</button>'
      : '';
    return '<tr>'
      + '<td>' + escapeHtml(formatDateTime(rec.created_at)) + '</td>'
      + '<td>' + escapeHtml(String(rec.source_count)) + 'x ' + escapeHtml(rec.source_instance_type) + '</td>'
      + '<td>' + escapeHtml(String(rec.target_count)) + 'x ' + escapeHtml(rec.target_instance_type) + '</td>'
      // payment_due is USD (the execute cap is max_payment_due_usd); an
      // empty amount renders as a dash, not a lone "$" (issue #284).
      + '<td class="num">' + (rec.payment_due ? escapeHtml(formatDecimalStringCurrency(rec.payment_due)) : '&mdash;') + '</td>'
      + '<td>' + statusBadgeHtml(rec.status) + errorLine + '</td>'
      + '<td>' + exchangeIdCell + '</td>'
      + '<td>' + approveBtn + '</td>'
      + '</tr>';
  }).join('');

  const tableHTML = '<table class="table-compact table-sticky-end">'
    + '<thead><tr>'
    + '<th>Date</th><th>Source Type</th><th>Target Type</th><th class="num">Payment</th><th>Status</th><th>Exchange ID</th><th>Actions</th>'
    + '</tr></thead>'
    + '<tbody>' + rowsHTML + '</tbody>'
    + '</table>';

  container.textContent = '';
  if (records && records.length > 0) {
    const wrapper = document.createElement('div');
    wrapper.innerHTML = tableHTML;
    while (wrapper.firstChild) {
      container.appendChild(wrapper.firstChild);
    }
  }
  renderDirectExchanges(container, direct);

  // Wire Approve button click handlers
  container.querySelectorAll<HTMLButtonElement>('.riexchange-approve-btn[data-approve-id]').forEach(btn => {
    const record = records.find(rec => rec.id === btn.dataset.approveId);
    if (record) btn.addEventListener('click', () => handleRIExchangeApproveClick(btn, record));
  });
}

async function handleRIExchangeApproveClick(btn: HTMLButtonElement, record: RIExchangeHistoryRecord): Promise<void> {
  if (btn.disabled) return;
  if (!exchangePaymentPattern.test(record.payment_due)) {
    showToast({ kind: 'error', message: 'Cannot approve exchange without a previously quoted payment.' });
    return;
  }

  btn.disabled = true;
  const confirmed = await confirmDialog({
    title: 'Approve RI Exchange',
    body: buildApproveConfirmBody(record),
    confirmLabel: 'Approve',
    destructive: true,
  });
  if (!confirmed) {
    btn.disabled = false;
    btn.focus();
    return;
  }

  try {
    showToast(approveResultToast(await api.approveRIExchange(record.id)));
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    showToast({ kind: 'error', message: 'Failed to approve exchange: ' + msg });
    btn.disabled = false;
    return;
  }
  try {
    await loadExchangeHistory();
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    showToast({ kind: 'error', message: 'Approved, but failed to refresh history: ' + msg });
  }
}
