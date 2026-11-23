/**
 * Shared setup for the group-edit permission regression suites
 * (group-edit-unrepresentable-permissions*.test.ts): the module mocks, the
 * Purchaser fixture, the modal DOM builders and the save/click helpers. Import
 * this module FIRST so the mocks register before the suites load the group
 * modules. This file is not a test (jest only collects *.test.ts).
 */
import './setup';

import * as api from '../api';
import * as groupModals from '../groups/groupModals';
import { setupGroupHandlers } from '../groups/handlers';

jest.mock('../api', () => ({
  getGroup: jest.fn(),
  updateGroup: jest.fn(),
  createGroup: jest.fn(),
}));

jest.mock('../users/userActions', () => ({
  loadUsers: jest.fn().mockResolvedValue(undefined),
}));

jest.mock('../confirmDialog', () => ({
  confirmDialog: jest.fn().mockResolvedValue(true),
}));

// Only the toast functions are stubbed; escapeHtml and the rest stay real,
// because the escaping tests below depend on the genuine implementation.
jest.mock('../users/utils', () => ({
  ...jest.requireActual('../users/utils'),
  showError: jest.fn(),
  showSuccess: jest.fn(),
}));

// The exact seeded Purchaser group permission set (PURCHASER_PERMS in
// permissions.generated.ts), reproduced here as a fixture so the test
// exercises the real failure scenario rather than a synthetic one.
export const PURCHASER_PERMISSIONS: api.Permission[] = [
  { action: 'approve-any', resource: 'purchases' },
  { action: 'execute', resource: 'purchases' },
  { action: 'retry-any', resource: 'purchases' },
  { action: 'view', resource: 'history' },
  { action: 'view', resource: 'plans' },
  { action: 'view', resource: 'purchases' },
  { action: 'view', resource: 'recommendations' },
];

export function setUpModalDom(): void {
  document.body.innerHTML = `
    <div id="group-modal" class="hidden">
      <span id="group-modal-title"></span>
      <form id="group-form">
        <input id="group-id" />
        <input id="group-name" />
        <textarea id="group-description"></textarea>
        <div id="permissions-list"></div>
      </form>
    </div>
  `;
}

// Simulates opening a group for edit, then saving after ONLY the
// description changed (the exact scenario from the issue: "an admin
// opens a group, changes only the description, and clicks Save").
export async function openEditDescriptionOnlyAndSave(group: api.APIGroup, newDescription: string): Promise<void> {
  (api.getGroup as jest.Mock).mockResolvedValue(group);
  await groupModals.openEditGroupModal(group.id);

  (document.getElementById('group-description') as HTMLTextAreaElement).value = newDescription;

  const event = { preventDefault: jest.fn() } as unknown as Event;
  await groupModals.saveGroup(event);
}

// permissions list, and a real submit button.
export function setUpRealFormDom(): void {
  document.body.innerHTML = `
    <div id="group-modal" class="hidden">
      <span id="group-modal-title"></span>
      <form id="group-form">
        <input type="hidden" id="group-id">
        <input type="text" id="group-name" required>
        <textarea id="group-description"></textarea>
        <div id="permissions-list"></div>
        <button type="submit" class="primary">Save group</button>
      </form>
    </div>
  `;
  setupGroupHandlers();
}

export function submitButton(): HTMLButtonElement {
  return document.querySelector('#group-form button[type="submit"]') as HTMLButtonElement;
}

// Clicks Save the way an admin does and waits for saveGroup's promise
// chain to settle (the submit listener fires it without awaiting).
export async function clickSave(): Promise<void> {
  submitButton().click();
  await new Promise(resolve => setTimeout(resolve, 0));
}

export async function openEdit(group: api.APIGroup): Promise<void> {
  (api.getGroup as jest.Mock).mockResolvedValue(group);
  await groupModals.openEditGroupModal(group.id);
}

export function accountsInput(index = 0): HTMLInputElement {
  const items = document.querySelectorAll('.permission-item');
  return items[index]!.querySelector('.perm-accounts') as HTMLInputElement;
}

export function savedPermissions(): api.Permission[] {
  const call = (api.updateGroup as jest.Mock).mock.calls[0];
  return call[1].permissions as api.Permission[];
}

// A realistic account-scoped operator group. The second permission is the
// narrowest form of the bug: accounts is its ONLY constraint, so the old
// `if (providers || services || regions || maxAmount)` guard never fired
// and the permission round-tripped fully unconstrained.
export const ACCOUNT_SCOPED_PERMISSIONS: api.Permission[] = [
  { action: 'update-any', resource: 'purchases', constraints: { accounts: ['acct-prod-1'], max_amount: 5000 } },
  { action: 'view', resource: 'history', constraints: { accounts: ['acct-prod-1'] } },
];

export function accountScopedGroup(): api.APIGroup {
  return {
    id: 'operators-group-id',
    name: 'Prod Operators',
    description: 'Old description',
    permissions: ACCOUNT_SCOPED_PERMISSIONS,
    created_at: '2024-01-01T00:00:00Z',
  };
}
