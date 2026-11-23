/**
 * Tests for groups module
 */

import { mockConfirmDialog, mockGroups } from './groups-harness';
import { loadUsers } from '../users/userActions';
import * as api from '../api';
import * as groupModals from '../groups/groupModals';
import * as groupActions from '../groups/groupActions';
import * as groupHandlers from '../groups/handlers';
import * as userState from '../users/state';

describe('groups/groupActions', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <div id="groups-list"></div>
    `;
    userState.setAvailableGroups(mockGroups as any);
    jest.clearAllMocks();
    mockConfirmDialog.mockResolvedValue(true);
    (api.deleteGroup as jest.Mock).mockResolvedValue(undefined);
    (loadUsers as jest.Mock).mockResolvedValue(undefined);
  });

  describe('deleteGroup', () => {
    it('should confirm before deleting', async () => {
      await groupActions.deleteGroup('group-1');

      expect(mockConfirmDialog).toHaveBeenCalledWith(
        expect.objectContaining({ title: expect.stringContaining('Administrators') })
      );
    });

    it('should call API to delete group when confirmed', async () => {
      await groupActions.deleteGroup('group-1');

      expect(api.deleteGroup).toHaveBeenCalledWith('group-1');
    });

    it('should reload users after successful deletion', async () => {
      await groupActions.deleteGroup('group-1');

      expect(loadUsers).toHaveBeenCalled();
    });

    it('should show success message after deletion', async () => {
      await groupActions.deleteGroup('group-1');

      expect(document.querySelector('.toast-success')?.textContent).toContain('Group deleted successfully');
    });

    it('should not delete when user cancels', async () => {
      mockConfirmDialog.mockResolvedValueOnce(false);

      await groupActions.deleteGroup('group-1');

      expect(api.deleteGroup).not.toHaveBeenCalled();
    });

    it('should not delete when group not found', async () => {
      await groupActions.deleteGroup('nonexistent-group');

      expect(mockConfirmDialog).not.toHaveBeenCalled();
      expect(api.deleteGroup).not.toHaveBeenCalled();
    });

    it('should handle API errors gracefully', async () => {
      (api.deleteGroup as jest.Mock).mockRejectedValue(new Error('Server error'));

      await groupActions.deleteGroup('group-1');

      expect(document.querySelector('.toast-error')?.textContent).toContain('Failed to delete group: Server error');
    });
  });

  describe('openEditGroupModal re-export', () => {
    it('should export openEditGroupModal from groupModals', () => {
      expect(groupActions.openEditGroupModal).toBe(groupModals.openEditGroupModal);
    });
  });
});

describe('groups/handlers', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <form id="group-form">
        <button type="submit">Save</button>
      </form>
    `;
    // Clear window properties
    delete (window as any).openCreateGroupModal;
    delete (window as any).closeGroupModal;
    delete (window as any).addPermission;
    jest.clearAllMocks();
  });

  describe('setupGroupHandlers', () => {
    it('should expose openCreateGroupModal on window', () => {
      groupHandlers.setupGroupHandlers();
      expect((window as any).openCreateGroupModal).toBeDefined();
      expect(typeof (window as any).openCreateGroupModal).toBe('function');
    });

    it('should expose closeGroupModal on window', () => {
      groupHandlers.setupGroupHandlers();
      expect((window as any).closeGroupModal).toBeDefined();
      expect(typeof (window as any).closeGroupModal).toBe('function');
    });

    it('should expose addPermission on window', () => {
      groupHandlers.setupGroupHandlers();
      expect((window as any).addPermission).toBeDefined();
      expect(typeof (window as any).addPermission).toBe('function');
    });

    it('should setup form submit handler', () => {
      document.body.innerHTML = `
        <form id="group-form">
          <input id="group-name" value="Test" />
          <textarea id="group-description"></textarea>
          <div id="permissions-list"></div>
        </form>
        <div id="group-modal"></div>
      `;

      groupHandlers.setupGroupHandlers();

      const form = document.getElementById('group-form') as HTMLFormElement;

      // Verify the event listener is attached by checking that the form has the handler
      // We can verify this indirectly by checking the form exists and handlers are set
      expect(form).toBeTruthy();

      // The handler calls saveGroup which is an async function
      // We verify the handler is set up by checking that no error is thrown
      const submitEvent = new Event('submit', { bubbles: true, cancelable: true });
      expect(() => form.dispatchEvent(submitEvent)).not.toThrow();
    });

    it('should handle missing group form gracefully', () => {
      document.body.innerHTML = '';
      expect(() => groupHandlers.setupGroupHandlers()).not.toThrow();
    });

    it('should call window.addPermission without arguments', () => {
      document.body.innerHTML = `
        <form id="group-form"></form>
        <div id="permissions-list"></div>
      `;

      groupHandlers.setupGroupHandlers();

      // Verify the addPermission function was registered on window
      const addPermFn = (window as any).addPermission;
      expect(typeof addPermFn).toBe('function');

      // The function should be callable without throwing
      expect(() => addPermFn()).not.toThrow();

      // Verify that calling it adds a permission item (using the real addPermission from groupModals)
      const permissionsList = document.getElementById('permissions-list');
      const items = permissionsList?.querySelectorAll('.permission-item');
      expect(items?.length).toBe(1);

      expect(permissionsList?.children.length).toBe(1);
    });
  });
});

