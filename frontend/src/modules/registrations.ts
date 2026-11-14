/**
 * Account Registrations module — admin approval panel for self-registered accounts.
 */

import * as api from '../api';
import type { AccountRegistration } from '../api/registrations';
import type { CloudAccount, CloudAccountRequest } from '../api/accounts';
import { openAccountModal, loadAccountsForProvider } from '../settings';
import { formatDateTime } from '../utils';
import { badgeClass, statusLabel, statusTone } from '../lib/status-badge';
import { showToast } from '../toast';
import { confirmDialog } from '../confirmDialog';
import { friendlyError } from '../api/errors';
import { rowIconButton } from '../lib/row-icon-button';

type AccountProvider = 'aws' | 'azure' | 'gcp';

/** Build a synthetic CloudAccount from registration fields for the modal. */
function registrationToAccount(reg: AccountRegistration): CloudAccount {
  return {
    id: '',
    name: reg.account_name,
    description: reg.description,
    provider: reg.provider,
    external_id: reg.external_id,
    contact_email: reg.contact_email,
    // The modal's checkbox starts checked; the backend honours an explicit false.
    enabled: true,
    aws_auth_mode: reg.aws_auth_mode as CloudAccount['aws_auth_mode'],
    aws_role_arn: reg.aws_role_arn,
    aws_external_id: reg.aws_external_id,
    azure_subscription_id: reg.azure_subscription_id,
    azure_tenant_id: reg.azure_tenant_id,
    azure_client_id: reg.azure_client_id,
    azure_auth_mode: reg.azure_auth_mode as CloudAccount['azure_auth_mode'],
    gcp_project_id: reg.gcp_project_id,
    gcp_client_email: reg.gcp_client_email,
    gcp_auth_mode: reg.gcp_auth_mode as CloudAccount['gcp_auth_mode'],
    gcp_wif_audience: reg.gcp_wif_audience,
    credentials_configured: false,
    created_at: '',
    updated_at: '',
  };
}

function providerLabel(p: string): string {
  return p === 'aws' ? 'AWS' : p === 'azure' ? 'Azure' : p === 'gcp' ? 'GCP' : p;
}

function createStatusBadge(status: string): HTMLSpanElement {
  const span = document.createElement('span');
  span.className = badgeClass(statusTone(status));
  span.textContent = statusLabel(status);
  return span;
}

function createCell(content: string | HTMLElement): HTMLTableCellElement {
  const td = document.createElement('td');
  if (typeof content === 'string') {
    td.textContent = content;
  } else {
    td.appendChild(content);
  }
  return td;
}

const EMAIL_MAX_CHARS = 28;

function createEmailCell(email: string): HTMLTableCellElement {
  const td = document.createElement('td');
  td.textContent = email.length > EMAIL_MAX_CHARS ? `${email.slice(0, EMAIL_MAX_CHARS - 1)}\u2026` : email;
  if (email.length > EMAIL_MAX_CHARS) td.title = email;
  return td;
}

function copyViaTextarea(text: string): boolean {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  ta.remove();
  return ok;
}

async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Fall through to the legacy path (insecure context, denied permission).
    }
  }
  return copyViaTextarea(text);
}

function createCodeCell(text: string): HTMLTableCellElement {
  const td = document.createElement('td');
  const code = document.createElement('code');
  code.textContent = text;
  td.appendChild(code);
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'copy-btn';
  btn.title = 'Copy external ID';
  btn.setAttribute('aria-label', `Copy external ID ${text}`);
  btn.textContent = 'Copy';
  btn.addEventListener('click', () => {
    void copyText(text).then(ok => {
      if (ok) showToast({ message: 'External ID copied', kind: 'success' });
      else showToast({ message: 'Copy failed: select the ID and copy it manually', kind: 'error' });
    });
  });
  td.appendChild(btn);
  return td;
}

