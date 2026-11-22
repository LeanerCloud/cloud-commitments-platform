import * as api from '../../api';
import { deleteAccount } from '../../settings-account-delete';
import { showToast } from '../../toast';
import { rowIconButton } from '../../lib/row-icon-button';
import { openAccountModal } from './modal';
import { openAccountOverridesModal } from '../settings-overrides';
import type { AccountProvider } from '../settings-state';

/**
 * Load and render accounts list for a provider
 */
export async function loadAccountsForProvider(provider: AccountProvider): Promise<void> {
  const container = document.getElementById(`${provider}-accounts-list`);
  if (!container) return;
  try {
    const accounts = await api.listAccounts({ provider });
    renderSelfAccountBanner(container, accounts, provider);
    renderAccountsList(container, accounts, provider);
  } catch {
    container.textContent = 'Failed to load accounts.';
  }
}

/**
 * Show a banner to add CUDly's own host account if not already present.
 * Only shown for the provider matching CUDLY_SOURCE_CLOUD.
 */
async function renderSelfAccountBanner(container: HTMLElement, accounts: api.CloudAccount[], provider: AccountProvider): Promise<void> {
  // Remove any existing banner first (prevents duplicates on re-render)
  container.querySelector('.self-account-banner')?.remove();

  const hasSelf = accounts.some(a => a.is_self);
  if (hasSelf) return;

  // Check if this provider matches the source cloud
  try {
    const cfg = await api.getConfig();
    if (!cfg.source_identity || cfg.source_identity.provider !== provider) return;
    const extId = cfg.source_identity.account_id || cfg.source_identity.subscription_id || cfg.source_identity.project_id;
    if (!extId) return;

    const banner = document.createElement('div');
    banner.className = 'account-row self-account-banner';

    const info = document.createElement('span');
    info.className = 'account-info';
    info.textContent = `CUDly host account (${extId})`;
    banner.appendChild(info);

    const addBtn = document.createElement('button');
    addBtn.type = 'button';
    addBtn.className = 'btn btn-small';
    addBtn.textContent = 'Add';
    addBtn.addEventListener('click', async () => {
      addBtn.disabled = true;
      addBtn.textContent = 'Adding...';
      try {
        await api.createSelfAccount();
        await loadAccountsForProvider(provider);
      } catch (e: unknown) {
        const msg = e instanceof Error ? e.message : String(e);
        if (msg.includes('409')) {
          await loadAccountsForProvider(provider);
        } else {
          addBtn.textContent = 'Failed';
        }
      }
    });
    banner.appendChild(addBtn);

    container.prepend(banner);
  } catch {
    // Config fetch failed — skip silently
  }
}

/**
 * Render accounts list into a container element as a scannable table.
 *
 * Replaces the prior inline-row layout ("Account Name (12345) Edit Test
 * Credentials Overrides Delete" as a single text-flow). The table surfaces
 * each field in its own column, uses a status pill for Active/Disabled,
 * and isolates the destructive Delete action in the right-most column.
 */
type AccountStatusFilter = 'all' | 'active' | 'disabled';

/**
 * Test account credentials. Surfaces progress + outcome via toast so the
 * signal isn't confined to the button label (which auto-clears in 3s and
 * provides no detail beyond OK/Failed). The button keeps its inline status
 * as a secondary affordance so the user's gaze point — the row they just
 * clicked — still gets feedback.
 */
async function testAccount(accountId: string, accountLabel: string, btn: HTMLButtonElement): Promise<void> {
  // The row action is an icon button: progress and result go to its hidden
  // text, its tooltip and a data-test-result tint; the toasts carry the
  // message itself.
  const labelEl = btn.querySelector<HTMLElement>('.sr-only') ?? btn;
  const original = labelEl.textContent;
  const originalTitle = btn.title;
  const show = (text: string, result?: 'ok' | 'failed'): void => {
    labelEl.textContent = text;
    btn.title = text;
    if (result) btn.dataset['testResult'] = result;
  };
  const restore = (): void => {
    labelEl.textContent = original;
    btn.title = originalTitle;
    delete btn.dataset['testResult'];
    btn.disabled = false;
  };
  btn.disabled = true;
  show('Testing...');
  showToast({ message: `Testing credentials for ${accountLabel}…`, kind: 'info', timeout: 3_000 });
  try {
    const result = await api.testAccountCredentials(accountId);
    show(result.ok ? 'OK' : 'Failed', result.ok ? 'ok' : 'failed');
    if (result.ok) {
      const detail = result.message ? `: ${result.message}` : '';
      showToast({ message: `Credentials OK for ${accountLabel}${detail}`, kind: 'success', timeout: 5_000 });
    } else {
      showToast({
        message: `Credentials failed for ${accountLabel}${result.message ? `: ${result.message}` : ''}`,
        kind: 'error',
        timeout: null,
      });
    }
    setTimeout(restore, 3000);
  } catch (err: unknown) {
    show('Error', 'failed');
    const msg = err instanceof Error ? err.message : String(err);
    showToast({ message: `Failed to test credentials for ${accountLabel}: ${msg}`, kind: 'error', timeout: null });
    setTimeout(restore, 3000);
  }
}