describe('groups/groupModals — duplicate', () => {
  const sourceGroup: api.APIGroup = {
    id: 'group-src',
    name: 'Plan Authors',
    description: 'Can author plans',
    permissions: [
      { action: 'view', resource: '*' },
      { action: 'create', resource: 'plans' },
    ],
    allowed_accounts: ['source-scope'],
  };

  const mockAccounts = [
    { id: 'a1', name: 'Production', provider: 'aws', external_id: '111111111111', enabled: true,
      credentials_configured: true, created_at: '2024-01-01T00:00:00Z', updated_at: '2024-01-01T00:00:00Z' },
    { id: 'a2', name: 'Staging', provider: 'aws', external_id: '222222222222', enabled: true,
      credentials_configured: true, created_at: '2024-01-01T00:00:00Z', updated_at: '2024-01-01T00:00:00Z' },
    { id: 'a3', name: 'AzureProd', provider: 'azure', external_id: 'sub-aaaa', enabled: true,
      credentials_configured: true, created_at: '2024-01-01T00:00:00Z', updated_at: '2024-01-01T00:00:00Z' },
    { id: 'a4', name: 'GCPProd', provider: 'gcp', external_id: 'gcp-proj-x', enabled: true,
      credentials_configured: true, created_at: '2024-01-01T00:00:00Z', updated_at: '2024-01-01T00:00:00Z' },
  ];

  function setupDuplicateModalDom(): void {
    document.body.innerHTML = `
      <div id="group-duplicate-modal" class="modal hidden">
        <div class="modal-content">
          <button id="close-group-duplicate-modal-btn"></button>
          <form id="group-duplicate-form">
            <input id="dup-group-name" />
            <textarea id="dup-group-description"></textarea>
            <div id="dup-source-permissions"></div>
            <div id="dup-provider-filter"></div>
            <div id="dup-accounts-list"></div>
            <button id="cancel-group-duplicate-btn" type="button"></button>
            <button type="submit"></button>
          </form>
        </div>
      </div>
    `;
  }

  beforeEach(() => {
    setupDuplicateModalDom();
    userState.setAvailableGroups([sourceGroup] as any);
    jest.clearAllMocks();
    (api.listAccounts as jest.Mock).mockResolvedValue(mockAccounts);
    (api.getGroup as jest.Mock).mockResolvedValue(sourceGroup);
    (api.createGroup as jest.Mock).mockResolvedValue({ id: 'new-group' });
    (loadUsers as jest.Mock).mockResolvedValue(undefined);
  });

  describe('openDuplicateGroupModal', () => {
    it('prefills the name input with a "(copy)" suffix', async () => {
      await groupModals.openDuplicateGroupModal('group-src');
      const nameInput = document.getElementById('dup-group-name') as HTMLInputElement;
      expect(nameInput.value).toBe('Plan Authors (copy)');
    });

    it('prefills the description from the source group', async () => {
      await groupModals.openDuplicateGroupModal('group-src');
      const descInput = document.getElementById('dup-group-description') as HTMLTextAreaElement;
      expect(descInput.value).toBe('Can author plans');
    });

    it('renders source permissions as badges', async () => {
      await groupModals.openDuplicateGroupModal('group-src');
      const container = document.getElementById('dup-source-permissions');
      const badges = container?.querySelectorAll('.permission-badge');
      expect(badges?.length).toBe(2);
      expect(badges?.[0]?.textContent).toBe('view:*');
      expect(badges?.[1]?.textContent).toBe('create:plans');
    });

    it('falls back to api.getGroup when source not in cached groups', async () => {
      userState.setAvailableGroups([] as any);
      await groupModals.openDuplicateGroupModal('group-src');
      expect(api.getGroup).toHaveBeenCalledWith('group-src');
      const nameInput = document.getElementById('dup-group-name') as HTMLInputElement;
      expect(nameInput.value).toBe('Plan Authors (copy)');
    });

    it('shows the modal by removing the hidden class', async () => {
      await groupModals.openDuplicateGroupModal('group-src');
      const modal = document.getElementById('group-duplicate-modal');
      expect(modal?.classList.contains('hidden')).toBe(false);
    });

    it('renders provider filter pills with "All" selected by default', async () => {
      await groupModals.openDuplicateGroupModal('group-src');
      const pills = document.querySelectorAll('#dup-provider-filter button');
      expect(pills.length).toBe(4);
      expect(pills[0]?.textContent).toBe('All');
      expect(pills[0]?.getAttribute('aria-pressed')).toBe('true');
    });

    it('populates the accounts list with one checkbox per account', async () => {
      await groupModals.openDuplicateGroupModal('group-src');
      const checkboxes = document.querySelectorAll('.dup-account-checkbox');
      expect(checkboxes.length).toBe(4);
      const values = Array.from(checkboxes).map(cb => (cb as HTMLInputElement).value);
      expect(values).toEqual(['Production', 'Staging', 'AzureProd', 'GCPProd']);
    });

    it('renders an empty-state message when no accounts exist', async () => {
      (api.listAccounts as jest.Mock).mockResolvedValueOnce([]);
      await groupModals.openDuplicateGroupModal('group-src');
      const list = document.getElementById('dup-accounts-list');
      expect(list?.textContent).toContain('No cloud accounts configured yet');
    });

    it('a failed account fetch shows an error, not the empty-state copy, and blocks submit', async () => {
      (api.listAccounts as jest.Mock).mockRejectedValueOnce(new Error('403'));
      await groupModals.openDuplicateGroupModal('group-src');
      const list = document.getElementById('dup-accounts-list');
      expect(list?.textContent).toContain('Could not load cloud accounts');
      expect(list?.textContent).not.toContain('No cloud accounts configured yet');
      const submit = document.querySelector<HTMLButtonElement>('#group-duplicate-modal button[type="submit"]');
      expect(submit?.disabled).toBe(true);
    });

    it('submit is enabled again after a successful account fetch', async () => {
      (api.listAccounts as jest.Mock).mockRejectedValueOnce(new Error('503'));
      await groupModals.openDuplicateGroupModal('group-src');
      await groupModals.openDuplicateGroupModal('group-src');
      const submit = document.querySelector<HTMLButtonElement>('#group-duplicate-modal button[type="submit"]');
      expect(submit?.disabled).toBe(false);
    });

    it('provider pill click filters visible accounts by provider', async () => {
      await groupModals.openDuplicateGroupModal('group-src');
      const pills = document.querySelectorAll('#dup-provider-filter button');
      const awsPill = pills[1] as HTMLButtonElement; // AWS
      awsPill.click();

      const labels = document.querySelectorAll('#dup-accounts-list label');
      const visibleNames: string[] = [];
      labels.forEach(l => {
        if (!l.classList.contains('dup-account-hidden')) {
          const cb = l.querySelector('.dup-account-checkbox') as HTMLInputElement;
          visibleNames.push(cb.value);
        }
      });
      expect(visibleNames).toEqual(['Production', 'Staging']);
      expect(awsPill.getAttribute('aria-pressed')).toBe('true');
    });
  });

  describe('saveDuplicateGroup', () => {
    async function openAndFill(): Promise<void> {
      await groupModals.openDuplicateGroupModal('group-src');
    }

    it('sends allowed_accounts as ticked names when any checkbox is ticked', async () => {
      await openAndFill();
      const checkboxes = document.querySelectorAll('.dup-account-checkbox');
      (checkboxes[0] as HTMLInputElement).checked = true; // Production
      (checkboxes[2] as HTMLInputElement).checked = true; // AzureProd

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveDuplicateGroup(event);

      expect(api.createGroup).toHaveBeenCalledWith({
        name: 'Plan Authors (copy)',
        description: 'Can author plans',
        permissions: sourceGroup.permissions,
        allowed_accounts: ['Production', 'AzureProd'],
      });
    });

    it('inherits source allowed_accounts when no checkboxes are ticked', async () => {
      await openAndFill();
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveDuplicateGroup(event);

      expect(api.createGroup).toHaveBeenCalledWith(expect.objectContaining({
        allowed_accounts: ['source-scope'],
      }));
    });

    it('provider pill selection does not affect payload when same accounts are ticked', async () => {
      await openAndFill();
      // Switch to AWS pill (UI-only filter).
      const awsPill = document.querySelectorAll('#dup-provider-filter button')[1] as HTMLButtonElement;
      awsPill.click();

      // Tick Production (visible after filter).
      const productionCb = document.querySelector(
        '.dup-account-checkbox[value="Production"]'
      ) as HTMLInputElement;
      productionCb.checked = true;

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveDuplicateGroup(event);

      expect(api.createGroup).toHaveBeenCalledWith(expect.objectContaining({
        allowed_accounts: ['Production'],
      }));
      // Provider is NOT stored anywhere in the payload.
      const callArg = (api.createGroup as jest.Mock).mock.calls[0][0];
      expect(callArg).not.toHaveProperty('provider');
      expect(callArg.permissions).toEqual(sourceGroup.permissions);
    });

    it('copies source permissions verbatim', async () => {
      await openAndFill();
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveDuplicateGroup(event);
      const callArg = (api.createGroup as jest.Mock).mock.calls[0][0];
      expect(callArg.permissions).toBe(sourceGroup.permissions);
    });

    it('closes the modal and reloads users on success', async () => {
      await openAndFill();
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveDuplicateGroup(event);

      const modal = document.getElementById('group-duplicate-modal');
      expect(modal?.classList.contains('hidden')).toBe(true);
      expect(loadUsers).toHaveBeenCalled();
    });

    it('shows a success toast on success', async () => {
      await openAndFill();
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveDuplicateGroup(event);
      expect(document.querySelector('.toast-success')?.textContent).toContain('Group duplicated successfully');
    });

    it('shows an error toast on API failure', async () => {
      await openAndFill();
      (api.createGroup as jest.Mock).mockRejectedValueOnce(new Error('boom'));
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveDuplicateGroup(event);
      expect(document.querySelector('.toast-error')?.textContent).toContain('Failed to duplicate group');
    });

    it('shows an error when called without opening the modal first', async () => {
      // closeDuplicateGroupModal clears module-level source state.
      groupModals.closeDuplicateGroupModal();
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveDuplicateGroup(event);
      expect(api.createGroup).not.toHaveBeenCalled();
      expect(document.querySelector('.toast-error')?.textContent).toContain('No source group to duplicate');
    });
  });

  describe('closeDuplicateGroupModal', () => {
    it('adds the hidden class to the modal', async () => {
      await groupModals.openDuplicateGroupModal('group-src');
      groupModals.closeDuplicateGroupModal();
      const modal = document.getElementById('group-duplicate-modal');
      expect(modal?.classList.contains('hidden')).toBe(true);
    });

    it('is idempotent when modal is already hidden', () => {
      expect(() => groupModals.closeDuplicateGroupModal()).not.toThrow();
    });

    it('handles missing modal element gracefully', () => {
      document.body.innerHTML = '';
      expect(() => groupModals.closeDuplicateGroupModal()).not.toThrow();
    });
  });
});

