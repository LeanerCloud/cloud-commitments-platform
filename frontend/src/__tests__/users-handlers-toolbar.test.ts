/**
 * Tests for users handlers, integration and bulk-actions toolbar
 */

// The harness registers the module mocks, so it must be imported first.
import './users-harness';

import * as api from '../api';
import * as userState from '../users/state';
import * as userFilters from '../users/filters';
import * as userList from '../users/userList';
import * as userActions from '../users/userActions';
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

// ============================================================================
// INTEGRATION TESTS
// ============================================================================
describe('users module integration', () => {
  const mockUsers = [
    { id: '1', email: 'admin@test.com', groups: ['00000000-0000-5000-8000-000000000001'], mfa_enabled: true, created_at: '2024-01-01' },
    { id: '2', email: 'user@test.com', groups: [], mfa_enabled: false, created_at: '2024-01-02' }
  ];

  const mockGroups = [
    { id: 'admins', name: 'Admins', permissions: [], description: '' }
  ];

  beforeEach(() => {
    document.body.innerHTML = `
      <div id="users-list"></div>
      <div id="groups-list"></div>
      <div id="user-stats"></div>
      <input type="text" id="user-search" />
      <select id="user-mfa-filter"><option value="">All</option></select>
      <select id="user-group-filter"><option value="">All</option></select>
      <div id="bulk-actions-bar" class="hidden">
        <span id="selected-count">0</span>
      </div>
    `;

    (api.listUsers as jest.Mock).mockResolvedValue({ users: mockUsers });
    (api.listGroups as jest.Mock).mockResolvedValue({ groups: mockGroups });

    userState.setAllUsers([]);
    userState.setFilteredUsers([]);
    userState.setAvailableGroups([]);
    userState.clearSelectedUserIds();
    userState.setSearchQuery('');
    userState.setMfaFilter('');
    userState.setGroupFilter('');

    jest.clearAllMocks();
  });

  it('should load users and render them', async () => {
    await userActions.loadUsers();

    const container = document.getElementById('users-list');
    expect(container?.innerHTML).toContain('admin@test.com');
    expect(container?.innerHTML).toContain('user@test.com');
  });

  it('should filter and re-render users', async () => {
    await userActions.loadUsers();

    userFilters.handleUserSearch('admin');

    const container = document.getElementById('users-list');
    expect(container?.innerHTML).toContain('admin@test.com');
    expect(container?.innerHTML).not.toContain('user@test.com');
  });

  it('should update stats when filtering', async () => {
    await userActions.loadUsers();

    const statsContainer = document.getElementById('user-stats');
    expect(statsContainer?.innerHTML).toContain('2'); // Total users

    userFilters.handleFilterChange('role', 'admin');

    expect(statsContainer?.innerHTML).toContain('1'); // Showing filtered
  });

  it('should track user selection state', async () => {
    await userActions.loadUsers();

    userState.addSelectedUserId('1');

    const container = document.getElementById('users-list');
    // Re-render to show selection
    userList.renderUsers(userState.filteredUsers);

    expect(container?.querySelector('.row-selected')).toBeTruthy();
  });

  it('should update bulk actions bar on selection', async () => {
    await userActions.loadUsers();

    userState.addSelectedUserId('1');
    userList.updateBulkActionsBar();

    const bar = document.getElementById('bulk-actions-bar');
    expect(bar?.classList.contains('hidden')).toBe(false);
  });
});

