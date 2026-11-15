import * as api from '../api';
import { formatCurrency, formatDate, amortizedMonthly, providerBadgeClass } from '../utils';
import { formatPaymentOption, formatTermShort } from '../lib/table-format';
import * as state from '../state';
import { appendCell, clearChildren, renderEmptyParagraph } from './inventory-dom';

/**
 * Build a context-aware empty-state message for the active-commitments
 * table. When chip filters are active the message names the scope so
 * the user knows the result is filtered rather than globally empty.
 */
function buildActiveCommitmentsEmptyMessage(provider?: string, accountID?: string): string {
  if (provider && accountID) {
    return `No active commitments for provider "${provider}" and account ${accountID}.`;
  }
  if (provider) {
    return `No active commitments for provider "${provider}".`;
  }
  if (accountID) {
    return `No active commitments for account ${accountID}.`;
  }
  return 'No active commitments found across your registered accounts.';
}

/**
 * Render the per-commitment table into `container`. Empty list yields
 * an inline `.empty` paragraph instead of an empty table so the user
 * gets a real message ("no active commitments"), not a blank header.
 *
 * When a chip filter is active the empty-state message names the scope
 * so the user understands the result is filtered, not globally empty.
 *
 * All text uses textContent / DOM construction — no innerHTML — to
 * keep the section safe by default against any unescaped backend
 * field (issue #340 XSS posture).
 */
export function renderActiveCommitmentsTable(
  container: HTMLElement,
  commitments: api.InventoryCommitment[],
  provider?: string,
  accountID?: string,
): void {
  if (!commitments || commitments.length === 0) {
    const msg = buildActiveCommitmentsEmptyMessage(provider, accountID);
    renderEmptyParagraph(container, msg);
    return;
  }

  clearChildren(container);
  const table = document.createElement('table');
  table.className = 'table-compact';

  const thead = document.createElement('thead');
  const headerRow = document.createElement('tr');
  const amortize = state.getAmortizeUpfront();
  const monthlyLabel = amortize ? 'Monthly Cost (amortized)' : 'Monthly Cost';
  const headers = ['Provider', 'Account', 'Service', 'Resource Type', 'Region', 'Count', 'Term', 'Payment', monthlyLabel, 'Monthly Savings', 'Expires'];
  const numericHeaders = new Set(['Count', monthlyLabel, 'Monthly Savings']);
  for (const label of headers) {
    const th = document.createElement('th');
    th.textContent = label;
    if (numericHeaders.has(label)) th.className = 'num';
    headerRow.appendChild(th);
  }
  thead.appendChild(headerRow);
  table.appendChild(thead);

  const tbody = document.createElement('tbody');
  for (const c of commitments) {
    tbody.appendChild(buildCommitmentRow(c));
  }
  table.appendChild(tbody);

  container.appendChild(table);

  // Mount the amortize toggle into the section-header-actions area (idempotent).
  mountInventoryAmortizeCheckbox();
}

function buildCommitmentRow(c: api.InventoryCommitment): HTMLTableRowElement {
  const tr = document.createElement('tr');

  tr.appendChild(buildProviderCell(c.provider));
  tr.appendChild(buildAccountCell(c));
  appendNarrowWrapCell(tr, c.service);
  appendNarrowWrapCell(tr, c.resource_type ?? '');
  appendCell(tr, c.region);
  appendCell(tr, String(c.count), true);
  appendCell(tr, formatTermShort(c.term_years));
  appendCell(tr, formatPaymentOption(c.payment_option));

  // When amortize is on, fold the upfront cost over the term years.
  const amortize = state.getAmortizeUpfront();
  let displayMonthly: number | null = c.monthly_cost;
  if (displayMonthly != null && amortize) {
    displayMonthly = c.upfront_cost == null ? null : amortizedMonthly(displayMonthly, c.upfront_cost, c.term_years);
  }
  appendCell(tr, displayMonthly != null ? formatCurrency(displayMonthly) : '—', true);

  appendCell(tr, formatCurrency(c.estimated_savings), true);
  appendCell(tr, formatDate(c.end_date));

  return tr;
}

// The same provider badge the other tables use (lowercase "aws" before).
function buildProviderCell(provider: string): HTMLTableCellElement {
  const td = document.createElement('td');
  const badge = document.createElement('span');
  const cls = providerBadgeClass(provider);
  badge.className = cls ? `provider-badge ${cls}` : 'provider-badge';
  badge.textContent = (provider || '').toUpperCase();
  td.appendChild(badge);
  return td;
}

// Long free-form values (savings plan descriptions, service slugs) wrap at a
// narrow width: the account cell already makes every row two lines high, so
// a second line here costs no height and keeps the whole value visible.
function appendNarrowWrapCell(tr: HTMLTableRowElement, text: string): void {
  const td = document.createElement('td');
  td.className = 'cell-wrap-narrow';
  td.textContent = text;
  tr.appendChild(td);
}

/**
 * Mount the "Amortize upfront over term" checkbox into the active-commitments
 * section-header-actions area (idempotent). Wires to setAmortizeUpfront so
 * the same localStorage key is shared with all other views (issue #1112).
 */
function mountInventoryAmortizeCheckbox(): void {
  const actions = document.querySelector<HTMLElement>('#inventory-active-commitments .section-header-actions');
  if (!actions) return;
  const checkboxId = 'inventory-amortize-checkbox';
  if (document.getElementById(checkboxId)) {
    // Already mounted -- sync checked state in case another view changed it.
    const cb = document.getElementById(checkboxId) as HTMLInputElement;
    cb.checked = state.getAmortizeUpfront();
    return;
  }

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
  actions.appendChild(wrapper);
}

// The id sits under the name in small muted type; both truncate with an
// ellipsis (Azure subscription GUIDs are long) and the cell's title carries
// the full value.
function buildAccountCell(c: api.InventoryCommitment): HTMLTableCellElement {
  const td = document.createElement('td');
  if (c.account_name && c.account_name.includes(c.account_id)) {
    td.textContent = c.account_name;
    td.title = c.account_name;
  } else if (c.account_name) {
    const name = document.createElement('span');
    name.className = 'cell-truncate cell-truncate--sm';
    name.textContent = c.account_name;
    td.appendChild(name);
    const id = document.createElement('span');
    id.className = 'monospace cell-id';
    id.textContent = c.account_id;
    td.appendChild(id);
    td.title = `${c.account_name} (${c.account_id})`;
  } else {
    const id = document.createElement('span');
    id.className = 'monospace';
    id.textContent = c.account_id;
    td.appendChild(id);
  }
  return td;
}