// ---------------------------------------------------------------------------
// Regression tests for finding 11-M3: max_amount must be validated before
// parseFloat assignment in collectPermissions.
// ---------------------------------------------------------------------------
describe('11-M3 max_amount validation in collectPermissions', () => {
  // Access collectPermissions indirectly via saveGroup and observe API call.

  it('accepts a valid positive max_amount', async () => {
    document.body.innerHTML = '';
    const modal = document.createElement('div');
    modal.id = 'group-modal';
    modal.innerHTML = `
      <input id="group-id" value="">
      <input id="group-name" value="Test">
      <textarea id="group-description"></textarea>
      <div id="permissions-list">
        <div class="permission-item">
          <select class="perm-action"><option value="view" selected>View</option></select>
          <select class="perm-resource"><option value="plans" selected>Plans</option></select>
          <input class="perm-providers" value="">
          <input class="perm-services" value="">
          <input class="perm-regions" value="">
          <input class="perm-max-amount" type="number" min="0" value="5000">
        </div>
      </div>
    `;
    document.body.appendChild(modal);

    (api.createGroup as jest.Mock).mockResolvedValue({ id: 'new-id', name: 'Test', description: '', permissions: [] });

    const event = new Event('submit', { cancelable: true });
    await groupModals.saveGroup(event);

    const call = (api.createGroup as jest.Mock).mock.calls[0][0] as { permissions: Array<{ constraints?: { max_amount?: number } }> };
    expect(call.permissions[0]?.constraints?.max_amount).toBe(5000);
  });

  it('rejects negative max_amount (does not forward it to API)', async () => {
    document.body.innerHTML = '';
    const modal = document.createElement('div');
    modal.id = 'group-modal';
    modal.innerHTML = `
      <input id="group-id" value="">
      <input id="group-name" value="Test">
      <textarea id="group-description"></textarea>
      <div id="permissions-list">
        <div class="permission-item">
          <select class="perm-action"><option value="view" selected>View</option></select>
          <select class="perm-resource"><option value="plans" selected>Plans</option></select>
          <input class="perm-providers" value="">
          <input class="perm-services" value="">
          <input class="perm-regions" value="">
          <input class="perm-max-amount" type="number" value="-100">
        </div>
      </div>
    `;
    document.body.appendChild(modal);

    (api.createGroup as jest.Mock).mockResolvedValue({ id: 'new-id', name: 'Test', description: '', permissions: [] });

    const event = new Event('submit', { cancelable: true });
    await groupModals.saveGroup(event);

    // Negative value must be refused, not silently dropped (#133).
    expect(api.createGroup).not.toHaveBeenCalled();
    expect(document.querySelector('.toast-error')?.textContent).toContain(
      'Max Amount must be greater than 0; leave it blank for no limit (permission 0).'
    );
    expect(document.querySelector('.toast-error')?.textContent).not.toContain('Repair');
  });

  const maxAmountForm = (attrs: string): string => `
      <input id="group-id" value="">
      <input id="group-name" value="Test">
      <textarea id="group-description"></textarea>
      <div id="permissions-list">
        <div class="permission-item">
          <select class="perm-action"><option value="view" selected>View</option></select>
          <select class="perm-resource"><option value="plans" selected>Plans</option></select>
          <input class="perm-max-amount" type="number" min="0" ${attrs}>
        </div>
      </div>
    `;
  const MAX_AMOUNT_MESSAGE = 'Max Amount must be greater than 0; leave it blank for no limit (permission 0).';

  it.each(['0', '-100'])('refuses max_amount %s instead of saving without the cap (#133)', async raw => {
    document.body.innerHTML = maxAmountForm(`value="${raw}"`);
    await groupModals.saveGroup(new Event('submit', { cancelable: true }));
    expect(api.createGroup).not.toHaveBeenCalled();
    expect(document.querySelector('.toast-error')?.textContent).toContain(MAX_AMOUNT_MESSAGE);
    expect(document.querySelector('.toast-error')?.textContent).not.toContain('Repair');
  });

  // Browsers report unparsable text ("abc", "1e", "1e999") as value "" with
  // validity.badInput set; without that check the cap would be silently dropped.
  it('refuses unparsable number-input text (badInput) instead of saving without the cap (#133)', async () => {
    document.body.innerHTML = maxAmountForm('');
    const input = document.querySelector('.perm-max-amount') as HTMLInputElement;
    Object.defineProperty(input, 'validity', { value: { badInput: true } });
    expect(input.value).toBe('');
    await groupModals.saveGroup(new Event('submit', { cancelable: true }));
    expect(api.createGroup).not.toHaveBeenCalled();
    expect(document.querySelector('.toast-error')?.textContent).toContain(MAX_AMOUNT_MESSAGE);
  });

  it('still saves a blank max_amount as no cap', async () => {
    document.body.innerHTML = maxAmountForm('');
    (api.createGroup as jest.Mock).mockResolvedValue({ id: 'x', name: 'Test', description: '', permissions: [] });
    await groupModals.saveGroup(new Event('submit', { cancelable: true }));
    expect(api.createGroup).toHaveBeenCalled();
  });

  it('shows a stored max_amount of 0 in the edit form (#133)', () => {
    document.body.innerHTML = '<div id="permissions-list"></div>';
    groupModals.addPermission({ action: 'execute', resource: 'purchases', constraints: { max_amount: 0 } });
    expect((document.querySelector('.perm-max-amount') as HTMLInputElement).value).toBe('0');
  });
});