// ============================================================================
// BULK TOOLBAR TESTS (issue #974)
// ============================================================================
describe('users bulk-actions toolbar', () => {
  const mockGroups = [
    { id: 'admins', name: 'Admins', permissions: [], description: '', allowed_accounts: [] },
    { id: 'developers', name: 'Developers', permissions: [], description: '', allowed_accounts: [] },
  ];

  function buildToolbarDom(): void {
    document.body.innerHTML = `
      <form id="user-form"></form>
      <input type="text" id="user-search" />
      <select id="user-mfa-filter"><option value="">All</option></select>
      <select id="user-group-filter"><option value="">All Groups</option></select>
      <button id="clear-filters-btn">Clear</button>
      <div id="users-list"></div>
      <div id="user-stats"></div>
      <div id="bulk-actions-bar" class="hidden">
        <span id="selected-count">0</span>
        <button id="bulk-delete-btn">Delete selected</button>
        <select id="bulk-group-select">
          <option value="">Add to group...</option>
        </select>
      </div>
    `;
  }

  beforeEach(() => {
    buildToolbarDom();
    userState.setAllUsers([]);
    userState.setFilteredUsers([]);
    userState.setAvailableGroups(mockGroups as any);
    userState.clearSelectedUserIds();
    jest.clearAllMocks();
  });

  describe('toolbar visibility', () => {
    it('should be hidden when no users are selected', () => {
      userList.updateBulkActionsBar();

      const bar = document.getElementById('bulk-actions-bar');
      expect(bar?.classList.contains('hidden')).toBe(true);
    });

    it('should be visible with correct count when one user is selected', () => {
      userState.addSelectedUserId('user-1');

      userList.updateBulkActionsBar();

      const bar = document.getElementById('bulk-actions-bar');
      expect(bar?.classList.contains('hidden')).toBe(false);
      const count = document.getElementById('selected-count');
      expect(count?.textContent).toBe('1');
    });

    it('should be visible with correct count when multiple users are selected', () => {
      userState.addSelectedUserId('user-1');
      userState.addSelectedUserId('user-2');
      userState.addSelectedUserId('user-3');

      userList.updateBulkActionsBar();

      const bar = document.getElementById('bulk-actions-bar');
      expect(bar?.classList.contains('hidden')).toBe(false);
      const count = document.getElementById('selected-count');
      expect(count?.textContent).toBe('3');
    });

    it('should become hidden again after all rows are unchecked', () => {
      userState.addSelectedUserId('user-1');
      userList.updateBulkActionsBar();

      const barBefore = document.getElementById('bulk-actions-bar');
      expect(barBefore?.classList.contains('hidden')).toBe(false);

      userState.clearSelectedUserIds();
      userList.updateBulkActionsBar();

      const barAfter = document.getElementById('bulk-actions-bar');
      expect(barAfter?.classList.contains('hidden')).toBe(true);
    });
  });

  describe('bulk-delete-btn', () => {
    it('should call bulkDeleteUsers when clicked via setupUserHandlers', async () => {
      const mockUser = { id: '1', email: 'u@t.com', role: 'user', groups: [], mfa_enabled: false };
      userState.setAllUsers([mockUser] as any);
      userState.addSelectedUserId('1');

      (api.deleteUser as jest.Mock).mockResolvedValue({});
      (api.listUsers as jest.Mock).mockResolvedValue({ users: [] });
      (api.listGroups as jest.Mock).mockResolvedValue({ groups: [] });

      userHandlers.setupUserHandlers();

      const bulkDeleteBtn = document.getElementById('bulk-delete-btn');
      bulkDeleteBtn?.click();

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(api.deleteUser).toHaveBeenCalledWith('1');
    });
  });

  describe('bulk-group-select', () => {
    it('should populate with available groups', () => {
      userList.populateBulkGroupSelect();

      const select = document.getElementById('bulk-group-select') as HTMLSelectElement;
      // placeholder + 2 groups
      expect(select.options.length).toBe(3);
      expect(select.options[1]?.value).toBe('admins');
      expect(select.options[1]?.text).toBe('Admins');
      expect(select.options[2]?.value).toBe('developers');
      expect(select.options[2]?.text).toBe('Developers');
    });

    it('should call bulkAddToGroup with selected group id on change via setupUserHandlers', async () => {
      const mockUser = { id: '1', email: 'test@test.com', role: 'user', groups: [], mfa_enabled: false };
      userState.setAllUsers([mockUser] as any);
      userState.addSelectedUserId('1');

      (api.updateUser as jest.Mock).mockResolvedValue({});
      (api.listUsers as jest.Mock).mockResolvedValue({ users: [mockUser] });
      (api.listGroups as jest.Mock).mockResolvedValue({ groups: mockGroups });

      userHandlers.setupUserHandlers();

      const select = document.getElementById('bulk-group-select') as HTMLSelectElement;
      select.value = 'admins';
      select.dispatchEvent(new Event('change', { bubbles: true }));

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(api.updateUser).toHaveBeenCalledWith('1', { groups: ['admins'] });
    });

    it('should not call bulkAddToGroup when placeholder is selected', async () => {
      userState.addSelectedUserId('1');
      (api.updateUser as jest.Mock).mockResolvedValue({});

      userHandlers.setupUserHandlers();

      const select = document.getElementById('bulk-group-select') as HTMLSelectElement;
      select.value = '';
      select.dispatchEvent(new Event('change', { bubbles: true }));

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(api.updateUser).not.toHaveBeenCalled();
    });
  });
});
