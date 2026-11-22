/**
 * Duplicate group modal.
 */

import * as api from '../../api';
import type { APIGroup, Permission } from '../../api';
import { availableGroups } from '../../users/state';
import { showError, showSuccess } from '../../users/utils';
import { loadUsers } from '../../users/userActions';
import { openModal, closeModal } from '../../modal';

// Module-level state for the duplicate modal — holds the source group so
// saveDuplicateGroup doesn't need another lookup.
let duplicateSourceGroup: APIGroup | null = null;

// ---------------------------------------------------------------------------
// Duplicate group modal
// ---------------------------------------------------------------------------

const DUP_PROVIDER_PILLS: Array<{ value: string; label: string }> = [
  { value: 'all',   label: 'All' },
  { value: 'aws',   label: 'AWS' },
  { value: 'azure', label: 'Azure' },
  { value: 'gcp',   label: 'GCP' },
];

/**
 * Render a read-only badge list of source permissions as "action:resource"
 * entries. Uses textContent + createElement to avoid innerHTML with user
 * strings.
 */
function renderSourcePermissionBadges(container: HTMLElement, permissions: Permission[]): void {
  container.textContent = '';
  if (permissions.length === 0) {
    const empty = document.createElement('span');
    empty.className = 'dup-empty';
    empty.textContent = 'No permissions on source group';
    container.appendChild(empty);
    return;
  }
  for (const perm of permissions) {
    const badge = document.createElement('span');
    badge.className = 'permission-badge';
    badge.textContent = `${perm.action}:${perm.resource}`;
    container.appendChild(badge);
  }
}

/**
 * Render the provider filter pills (All / AWS / Azure / GCP). Each pill
 * filters the visible account checkboxes by data-provider; selection is
 * UI-only and never stored in the created group.
 */
function renderDuplicateProviderPills(container: HTMLElement, accountsList: HTMLElement): void {
  container.textContent = '';
  const buttons: HTMLButtonElement[] = [];

  for (const pill of DUP_PROVIDER_PILLS) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn btn-small target-cloud-pill';
    btn.textContent = pill.label;
    btn.setAttribute('data-provider', pill.value);
    btn.setAttribute('aria-pressed', 'false');
    btn.addEventListener('click', () => {
      for (const b of buttons) {
        const selected = b === btn;
        b.setAttribute('aria-pressed', selected ? 'true' : 'false');
        b.classList.toggle('selected', selected);
      }
      applyDuplicateProviderFilter(accountsList, pill.value);
    });
    buttons.push(btn);
    container.appendChild(btn);
  }

  // Default selection: "All" (first option).
  const first = buttons[0];
  if (first) {
    first.setAttribute('aria-pressed', 'true');
    first.classList.add('selected');
  }
  applyDuplicateProviderFilter(accountsList, 'all');
}

/**
 * Hide/show account checkbox rows by data-provider. "all" shows everything.
 */
function applyDuplicateProviderFilter(accountsList: HTMLElement, provider: string): void {
  const labels = accountsList.querySelectorAll('label[data-provider]');
  labels.forEach(label => {
    const rowProvider = (label as HTMLElement).getAttribute('data-provider') || '';
    const visible = provider === 'all' || provider === rowProvider;
    label.classList.toggle('dup-account-hidden', !visible);
  });
}

function renderDuplicateAccountsError(container: HTMLElement): void {
  container.textContent = '';
  const msg = document.createElement('p');
  msg.className = 'dup-empty';
  msg.setAttribute('role', 'alert');
  msg.textContent = 'Could not load cloud accounts, so the duplicate cannot be scoped. Close this dialog and try again.';
  container.appendChild(msg);
}

/**
 * Render the account checkbox list. Each row is a label + checkbox whose
 * value is the account name (names are what the backend matcher accepts
 * for human-readable scoping).
 */
function renderDuplicateAccountsList(container: HTMLElement, accounts: api.CloudAccount[]): void {
  container.textContent = '';
  if (accounts.length === 0) {
    const empty = document.createElement('p');
    empty.className = 'dup-empty';
    empty.textContent = 'No cloud accounts configured yet. Duplicating without scope clones the full source group — add accounts first if you want to restrict.';
    container.appendChild(empty);
    return;
  }

  for (const acct of accounts) {
    const label = document.createElement('label');
    label.setAttribute('data-provider', acct.provider);

    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.className = 'dup-account-checkbox';
    cb.value = acct.name;
    cb.setAttribute('data-provider', acct.provider);

    const text = document.createElement('span');
    text.textContent = `${acct.name} (${acct.external_id}) [${acct.provider}]`;

    label.appendChild(cb);
    label.appendChild(text);
    container.appendChild(label);
  }
}

