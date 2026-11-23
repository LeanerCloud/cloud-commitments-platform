/**
 * Tests for users handlers, integration and bulk-actions toolbar
 */

// The harness registers the module mocks, so it must be imported first.
import './users-harness';

import * as api from '../api';
import * as userState from '../users/state';
import * as userModals from '../users/userModals';
import * as userHandlers from '../users/handlers';

// ============================================================================
// HANDLERS TESTS
// ============================================================================
describe('users/handlers', () => {
  const mockGroups = [
    { id: 'admins', name: 'Admins', permissions: [], description: '' },
    { id: 'users', name: 'Users', permissions: [], description: '' }
  ];

  beforeEach(() => {
    document.body.innerHTML = `
      <form id="user-form"></form>
      <input type="text" id="user-search" />
      <select id="user-mfa-filter">
        <option value="">All</option>
        <option value="enabled">Enabled</option>
        <option value="disabled">Disabled</option>
      </select>
      <select id="user-group-filter">
        <option value="">All Groups</option>
      </select>
      <button id="clear-filters-btn">Clear</button>
      <button id="bulk-delete-btn">Delete</button>
      <button id="bulk-role-btn">Change Role</button>
      <button id="bulk-group-btn">Add to Group</button>
      <div id="users-list"></div>
      <div id="user-stats"></div>
      <div id="bulk-actions-bar" class="hidden">
        <span id="selected-count">0</span>
      </div>
    `;

    userState.setAvailableGroups(mockGroups as any);
    userState.setAllUsers([]);
    userState.setFilteredUsers([]);
    userState.setSearchQuery('');
    userState.setMfaFilter('');
    userState.setGroupFilter('');
    userState.clearSelectedUserIds();

    jest.clearAllMocks();
  });

  describe('setupUserHandlers', () => {
    it('should set up global modal functions', () => {
      userHandlers.setupUserHandlers();

      expect((window as any).openCreateUserModal).toBeDefined();
      expect((window as any).closeUserModal).toBeDefined();
    });

    it('should set up form submit handler', () => {
      // saveUser is wired via addEventListener('submit', ...), so form.onsubmit
      // stays null. Prove the listener is actually attached by spying on saveUser
      // and dispatching a real submit event, then asserting the observable call.
      const saveUserSpy = jest.spyOn(userModals, 'saveUser').mockResolvedValue(undefined);

      userHandlers.setupUserHandlers();

      const form = document.getElementById('user-form') as HTMLFormElement;
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));

      expect(saveUserSpy).toHaveBeenCalledTimes(1);
      saveUserSpy.mockRestore();
    });

    it('should set up search input handler', () => {
      userHandlers.setupUserHandlers();

      const searchInput = document.getElementById('user-search') as HTMLInputElement;
      searchInput.value = 'test';
      searchInput.dispatchEvent(new Event('input', { bubbles: true }));

      expect(userState.searchQuery).toBe('test');
    });

    it('should set up mfa filter handler', () => {
      userHandlers.setupUserHandlers();

      const mfaFilter = document.getElementById('user-mfa-filter') as HTMLSelectElement;
      mfaFilter.value = 'enabled';
      mfaFilter.dispatchEvent(new Event('change', { bubbles: true }));

      expect(userState.mfaFilter).toBe('enabled');
    });

    it('should set up group filter handler', () => {
      userHandlers.setupUserHandlers();

      const groupFilter = document.getElementById('user-group-filter') as HTMLSelectElement;
      groupFilter.value = 'admins';
      groupFilter.dispatchEvent(new Event('change', { bubbles: true }));

      expect(userState.groupFilter).toBe('admins');
    });

    it('should set up clear filters button handler', () => {
      userState.setSearchQuery('test');

      userHandlers.setupUserHandlers();

      const clearBtn = document.getElementById('clear-filters-btn');
      clearBtn?.click();

      expect(userState.searchQuery).toBe('');
    });

    it('should populate group filter dropdown', () => {
      userHandlers.setupUserHandlers();

      const groupFilter = document.getElementById('user-group-filter') as HTMLSelectElement;
      expect(groupFilter.options.length).toBe(3); // All Groups + 2 groups
    });

    it('should handle missing elements gracefully', () => {
      document.body.innerHTML = '';
      expect(() => userHandlers.setupUserHandlers()).not.toThrow();
    });
  });

  describe('bulk action handlers', () => {
    beforeEach(() => {
      (api.deleteUser as jest.Mock).mockResolvedValue({});
      (api.updateUser as jest.Mock).mockResolvedValue({});
      (api.listUsers as jest.Mock).mockResolvedValue({ users: [] });
      (api.listGroups as jest.Mock).mockResolvedValue({ groups: mockGroups });
      (global.confirm as jest.Mock).mockReturnValue(true);
      (global.prompt as jest.Mock) = jest.fn();
    });

    it('should set up bulk delete handler', async () => {
      userState.addSelectedUserId('1');

      userHandlers.setupUserHandlers();

      const bulkDeleteBtn = document.getElementById('bulk-delete-btn');
      bulkDeleteBtn?.click();

      // Wait for async operation
      await new Promise(resolve => setTimeout(resolve, 0));

      expect(api.deleteUser).toHaveBeenCalled();
    });

    it('bulk-role-btn is intentionally not wired (PR #912: role concept dropped)', async () => {
      // bulkChangeRole was removed. The handler does not wire bulk-role-btn
      // anymore. Clicking it must be a no-op rather than throwing.
      userState.addSelectedUserId('1');
      userHandlers.setupUserHandlers();
      const bulkRoleBtn = document.getElementById('bulk-role-btn');
      if (bulkRoleBtn) bulkRoleBtn.click();
      await new Promise(resolve => setTimeout(resolve, 0));
      expect(api.updateUser).not.toHaveBeenCalled();
    });

    it('never falls back to a native prompt() for bulk add to group (#705)', async () => {
      userState.addSelectedUserId('1');

      userHandlers.setupUserHandlers();

      // A stray legacy #bulk-group-btn must stay inert: the dropdown is the
      // only bulk add-to-group path.
      document.getElementById('bulk-group-btn')?.click();

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(global.prompt).not.toHaveBeenCalled();
      expect(api.updateUser).not.toHaveBeenCalled();
    });
  });
});
