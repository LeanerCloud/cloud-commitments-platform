/**
 * Group modal functionality
 */

import * as api from '../api';
import { currentEditingGroup, setCurrentEditingGroup } from './state';
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
export { closeDuplicateGroupModal, openDuplicateGroupModal, saveDuplicateGroup } from './groupModalsflow/duplicate';

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
