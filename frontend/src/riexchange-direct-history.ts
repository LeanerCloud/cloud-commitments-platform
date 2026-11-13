/**
 * Read-only History rows for direct (interactive) RI exchanges (#1883). They
 * are recorded outside ri_exchange_history, so they render in their own table
 * under the exchange history, labelled by provider. There are no actions here:
 * resolving a stuck AWS row stays on the resolve API.
 */
import { escapeHtml, formatDateTime } from './utils';
import { statusBadgeHtml } from './lib/status-badge';
import type { RIExchangeDirectExecution } from './api/types';

function formatAmount(amount: string, currency: string): string {
  const n = Number(amount);
  if (amount.trim() === '' || !Number.isFinite(n)) return amount;
  try {
    return n.toLocaleString('en-US', { style: 'currency', currency: currency || 'USD' });
  } catch {
    return `${amount} ${currency}`.trim();
  }
}

function amountCell(row: RIExchangeDirectExecution): string {
  // The provider-reported net amount wins; before the outcome is known only
  // the caller's cap exists, so label it as a cap.
  if (row.net_payable) return escapeHtml(formatAmount(row.net_payable, row.net_payable_currency));
  return escapeHtml('cap ' + formatAmount(row.max_payment_due, row.max_payment_currency));
}

function statusCell(row: RIExchangeDirectExecution): string {
  const resolved = row.resolved_status
    ? '<div class="status-detail">Resolved by operator: ' + escapeHtml(row.resolved_status) + '</div>'
    : '';
  const err = row.error_summary ? '<div class="status-detail">' + escapeHtml(row.error_summary) + '</div>' : '';
  return statusBadgeHtml(row.status) + resolved + err;
}

function sourceCell(row: RIExchangeDirectExecution): string {
  return escapeHtml(row.source_ids.length + ' source' + (row.source_ids.length === 1 ? '' : 's'))
    + '<div class="status-detail monospace">' + escapeHtml(row.source_ids.join(', ')) + '</div>';
}

function targetCount(row: RIExchangeDirectExecution): string {
  const n = Array.isArray(row.targets) ? row.targets.length : 0;
  return escapeHtml(n + ' target' + (n === 1 ? '' : 's'));
}

/** Build the "Direct exchanges" section; the caller appends it. */
export function buildDirectExchangeSection(rows: RIExchangeDirectExecution[]): HTMLElement {
  const section = document.createElement('div');
  section.className = 'ri-exchange-direct-history';
  const heading = document.createElement('h4');
  heading.textContent = 'Direct exchanges';
  section.appendChild(heading);
  const note = document.createElement('p');
  note.className = 'hint';
  note.textContent = 'Interactive exchanges run directly, outside the approval flow. Read-only.';
  section.appendChild(note);

  const body = rows.map(r => '<tr>'
    + '<td>' + escapeHtml(formatDateTime(r.created_at)) + '</td>'
    + '<td>' + escapeHtml(r.provider.toUpperCase()) + '</td>'
    + '<td>' + sourceCell(r) + '</td>'
    + '<td>' + targetCount(r) + '</td>'
    + '<td class="num">' + amountCell(r) + '</td>'
    + '<td>' + statusCell(r) + '</td>'
    + '<td>' + escapeHtml(r.executed_by) + '</td>'
    + '</tr>').join('');
  const table = document.createElement('div');
  table.innerHTML = '<table class="table-compact">'
    + '<thead><tr><th>Date</th><th>Provider</th><th>Sources</th><th>Targets</th><th class="num">Net payment</th><th>Status</th><th>Executed by</th></tr></thead>'
    + '<tbody>' + body + '</tbody></table>';
  while (table.firstChild) section.appendChild(table.firstChild);
  return section;
}