// Auto-generated registration names often embed a UUID (8-4-4-4-12 hex).
// Dim the UUID portion so the readable prefix (e.g. "Azure") stands out.
const UUID_RE = /([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/i;

function createNameCell(name: string): HTMLTableCellElement {
  const td = document.createElement('td');
  const match = name.match(UUID_RE);
  if (!match || match.index === undefined) {
    td.textContent = name;
    return td;
  }
  const uuid = match[1] ?? '';
  const before = name.slice(0, match.index);
  const after = name.slice(match.index + uuid.length);
  if (before) td.appendChild(document.createTextNode(before));
  const uuidSpan = document.createElement('span');
  uuidSpan.className = 'text-muted';
  uuidSpan.textContent = uuid;
  td.appendChild(uuidSpan);
  if (after) td.appendChild(document.createTextNode(after));
  return td;
}

// Ids of pending rows ticked for the bulk action. Pruned on every render, so
// a row that is no longer pending drops out; failed rows stay pending and stay ticked.
const selectedIds = new Set<string>();
let bulkRunning = false;

// Update the bulk bar and checkboxes in place: rebuilding the table on each
// toggle would drop keyboard focus and stop a user from ticking several rows.
function syncSelection(container: HTMLElement): void {
  const boxes = Array.from(container.querySelectorAll<HTMLInputElement>('input[data-reg-id]'));
  for (const box of boxes) {
    box.checked = selectedIds.has(box.dataset['regId'] ?? '');
    box.disabled = bulkRunning;
  }
  const all = container.querySelector<HTMLInputElement>('input[data-select-all]');
  if (all) {
    all.checked = boxes.length > 0 && boxes.every(b => b.checked);
    all.disabled = bulkRunning;
  }
  const btn = container.querySelector<HTMLButtonElement>('#registrations-bulk-reject');
  if (btn) {
    btn.textContent = `Reject ${selectedIds.size}`;
    btn.disabled = bulkRunning || selectedIds.size === 0;
  }
}

function renderBulkBar(container: HTMLElement, pending: AccountRegistration[]): void {
  const bar = document.createElement('div');
  bar.className = 'registrations-bulk-bar';
  const rejectBtn = document.createElement('button');
  rejectBtn.type = 'button';
  rejectBtn.className = 'btn btn-small btn-warning';
  rejectBtn.id = 'registrations-bulk-reject';
  rejectBtn.addEventListener('click', () => {
    void handleBulkReject(pending.filter(r => selectedIds.has(r.id)));
  });
  bar.appendChild(rejectBtn);
  container.appendChild(bar);
}

function renderRegistrationsTable(container: HTMLElement, regs: AccountRegistration[]): void {
  container.textContent = '';

  const pending = regs.filter(r => r.status === 'pending');
  for (const id of Array.from(selectedIds)) {
    if (!pending.some(r => r.id === id)) selectedIds.delete(id);
  }

  if (regs.length === 0) {
    const p = document.createElement('p');
    p.className = 'empty-state';
    p.textContent = 'No registrations found.';
    container.appendChild(p);
    return;
  }

  if (pending.length > 0) renderBulkBar(container, pending);

  const table = document.createElement('table');
  table.className = 'data-table';

  const thead = document.createElement('thead');
  const headerRow = document.createElement('tr');
  const selectTh = document.createElement('th');
  if (pending.length > 0) {
    const all = document.createElement('input');
    all.type = 'checkbox';
    all.dataset['selectAll'] = '1';
    all.setAttribute('aria-label', 'Select all pending registrations');
    all.addEventListener('change', () => {
      for (const r of pending) {
        if (all.checked) selectedIds.add(r.id);
        else selectedIds.delete(r.id);
      }
      syncSelection(container);
    });
    selectTh.appendChild(all);
  }
  headerRow.appendChild(selectTh);
  for (const h of ['Provider', 'Name', 'External ID', 'Contact', 'Submitted', 'Status', 'Actions']) {
    const th = document.createElement('th');
    th.textContent = h;
    headerRow.appendChild(th);
  }
  thead.appendChild(headerRow);
  table.appendChild(thead);

  const tbody = document.createElement('tbody');
  for (const reg of regs) {
    const row = document.createElement('tr');
    const selectTd = document.createElement('td');
    if (reg.status === 'pending') {
      const box = document.createElement('input');
      box.type = 'checkbox';
      box.dataset['regId'] = reg.id;
      box.setAttribute('aria-label', `Select registration ${reg.account_name || reg.external_id}`);
      box.addEventListener('change', () => {
        if (box.checked) selectedIds.add(reg.id);
        else selectedIds.delete(reg.id);
        syncSelection(container);
      });
      selectTd.appendChild(box);
    }
    row.appendChild(selectTd);
    row.appendChild(createCell(providerLabel(reg.provider)));
    row.appendChild(createNameCell(reg.account_name));
    row.appendChild(createCodeCell(reg.external_id));
    row.appendChild(createEmailCell(reg.contact_email));
    row.appendChild(createCell(formatDateTime(reg.created_at)));

    const statusTd = document.createElement('td');
    statusTd.className = 'registration-status';
    statusTd.appendChild(createStatusBadge(reg.status));
    if (reg.status === 'approved') {
      // An approved row always has a live cloud_account_id (deleteAccount
      // resets the link to pending). It is a status, so it lives here.
      const created = document.createElement('span');
      created.className = 'text-muted ml-xs';
      created.textContent = 'Account created';
      statusTd.appendChild(created);
    }
    if (reg.has_credentials) {
      const credBadge = document.createElement('span');
      credBadge.className = `${badgeClass('info')} ml-xs`;
      credBadge.textContent = 'creds';
      credBadge.title = 'Credentials included — will be auto-stored on approval';
      statusTd.appendChild(credBadge);
    }
    row.appendChild(statusTd);

    const actionsTd = document.createElement('td');
    // One non-wrapping row: Approve stays a small text button (the main
    // call to action), Reject and Delete are compact icon buttons.
    const actions = document.createElement('div');
    actions.className = 'row-actions';
    const regLabel = reg.account_name || reg.external_id;
    if (reg.status === 'pending') {
      const approveBtn = document.createElement('button');
      approveBtn.type = 'button';
      approveBtn.className = 'btn btn-small btn-primary';
      approveBtn.textContent = 'Approve';
      approveBtn.setAttribute('aria-label', `Approve registration ${regLabel}`);
      approveBtn.addEventListener('click', () => handleApprove(reg));
      actions.appendChild(approveBtn);

      const rejectBtn = rowIconButton({
        icon: 'x-circle', text: 'Reject', title: 'Reject registration',
        ariaLabel: `Reject registration ${regLabel}`, className: 'btn-warning',
      });
      rejectBtn.addEventListener('click', () => void handleReject(reg));
      actions.appendChild(rejectBtn);
    } else if (reg.rejection_reason) {
      const span = document.createElement('span');
      span.className = 'text-muted';
      span.textContent = 'Rejected';
      span.title = reg.rejection_reason;
      actions.appendChild(span);
    }
    // Delete button (available for any status)
    const deleteBtn = rowIconButton({
      icon: 'trash-2', text: 'Delete', title: 'Delete registration',
      ariaLabel: `Delete registration ${regLabel}`, className: 'btn-danger',
    });
    deleteBtn.addEventListener('click', () => void handleDelete(reg));
    actions.appendChild(deleteBtn);
    actionsTd.appendChild(actions);

    row.appendChild(actionsTd);
    tbody.appendChild(row);
  }
  table.appendChild(tbody);
  container.appendChild(table);
  syncSelection(container);
}

/** Load and render registrations list. */
export async function loadRegistrations(): Promise<void> {
  const container = document.getElementById('registrations-list');
  if (!container) return;

  const filterEl = document.getElementById('registrations-status-filter') as HTMLSelectElement | null;
  // The select's default `selected` option is "pending" (see index.html), so on
  // first load `filterEl.value === 'pending'` and only pending registrations
  // are fetched. This is intentional (refs #45): admins almost always come to
  // this panel to action the pending queue, and surfacing approved/rejected
  // rows by default added noise. Operators who need other states can pick
  // them from the dropdown — picking "All" yields an empty string, which the
  // `status || undefined` coercion below turns into "no status filter".
  const status = filterEl?.value ?? '';

  try {
    const regs = await api.listRegistrations(status || undefined);
    renderRegistrationsTable(container, regs);
  } catch {
    container.textContent = '';
    const p = document.createElement('p');
    p.className = 'error-message';
    p.textContent = 'Failed to load registrations.';
    container.appendChild(p);
  }
}

function handleApprove(reg: AccountRegistration): void {
  const syntheticAccount = registrationToAccount(reg);

  openAccountModal(reg.provider as AccountProvider, syntheticAccount, {
    onSave: async (provider: AccountProvider, request: CloudAccountRequest) => {
      await api.approveRegistration(reg.id, request);
      await loadRegistrations();
      // Refresh the provider's account list so the newly-approved account appears
      // without requiring a page reload.
      await loadAccountsForProvider(provider);
    },
  });
}

async function handleDelete(reg: AccountRegistration): Promise<void> {
  const ok = await confirmDialog({
    title: `Delete registration for "${reg.account_name}"?`,
    body: `Provider: ${reg.provider}. External ID: ${reg.external_id}. This removes the registration request. The target account itself is not affected.`,
    confirmLabel: 'Delete registration',
    destructive: true,
  });
  if (!ok) return;
  try {
    await api.deleteRegistration(reg.id);
    await loadRegistrations();
  } catch (err) {
    showToast({ message: `Failed to delete registration: ${friendlyError(err)}`, kind: 'error' });
  }
}

function buildReasonBody(): { body: HTMLElement; reasonInput: HTMLTextAreaElement } {
  const body = document.createElement('div');
  const label = document.createElement('label');
  label.textContent = 'Reason (optional)';
  label.htmlFor = 'registration-reject-reason';
  const reasonInput = document.createElement('textarea');
  reasonInput.id = 'registration-reject-reason';
  reasonInput.rows = 3;
  reasonInput.className = 'registration-reject-reason';
  body.append(label, reasonInput);
  return { body, reasonInput };
}

async function handleBulkReject(regs: AccountRegistration[]): Promise<void> {
  if (regs.length === 0) return;
  const { body, reasonInput } = buildReasonBody();
  const ok = await confirmDialog({
    title: `Reject ${regs.length} registration${regs.length === 1 ? '' : 's'}?`,
    body,
    confirmLabel: `Reject ${regs.length}`,
    destructive: true,
  });
  if (!ok) return;
  const reason = reasonInput.value.trim() || undefined;
  const listEl = document.getElementById('registrations-list');
  bulkRunning = true;
  if (listEl) syncSelection(listEl);
  const failed: string[] = [];
  try {
    for (const reg of regs) {
      try {
        await api.rejectRegistration(reg.id, reason);
      } catch (err) {
        failed.push(`${reg.account_name || reg.external_id} (${friendlyError(err)})`);
      }
    }
  } finally {
    bulkRunning = false;
  }
  if (failed.length > 0) {
    showToast({
      message: `Rejected ${regs.length - failed.length} of ${regs.length}. Failed: ${failed.join('; ')}`,
      kind: 'error',
    });
  }
  await loadRegistrations();
}

async function handleReject(reg: AccountRegistration): Promise<void> {
  const { body, reasonInput } = buildReasonBody();
  const ok = await confirmDialog({
    title: `Reject registration for "${reg.account_name}"?`,
    body,
    confirmLabel: 'Reject registration',
    destructive: true,
  });
  if (!ok) return;
  const reason = reasonInput.value.trim();

  try {
    await api.rejectRegistration(reg.id, reason || undefined);
    await loadRegistrations();
  } catch (err) {
    // Use showToast instead of blocking alert() (finding 11-L3).
    showToast({ message: `Failed to reject registration: ${friendlyError(err)}`, kind: 'error' });
  }
}

/** Initialize the registrations panel: wire filter change and initial load. */
export function initRegistrations(): void {
  // Runs on every Accounts sub-tab visit, so wire the filter once; a stacked
  // listener per visit fired N racing loads per filter change.
  const filterEl = document.getElementById('registrations-status-filter');
  if (filterEl && filterEl.dataset['wired'] !== '1') {
    filterEl.dataset['wired'] = '1';
    filterEl.addEventListener('change', () => void loadRegistrations());
  }
  void loadRegistrations();
}