function renderAccountsList(
  container: HTMLElement,
  accounts: api.CloudAccount[],
  provider: AccountProvider,
  filter: AccountStatusFilter = 'all'
): void {
  // Remove prior rendered rows (banner lives in a separate className
  // managed by renderSelfAccountBanner). `.account-overrides-panel` used
  // to be cleaned up here too — it was the inline expandable panel each
  // account carried before #122/#124 moved overrides into a per-account
  // modal. No such elements exist anymore.
  container.querySelectorAll(
    '.accounts-table, .accounts-empty, .status-chip-row'
  ).forEach(el => el.remove());

  if (!accounts || accounts.length === 0) {
    if (!container.querySelector('.self-account-banner')) {
      const empty = document.createElement('p');
      empty.className = 'accounts-empty';
      empty.textContent = 'No accounts configured.';
      container.appendChild(empty);
    }
    return;
  }

  // Status chip filter — mirrors the P3 audit recommendation scoped to
  // states the per-provider table actually exposes (Active/Disabled).
  const activeCount = accounts.filter(a => a.enabled).length;
  const disabledCount = accounts.length - activeCount;
  const chipRow = document.createElement('div');
  chipRow.className = 'status-chip-row';
  chipRow.setAttribute('role', 'tablist');
  chipRow.setAttribute('aria-label', 'Filter accounts by status');
  const chips: Array<{ key: AccountStatusFilter; label: string; count: number }> = [
    { key: 'all', label: 'All', count: accounts.length },
    { key: 'active', label: 'Active', count: activeCount },
    { key: 'disabled', label: 'Disabled', count: disabledCount },
  ];
  chips.forEach(({ key, label, count }) => {
    const chip = document.createElement('button');
    chip.type = 'button';
    chip.className = 'status-chip' + (filter === key ? ' active' : '');
    chip.textContent = `${label} (${count})`;
    chip.setAttribute('role', 'tab');
    chip.setAttribute('aria-selected', String(filter === key));
    chip.addEventListener('click', () => {
      if (filter === key) return;
      renderAccountsList(container, accounts, provider, key);
    });
    chipRow.appendChild(chip);
  });
  container.appendChild(chipRow);

  const visible = accounts.filter(a => {
    if (filter === 'active') return a.enabled;
    if (filter === 'disabled') return !a.enabled;
    return true;
  });

  const table = document.createElement('table');
  table.className = 'accounts-table';
  const thead = document.createElement('thead');
  const headerRow = document.createElement('tr');
  ['Name', 'Account ID', 'Status', 'Actions'].forEach((label) => {
    const th = document.createElement('th');
    th.textContent = label;
    headerRow.appendChild(th);
  });
  thead.appendChild(headerRow);
  table.appendChild(thead);

  const tbody = document.createElement('tbody');

  visible.forEach((account) => {
    const accountLabel = account.name.includes(account.external_id)
      ? account.name
      : `${account.name} (${account.external_id})`;
    const tr = document.createElement('tr');

    const nameTd = document.createElement('td');
    nameTd.textContent = account.name;
    if (account.is_self) {
      const self = document.createElement('span');
      self.className = 'badge badge-info';
      self.textContent = ' Self';
      self.setAttribute('title', 'The cloud account running CUDly itself');
      nameTd.appendChild(self);
    }
    tr.appendChild(nameTd);

    const idTd = document.createElement('td');
    idTd.className = 'monospace';
    idTd.textContent = account.external_id;
    tr.appendChild(idTd);

    const statusTd = document.createElement('td');
    const status = document.createElement('span');
    status.className = account.enabled
      ? 'badge badge-success'
      : 'badge badge-warning';
    status.textContent = account.enabled ? 'Active' : 'Disabled';
    statusTd.appendChild(status);
    tr.appendChild(statusTd);

    const actionsTd = document.createElement('td');
    actionsTd.className = 'account-actions';
    // Compact icon buttons in one non-wrapping row, like the Users rows.
    const actions = document.createElement('div');
    actions.className = 'row-actions';

    const editBtn = rowIconButton({
      icon: 'edit', text: 'Edit', title: 'Edit account', ariaLabel: `Edit ${accountLabel}`,
    });
    editBtn.addEventListener('click', () => openAccountModal(provider, account));
    actions.appendChild(editBtn);

    const testBtn = rowIconButton({
      icon: 'zap', text: 'Test', title: 'Test credentials', ariaLabel: `Test credentials for ${accountLabel}`,
    });
    testBtn.addEventListener('click', () => void testAccount(account.id, accountLabel, testBtn));
    actions.appendChild(testBtn);

    const overridesBtn = rowIconButton({
      icon: 'sliders', text: 'Overrides', title: 'Service overrides', ariaLabel: `Service overrides for ${accountLabel}`,
    });
    overridesBtn.addEventListener('click', () => openAccountOverridesModal(account));
    actions.appendChild(overridesBtn);

    // Destructive Delete: the subtle danger icon variant, set slightly apart
    // from the routine actions. .btn-destructive stays as the hook that
    // identifies the row's delete action.
    const deleteBtn = rowIconButton({
      icon: 'trash-2', text: 'Delete', title: 'Delete account', ariaLabel: `Delete ${accountLabel}`,
      className: 'btn-destructive btn-danger account-delete-btn',
    });
    deleteBtn.addEventListener('click', () => void deleteAccount(account, provider, loadAccountsForProvider));
    actions.appendChild(deleteBtn);

    actionsTd.appendChild(actions);
    tr.appendChild(actionsTd);
    tbody.appendChild(tr);
  });
  table.appendChild(tbody);
  container.appendChild(table);
  // Per-account overrides used to render in inline panels appended below
  // the table; they now live in the account-overrides-modal (issue #122)
  // so the accounts table stays compact and per-account scoping is
  // unambiguous.
}