/**
 * Open the Duplicate Group modal for the given source group.
 *
 * Looks up the source in cached `availableGroups` first, falling back to
 * a fresh `api.getGroup` fetch. Prefills name (with " (copy)" suffix),
 * description, and renders source permissions as read-only badges.
 * Populates account checkboxes from `api.listAccounts()`.
 */
export async function openDuplicateGroupModal(groupId: string): Promise<void> {
  try {
    let source = availableGroups.find(g => g.id === groupId) || null;
    if (!source) {
      source = await api.getGroup(groupId);
    }
    duplicateSourceGroup = source;

    const modal = document.getElementById('group-duplicate-modal');
    if (!modal) return;

    const nameInput = document.getElementById('dup-group-name') as HTMLInputElement | null;
    const descInput = document.getElementById('dup-group-description') as HTMLTextAreaElement | null;
    const permsContainer = document.getElementById('dup-source-permissions');
    const providerFilter = document.getElementById('dup-provider-filter');
    const accountsList = document.getElementById('dup-accounts-list');

    if (nameInput) nameInput.value = `${source.name} (copy)`;
    if (descInput) descInput.value = source.description || '';
    if (permsContainer) renderSourcePermissionBadges(permsContainer, source.permissions);

    // Populate accounts, then wire provider pills to filter them.
    // A failed fetch must not read as "no accounts configured": that copy
    // nudges the operator to duplicate unscoped, which inherits the source's
    // full account scope. Show the failure and block submit instead.
    const submitBtn = modal.querySelector<HTMLButtonElement>('button[type="submit"]');
    let accounts: api.CloudAccount[] = [];
    let accountsFailed = false;
    try {
      accounts = await api.listAccounts();
    } catch (err) {
      console.error('Failed to list accounts for duplicate modal:', err);
      accountsFailed = true;
    }
    if (submitBtn) submitBtn.disabled = accountsFailed;
    if (accountsList) {
      if (accountsFailed) renderDuplicateAccountsError(accountsList);
      else renderDuplicateAccountsList(accountsList, accounts);
    }
    if (providerFilter && accountsList) renderDuplicateProviderPills(providerFilter, accountsList);

    openModal(modal);
  } catch (error) {
    console.error('Failed to open duplicate group modal:', error);
    showError('Failed to load group details');
  }
}

/**
 * Close the Duplicate Group modal and clear its module-level state.
 */
export function closeDuplicateGroupModal(): void {
  const modal = document.getElementById('group-duplicate-modal');
  if (modal) closeModal(modal);
  duplicateSourceGroup = null;
}

/**
 * Save the duplicate group — posts to the existing POST /api/groups
 * endpoint. If account checkboxes are ticked, their names become the new
 * group's `allowed_accounts`; otherwise the source's `allowed_accounts`
 * is inherited as-is. Permissions are copied verbatim from the source.
 */
export async function saveDuplicateGroup(e: Event): Promise<void> {
  e.preventDefault();

  const source = duplicateSourceGroup;
  if (!source) {
    showError('No source group to duplicate');
    return;
  }

  const nameInput = document.getElementById('dup-group-name') as HTMLInputElement | null;
  const descInput = document.getElementById('dup-group-description') as HTMLTextAreaElement | null;
  const accountsList = document.getElementById('dup-accounts-list');

  const name = nameInput?.value.trim() || '';
  const description = descInput?.value || '';

  const tickedNames: string[] = [];
  if (accountsList) {
    const checked = accountsList.querySelectorAll('.dup-account-checkbox:checked');
    checked.forEach(cb => {
      const val = (cb as HTMLInputElement).value;
      if (val) tickedNames.push(val);
    });
  }

  const allowedAccounts = tickedNames.length > 0
    ? tickedNames
    : (source.allowed_accounts || []);

  try {
    await api.createGroup({
      name,
      description,
      permissions: source.permissions,
      allowed_accounts: allowedAccounts,
    });
    showSuccess('Group duplicated successfully');
    closeDuplicateGroupModal();
    await loadUsers();
  } catch (error) {
    console.error('Failed to duplicate group:', error);
    const err = error as Error;
    showError(`Failed to duplicate group: ${err.message}`);
  }
}
