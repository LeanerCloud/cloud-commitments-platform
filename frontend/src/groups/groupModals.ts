/**
 * Group modal functionality
 */

import * as api from '../api';
import type { APIGroup, Permission } from '../api';
import { currentEditingGroup, setCurrentEditingGroup } from './state';
import { availableGroups } from '../users/state';
import { showError, showSuccess } from '../users/utils';
import { loadUsers } from '../users/userActions';
import { openModal, closeModal } from '../modal';
import {
  clearGroupFieldProblems, clearGroupFormValidation,
  showGroupFieldProblems, validateGroupForm,
} from './groupFormValidation';
import {
  collectPermissions, maxAmountInputErrors, NO_PERMISSIONS_FIELD_MESSAGE, renderPermissions,
  unrepresentablePermissionErrors,
} from './groupModalsflow/permissions';

export { addPermission } from './groupModalsflow/permissions';

// Module-level state for the duplicate modal — holds the source group so
// saveDuplicateGroup doesn't need another lookup.
let duplicateSourceGroup: APIGroup | null = null;

/**
 * Open create group modal
 */
export function openCreateGroupModal(): void {
  setCurrentEditingGroup(null);
  const modal = document.getElementById('group-modal');
  const title = document.getElementById('group-modal-title');
  const form = document.getElementById('group-form') as HTMLFormElement;

  if (!modal || !title || !form) return;

  title.textContent = 'Create group';
  form.reset();
  clearGroupFormValidation();
  (document.getElementById('group-id') as HTMLInputElement).value = '';

  // Clear permissions list
  const permissionsList = document.getElementById('permissions-list');
  if (permissionsList) {
    permissionsList.innerHTML = '';
  }

  openModal(modal);
}

/**
 * Open edit group modal
 */
export async function openEditGroupModal(groupId: string): Promise<void> {
  try {
    const group = await api.getGroup(groupId);
    setCurrentEditingGroup(group);

    const modal = document.getElementById('group-modal');
    const title = document.getElementById('group-modal-title');
    const form = document.getElementById('group-form') as HTMLFormElement;

    if (!modal || !title || !form) return;

    title.textContent = 'Edit group';
    clearGroupFormValidation();
    (document.getElementById('group-id') as HTMLInputElement).value = group.id;
    (document.getElementById('group-name') as HTMLInputElement).value = group.name;
    (document.getElementById('group-description') as HTMLTextAreaElement).value = group.description || '';

    // Render existing permissions
    renderPermissions(group.permissions);

    openModal(modal);
  } catch (error) {
    console.error('Failed to load group:', error);
    showError('Failed to load group details');
  }
}

/**
 * Close group modal
 */
export function closeGroupModal(): void {
  const modal = document.getElementById('group-modal');
  if (modal) {
    closeModal(modal);
  }
  setCurrentEditingGroup(null);
}

/**
 * Save group (create or update)
 */
export async function saveGroup(e: Event): Promise<void> {
  e.preventDefault();

  // Issue #213: a blank name or a row with no action/resource is refused
  // with a note under the field, never dropped or widened (#group-form is
  // novalidate, so this is the only gate).
  if (validateGroupForm()) return;
  clearGroupFieldProblems();

  // Refuse rather than widen. A stored constraint value this form cannot
  // carry unchanged is flagged when the row is rendered; saving anyway would
  // re-submit a materially different permission set than the one loaded, and
  // for a constraint list that means an unintended widening (an empty list is
  // "no restriction" at enforcement). The group stays uneditable through the
  // form until the value is repaired via the API or in the database.
  // Issue #727: each refusal below also shows under the field to fix.
  const blocked = unrepresentablePermissionErrors();
  if (blocked.length > 0) {
    showGroupFieldProblems(blocked);
    showError(
      `Cannot save: ${blocked.map(p => p.summary).filter(Boolean).join('; ')}. ` +
      'Saving would silently drop the restriction, so the edit is refused. ' +
      'Repair the value through the API or in the database, then reload this page.'
    );
    return;
  }

  const badAmounts = maxAmountInputErrors();
  if (badAmounts.length > 0) {
    showGroupFieldProblems(badAmounts);
    showError(
      `Max Amount must be greater than 0; leave it blank for no limit (${badAmounts.map(p => p.summary).join(', ')}).`
    );
    return;
  }

  const name = (document.getElementById('group-name') as HTMLInputElement).value;
  const description = (document.getElementById('group-description') as HTMLTextAreaElement).value;
  const permissions = collectPermissions();

  // Refuse an empty permission list rather than submit it. Rows left on a
  // placeholder are refused above, so this is reached by removing every
  // row. The update API treats an empty list as "not sent"
  // and leaves the group's stored permissions untouched (issue #237), so
  // submitting would show "Group updated successfully" while silently
  // keeping the group's old privileges -- there is no way for this form to
  // express "clear all permissions".
  if (permissions.length === 0) {
    const add = document.getElementById('add-permission-btn');
    if (add) {
      showGroupFieldProblems([{ control: add, message: NO_PERMISSIONS_FIELD_MESSAGE, summary: '' }]);
    }
    showError(
      'A group must grant at least one permission. ' +
      'To remove all privileges, delete the group instead.'
    );
    return;
  }

  try {
    if (currentEditingGroup) {
      // Update existing group
      await api.updateGroup(currentEditingGroup.id, {
        name,
        description,
        permissions
      });
      showSuccess('Group updated successfully');
    } else {
      // Create new group
      await api.createGroup({
        name,
        description,
        permissions
      });
      showSuccess('Group created successfully');
    }

    closeGroupModal();
    await loadUsers();
  } catch (error) {
    console.error('Failed to save group:', error);
    const err = error as Error;
    showError(`Failed to save group: ${err.message}`);
  }
}

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
