/**
 * Tests for users module
 * Comprehensive tests for 80%+ coverage
 */

// The harness registers the module mocks, so it must be imported first.
import { mockConfirmDialog } from './users-harness';

import * as api from '../api';
import * as userUtils from '../users/utils';
import * as userState from '../users/state';
import * as userFilters from '../users/filters';
import * as userList from '../users/userList';
import * as userActions from '../users/userActions';
import * as userModals from '../users/userModals';
import * as userHandlers from '../users/handlers';

// ============================================================================
// USER MODALS TESTS
// ============================================================================
describe('users/userModals', () => {
  const mockUser = {
    id: '1',
    email: 'test@test.com',
    // role removed: PR #912 -- authorization is group-membership based.
    groups: ['users'],
    mfa_enabled: false
  };

  const mockGroups = [
    { id: 'admins', name: 'Admins', permissions: [], description: '' },
    { id: 'users', name: 'Users', permissions: [], description: '' }
  ];

  beforeEach(() => {
    document.body.innerHTML = `
      <div id="user-modal" class="hidden">
        <h2 id="user-modal-title">Create user</h2>
        <form id="user-form">
          <input type="hidden" id="user-id" />
          <input type="email" id="user-email" />
          <div id="password-fields">
            <input type="password" id="user-password" />
          </div>
          <!-- role selector removed in PR #912 -->
          <select id="user-groups" multiple></select>
          <button type="submit">Save</button>
        </form>
      </div>
      <div id="users-list"></div>
      <div id="groups-list"></div>
      <div id="user-stats"></div>
    `;

    userState.setAvailableGroups(mockGroups as any);
    userState.setCurrentEditingUser(null);
    userState.setAllUsers([mockUser] as any);
    userState.setFilteredUsers([mockUser] as any);

    (api.getUser as jest.Mock).mockResolvedValue(mockUser);
    (api.createUser as jest.Mock).mockResolvedValue({});
    (api.updateUser as jest.Mock).mockResolvedValue({});
    (api.listUsers as jest.Mock).mockResolvedValue({ users: [mockUser] });
    (api.listGroups as jest.Mock).mockResolvedValue({ groups: mockGroups });

    jest.clearAllMocks();
  });

  describe('saveUser', () => {
    it('should show success message on create', async () => {
      jest.useFakeTimers();
      userModals.openCreateUserModal();

      (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = 'SecurePass123!';

      const gs4 = document.getElementById('user-groups') as HTMLSelectElement;
      if (gs4.options.length > 0) gs4.options[0]!.selected = true;

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(document.querySelector('.toast-success')).toBeTruthy();
      jest.useRealTimers();
    });

    it('should show success message on update', async () => {
      jest.useFakeTimers();
      await userModals.openEditUserModal('1');

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(document.querySelector('.toast-success')).toBeTruthy();
      jest.useRealTimers();
    });

    it('should handle save error', async () => {
      (api.createUser as jest.Mock).mockRejectedValue(new Error('Create failed'));

      userModals.openCreateUserModal();

      (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = 'SecurePass123!';

      // PR #912: select a group so the required-group check passes and
      // the error comes from the API call, not the validation.
      const gs6 = document.getElementById('user-groups') as HTMLSelectElement;
      if (gs6.options.length > 0) gs6.options[0]!.selected = true;

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(document.querySelector('.toast-error')).toBeTruthy();
      expect(document.querySelector('.toast-error')?.textContent).toContain('Create failed');
    });

    it('should include selected groups', async () => {
      jest.useFakeTimers();
      userModals.openCreateUserModal();

      (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = 'SecurePass123!';

      const groupsSelect = document.getElementById('user-groups') as HTMLSelectElement;
      if (groupsSelect.options[0]) {
        groupsSelect.options[0].selected = true; // Select 'admins'
      }

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(api.createUser).toHaveBeenCalledWith(
        expect.objectContaining({
          groups: ['admins']
        })
      );
      jest.useRealTimers();
    });
  });
});

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

// ============================================================================
// GROUP ASSIGNMENT UX TESTS (issue #998)
// ============================================================================
describe('group assignment UX (issue #998)', () => {
  const mockUsers = [
    { id: '1', email: 'alice@test.com', groups: ['g1'], mfa_enabled: false },
    { id: '2', email: 'bob@test.com', groups: [], mfa_enabled: false },
  ];
  const mockGroups = [
    { id: 'g1', name: 'Admins', permissions: [], description: '', allowed_accounts: [] },
    { id: 'g2', name: 'Editors', permissions: [], description: '', allowed_accounts: [] },
  ];

  function buildDom(): void {
    document.body.innerHTML = `
      <div id="users-list"></div>
      <div id="user-stats"></div>
      <div id="bulk-actions-bar" class="hidden">
        <span id="selected-count">0</span>
      </div>
    `;
  }

  beforeEach(() => {
    buildDom();
    userState.setAllUsers(mockUsers as any);
    userState.setFilteredUsers(mockUsers as any);
    userState.setAvailableGroups(mockGroups as any);
    userState.clearSelectedUserIds();
    (api.updateUser as jest.Mock).mockResolvedValue({});
    jest.clearAllMocks();
  });

  afterEach(() => {
    jest.useRealTimers();
  });

  // -----------------------------------------------------------------------
  // 1. Single toast per group update
  // -----------------------------------------------------------------------
  describe('single toast per group update', () => {
    it('emits exactly one success toast when a group checkbox is toggled once', async () => {
      jest.useFakeTimers();
      userList.renderUsers(mockUsers as any);

      // Expand user "1" so the panel + checkboxes are in the DOM.
      const expandBtn = document.querySelector<HTMLButtonElement>(
        '.user-expand-btn[data-user-id="1"]',
      );
      expect(expandBtn).toBeTruthy();
      expandBtn!.click();

      // Fire the change event on the group-assign checkbox.
      const cb = document.querySelector<HTMLInputElement>(
        '.group-assign-checkbox[data-user-id="1"][data-group-id="g2"]',
      );
      expect(cb).toBeTruthy();
      cb!.checked = true;
      cb!.dispatchEvent(new Event('change', { bubbles: true }));

      // Flush the async toggleUserGroup promise.
      await Promise.resolve();
      await Promise.resolve();

      const toasts = document.querySelectorAll('.toast-success');
      expect(toasts.length).toBe(1);
    });

    it('still emits exactly one toast even after multiple renderUsers calls (listener-stacking regression)', async () => {
      // Pre-fix: each renderUsers call stacked a new document-level listener,
      // so N renders produced N toasts for a single toggle.
      jest.useFakeTimers();

      // Simulate three renderUsers calls (as would happen after 3 prior group
      // toggles that called loadUsers -> renderUsers).
      userList.renderUsers(mockUsers as any);
      userList.renderUsers(mockUsers as any);
      userList.renderUsers(mockUsers as any);

      const expandBtn = document.querySelector<HTMLButtonElement>(
        '.user-expand-btn[data-user-id="1"]',
      );
      expandBtn!.click();

      const cb = document.querySelector<HTMLInputElement>(
        '.group-assign-checkbox[data-user-id="1"][data-group-id="g2"]',
      );
      expect(cb).toBeTruthy();
      cb!.checked = true;
      cb!.dispatchEvent(new Event('change', { bubbles: true }));

      await Promise.resolve();
      await Promise.resolve();

      const toasts = document.querySelectorAll('.toast-success');
      // Must be exactly 1, not 3 (one per stacked listener).
      expect(toasts.length).toBe(1);
    });
  });

  // -----------------------------------------------------------------------
  // 2. Expand panel stays open after a group checkbox toggle
  // -----------------------------------------------------------------------
  describe('expand panel stays open after checkbox toggle', () => {
    it('does not collapse the expand row after a group toggle', async () => {
      jest.useFakeTimers();
      userList.renderUsers(mockUsers as any);

      const expandBtn = document.querySelector<HTMLButtonElement>(
        '.user-expand-btn[data-user-id="1"]',
      );
      expandBtn!.click();

      // Verify it is open.
      const expandRow = document.querySelector<HTMLElement>(
        'tr.user-expand-row[data-user-id="1"]',
      );
      expect(expandRow?.classList.contains('hidden')).toBe(false);

      // Toggle a group checkbox.
      const cb = document.querySelector<HTMLInputElement>(
        '.group-assign-checkbox[data-user-id="1"][data-group-id="g2"]',
      );
      cb!.checked = true;
      cb!.dispatchEvent(new Event('change', { bubbles: true }));

      await Promise.resolve();
      await Promise.resolve();

      // The row must still be visible - the fix does NOT call renderUsers (which
      // would replace the whole table and reset all rows to hidden).
      expect(expandRow?.classList.contains('hidden')).toBe(false);
    });

    it('keeps the expand panel in the DOM after a successful toggle', async () => {
      jest.useFakeTimers();
      userList.renderUsers(mockUsers as any);

      const expandBtn = document.querySelector<HTMLButtonElement>(
        '.user-expand-btn[data-user-id="1"]',
      );
      expandBtn!.click();

      const panelBefore = document.querySelector('.user-expand-panel[data-user-id="1"]');
      expect(panelBefore).toBeTruthy();

      const cb = document.querySelector<HTMLInputElement>(
        '.group-assign-checkbox[data-user-id="1"][data-group-id="g2"]',
      );
      cb!.checked = true;
      cb!.dispatchEvent(new Event('change', { bubbles: true }));

      await Promise.resolve();
      await Promise.resolve();

      // The panel element must still exist (not destroyed by a table re-render).
      const panelAfter = document.querySelector('.user-expand-panel[data-user-id="1"]');
      expect(panelAfter).toBeTruthy();
    });
  });

  describe('ids that are not valid selector text (#136)', () => {
    it('reverts the checkbox on a failed save when the user id contains a quote', async () => {
      const hostile = [{ id: 'u"]x', email: 'eve@test.com', groups: ['g1'], mfa_enabled: false }];
      userState.setAllUsers(hostile as any);
      userState.setFilteredUsers(hostile as any);
      (api.updateUser as jest.Mock).mockRejectedValue(new Error('rejected'));
      userList.renderUsers(hostile as any);

      document.querySelector<HTMLButtonElement>('.user-expand-btn')!.click();
      const cb = document.querySelector<HTMLInputElement>(
        `.group-assign-checkbox[data-group-id="g2"]`,
      );
      expect(cb).toBeTruthy();
      cb!.checked = true;
      cb!.dispatchEvent(new Event('change', { bubbles: true }));
      await new Promise(resolve => setTimeout(resolve, 0));

      expect(api.updateUser).toHaveBeenCalled();
      expect(cb!.checked).toBe(false);
    });
  });

  // -----------------------------------------------------------------------
  // 3. Expand button reacts reliably (single listener bound per render)
  // -----------------------------------------------------------------------
  describe('expand button listener bound once', () => {
    it('expand button opens the panel on the first click after renderUsers', () => {
      userList.renderUsers(mockUsers as any);

      const expandBtn = document.querySelector<HTMLButtonElement>(
        '.user-expand-btn[data-user-id="1"]',
      );
      expect(expandBtn).toBeTruthy();

      // First click must open the panel.
      expandBtn!.click();

      const expandRow = document.querySelector<HTMLElement>(
        'tr.user-expand-row[data-user-id="1"]',
      );
      expect(expandRow?.classList.contains('hidden')).toBe(false);
      expect(expandBtn!.getAttribute('aria-expanded')).toBe('true');
    });

    it('expand button toggles correctly after multiple renderUsers calls', () => {
      // Each renderUsers replaces the table innerHTML, so each new expand
      // button element gets exactly one click listener.
      userList.renderUsers(mockUsers as any);
      userList.renderUsers(mockUsers as any);
      userList.renderUsers(mockUsers as any);

      const expandBtn = document.querySelector<HTMLButtonElement>(
        '.user-expand-btn[data-user-id="2"]',
      );
      expandBtn!.click();

      const expandRow = document.querySelector<HTMLElement>(
        'tr.user-expand-row[data-user-id="2"]',
      );
      expect(expandRow?.classList.contains('hidden')).toBe(false);

      // Second click must collapse.
      expandBtn!.click();
      expect(expandRow?.classList.contains('hidden')).toBe(true);
    });
  });

  // -----------------------------------------------------------------------
  // 4. In-memory state is patched after toggle (no full round-trip)
  // -----------------------------------------------------------------------
  describe('in-memory state updated after toggle', () => {
    it('patches allUsers so the toggled group appears in user.groups', async () => {
      jest.useFakeTimers();
      userList.renderUsers(mockUsers as any);

      const expandBtn = document.querySelector<HTMLButtonElement>(
        '.user-expand-btn[data-user-id="2"]',
      );
      expandBtn!.click();

      // Bob starts with no groups; add g1.
      const cb = document.querySelector<HTMLInputElement>(
        '.group-assign-checkbox[data-user-id="2"][data-group-id="g1"]',
      );
      cb!.checked = true;
      cb!.dispatchEvent(new Event('change', { bubbles: true }));

      await Promise.resolve();
      await Promise.resolve();

      const updated = userState.allUsers.find(u => u.id === '2');
      expect(updated?.groups).toContain('g1');
    });

    it('patches allUsers so the removed group disappears from user.groups', async () => {
      jest.useFakeTimers();
      userList.renderUsers(mockUsers as any);

      const expandBtn = document.querySelector<HTMLButtonElement>(
        '.user-expand-btn[data-user-id="1"]',
      );
      expandBtn!.click();

      // Alice starts with ['g1']; remove g1.
      const cb = document.querySelector<HTMLInputElement>(
        '.group-assign-checkbox[data-user-id="1"][data-group-id="g1"]',
      );
      cb!.checked = false;
      cb!.dispatchEvent(new Event('change', { bubbles: true }));

      await Promise.resolve();
      await Promise.resolve();

      const updated = userState.allUsers.find(u => u.id === '1');
      expect(updated?.groups).not.toContain('g1');
    });
  });
});

// ============================================================================
// GROUP UNION HINT (issue #1001)
// ============================================================================
describe('group union permission hint (issue #1001)', () => {
  const mockUsers = [
    { id: '1', email: 'alice@test.com', groups: ['g1'], mfa_enabled: false },
  ];
  const mockGroups = [
    { id: 'g1', name: 'Admins', permissions: [], description: '', allowed_accounts: [] },
    { id: 'g2', name: 'Viewers', permissions: [], description: '', allowed_accounts: [] },
  ];

  beforeEach(() => {
    document.body.innerHTML = `
      <div id="users-list"></div>
      <div id="user-stats"></div>
      <div id="bulk-actions-bar" class="hidden">
        <span id="selected-count">0</span>
      </div>
    `;
    userState.setAllUsers(mockUsers as any);
    userState.setFilteredUsers(mockUsers as any);
    userState.setAvailableGroups(mockGroups as any);
    userState.clearSelectedUserIds();
    jest.clearAllMocks();
  });

  it('renders the union hint in the group membership expand panel', () => {
    userList.renderUsers(mockUsers as any);

    const expandBtn = document.querySelector<HTMLButtonElement>(
      '.user-expand-btn[data-user-id="1"]',
    );
    expect(expandBtn).toBeTruthy();
    expandBtn!.click();

    const panel = document.querySelector('.user-expand-panel[data-user-id="1"]');
    expect(panel).toBeTruthy();
    expect(panel!.textContent).toContain('combined (union) of all selected groups');
  });

  it('effective permissions use readable labels with the raw string as title', () => {
    userState.setAvailableGroups([
      { id: 'g-admin', name: 'Admins', description: '', permissions: [{ action: 'admin', resource: '*' }, { action: 'create', resource: 'plans' }] },
    ] as any);
    const user = { ...(mockUsers as any[])[0], groups: ['g-admin'] };
    userState.setAllUsers([user] as any);
    userList.renderUsers([user] as any);
    document.querySelector<HTMLButtonElement>('.user-expand-btn[data-user-id="1"]')!.click();

    const badges = Array.from(document.querySelectorAll('.perm-badge-list .permission-badge'));
    expect(badges.map(b => b.textContent)).toEqual(['Full access', 'Create plans']);
    expect(badges[0]!.getAttribute('title')).toContain('admin:*: full access to all resources');
    expect(badges[1]!.getAttribute('title')).toBe('create:plans');
  });

  it('hint appears in expand-panel-groups section, before the checkbox list', () => {
    userList.renderUsers(mockUsers as any);

    const expandBtn = document.querySelector<HTMLButtonElement>(
      '.user-expand-btn[data-user-id="1"]',
    );
    expandBtn!.click();

    const groupsSection = document.querySelector('.expand-panel-groups');
    expect(groupsSection).toBeTruthy();
    expect(groupsSection!.textContent).toContain('combined (union) of all selected groups');
  });
});

// ============================================================================
// VALIDATE GROUP COMBINATION UNIT TESTS (issue #1405)
// ============================================================================
describe('validateGroupCombination (issue #1405)', () => {
  const viewOnly = {
    id: 'v1', name: 'Viewers',
    permissions: [{ action: 'view', resource: 'aws' }],
    description: '', allowed_accounts: [],
  };
  const writeCapable = {
    id: 'w1', name: 'Editors',
    permissions: [{ action: 'create', resource: 'aws' }],
    description: '', allowed_accounts: [],
  };
  const mixedCapable = {
    id: 'm1', name: 'Mixed',
    permissions: [{ action: 'view', resource: 'a' }, { action: 'create', resource: 'b' }],
    description: '', allowed_accounts: [],
  };
  const emptyPerms = {
    id: 'e1', name: 'Empty',
    permissions: [],
    description: '', allowed_accounts: [],
  };
  const allGroups = [viewOnly, writeCapable, mixedCapable, emptyPerms];

  it('returns null for empty selection', () => {
    expect(userUtils.validateGroupCombination([], allGroups as any)).toBeNull();
  });

  it('returns null for a single view-only group', () => {
    expect(userUtils.validateGroupCombination(['v1'], allGroups as any)).toBeNull();
  });

  it('returns null for a single write-capable group', () => {
    expect(userUtils.validateGroupCombination(['w1'], allGroups as any)).toBeNull();
  });

  it('returns null for an empty-permission group alone', () => {
    expect(userUtils.validateGroupCombination(['e1'], allGroups as any)).toBeNull();
  });

  it('returns null for view-only + empty-permission group (empty is neutral)', () => {
    expect(userUtils.validateGroupCombination(['v1', 'e1'], allGroups as any)).toBeNull();
  });

  it('returns null for two write-capable groups', () => {
    const w2 = {
      id: 'w2', name: 'Admins',
      permissions: [{ action: 'delete', resource: 'aws' }],
      description: '', allowed_accounts: [],
    };
    expect(userUtils.validateGroupCombination(['w1', 'w2'], [...allGroups, w2] as any)).toBeNull();
  });

  it('returns null for an unknown group ID (not in allGroups)', () => {
    expect(userUtils.validateGroupCombination(['unknown-uuid'], allGroups as any)).toBeNull();
  });

  it('returns an error string for view-only + write-capable combination', () => {
    const result = userUtils.validateGroupCombination(['v1', 'w1'], allGroups as any);
    expect(result).not.toBeNull();
    expect(result).toContain('Contradictory');
    expect(result).toContain('"Viewers"');
    expect(result).toContain('"Editors"');
  });

  it('uses "is" (singular) for one view-only group in the error message', () => {
    const result = userUtils.validateGroupCombination(['v1', 'w1'], allGroups as any);
    expect(result).toContain(' is view-only');
  });

  it('uses "are" (plural) for multiple view-only groups in the error message', () => {
    const v2 = {
      id: 'v2', name: 'Readers',
      permissions: [{ action: 'view', resource: 'gcp' }],
      description: '', allowed_accounts: [],
    };
    const result = userUtils.validateGroupCombination(['v1', 'v2', 'w1'], [...allGroups, v2] as any);
    expect(result).toContain(' are view-only');
  });

  it('uses "grants" (singular) for one write-capable group in the error message', () => {
    const result = userUtils.validateGroupCombination(['v1', 'w1'], allGroups as any);
    expect(result).toContain('grants write');
  });

  it('uses "grant" (plural) for multiple write-capable groups in the error message', () => {
    const w2 = {
      id: 'w2', name: 'Admins',
      permissions: [{ action: 'delete', resource: 'aws' }],
      description: '', allowed_accounts: [],
    };
    const result = userUtils.validateGroupCombination(['v1', 'w1', 'w2'], [...allGroups, w2] as any);
    expect(result).toContain('grant write');
  });

  it('treats a mixed group (has at least one non-view action) as write-capable', () => {
    // m1 has both view and create permissions -- must trigger a conflict when
    // combined with a pure view-only group.
    const result = userUtils.validateGroupCombination(['v1', 'm1'], allGroups as any);
    expect(result).not.toBeNull();
    expect(result).toContain('"Mixed"');
  });
});

// ============================================================================
// USER-SELECTION CHECKBOX UX (issue #1404)
// Panel must not collapse when a user-selection checkbox is toggled.
// ============================================================================
describe('user-selection checkbox UX (issue #1404)', () => {
  const mockUsers = [
    { id: '1', email: 'alice@test.com', groups: ['g1'], mfa_enabled: false },
    { id: '2', email: 'bob@test.com', groups: [], mfa_enabled: false },
  ];
  const mockGroups = [
    { id: 'g1', name: 'Admins', permissions: [], description: '', allowed_accounts: [] },
  ];

  function buildDom(): void {
    document.body.innerHTML = `
      <div id="users-list"></div>
      <div id="user-stats"></div>
      <div id="bulk-actions-bar" class="hidden">
        <span id="selected-count">0</span>
      </div>
    `;
  }

  beforeEach(() => {
    buildDom();
    userState.setAllUsers(mockUsers as any);
    userState.setFilteredUsers(mockUsers as any);
    userState.setAvailableGroups(mockGroups as any);
    userState.clearSelectedUserIds();
    jest.clearAllMocks();
  });

  it('individual user checkbox does not collapse an open expand panel', () => {
    userList.renderUsers(mockUsers as any);

    // Expand user "1"
    const expandBtn = document.querySelector<HTMLButtonElement>(
      '.user-expand-btn[data-user-id="1"]',
    );
    expect(expandBtn).toBeTruthy();
    expandBtn!.click();

    const expandRow = document.querySelector<HTMLElement>(
      'tr.user-expand-row[data-user-id="1"]',
    );
    expect(expandRow?.classList.contains('hidden')).toBe(false);

    // Check user "2"'s selection checkbox (not a group-assign checkbox).
    // Pre-fix: this called renderUsers, which rebuilt the whole table and
    // collapsed user "1"'s expand row.
    const cb = document.querySelector<HTMLInputElement>(
      '.user-checkbox[data-user-id="2"]',
    );
    expect(cb).toBeTruthy();
    cb!.checked = true;
    cb!.dispatchEvent(new Event('change', { bubbles: true }));

    // User "1"'s panel must still be open after the checkbox event.
    expect(expandRow?.classList.contains('hidden')).toBe(false);
  });

  it('select-all checkbox does not collapse an open expand panel', () => {
    userList.renderUsers(mockUsers as any);

    // Expand user "2"
    const expandBtn = document.querySelector<HTMLButtonElement>(
      '.user-expand-btn[data-user-id="2"]',
    );
    expect(expandBtn).toBeTruthy();
    expandBtn!.click();

    const expandRow = document.querySelector<HTMLElement>(
      'tr.user-expand-row[data-user-id="2"]',
    );
    expect(expandRow?.classList.contains('hidden')).toBe(false);

    // Check select-all. Pre-fix: this called renderUsers, collapsing all panels.
    const selectAll = document.getElementById('select-all-users') as HTMLInputElement;
    expect(selectAll).toBeTruthy();
    selectAll!.checked = true;
    selectAll!.dispatchEvent(new Event('change', { bubbles: true }));

    // User "2"'s expand panel must still be open.
    expect(expandRow?.classList.contains('hidden')).toBe(false);
  });

  it('individual checkbox adds row-selected class on the affected row only', () => {
    userList.renderUsers(mockUsers as any);

    const row1 = document.querySelector<HTMLElement>('tr.user-row[data-user-id="1"]');
    const row2 = document.querySelector<HTMLElement>('tr.user-row[data-user-id="2"]');
    expect(row1?.classList.contains('row-selected')).toBe(false);
    expect(row2?.classList.contains('row-selected')).toBe(false);

    const cb = document.querySelector<HTMLInputElement>('.user-checkbox[data-user-id="1"]');
    cb!.checked = true;
    cb!.dispatchEvent(new Event('change', { bubbles: true }));

    expect(row1?.classList.contains('row-selected')).toBe(true);
    expect(row2?.classList.contains('row-selected')).toBe(false);
  });

  it('individual checkbox shows the bulk-actions bar when a user is checked', () => {
    userList.renderUsers(mockUsers as any);

    const bar = document.getElementById('bulk-actions-bar');
    expect(bar?.classList.contains('hidden')).toBe(true);

    const cb = document.querySelector<HTMLInputElement>('.user-checkbox[data-user-id="1"]');
    cb!.checked = true;
    cb!.dispatchEvent(new Event('change', { bubbles: true }));

    expect(bar?.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('selected-count')?.textContent).toBe('1');
  });

  it('unchecking all individual checkboxes hides the bulk-actions bar again', () => {
    userList.renderUsers(mockUsers as any);

    const bar = document.getElementById('bulk-actions-bar');
    const cb1 = document.querySelector<HTMLInputElement>('.user-checkbox[data-user-id="1"]');
    cb1!.checked = true;
    cb1!.dispatchEvent(new Event('change', { bubbles: true }));
    expect(bar?.classList.contains('hidden')).toBe(false);

    cb1!.checked = false;
    cb1!.dispatchEvent(new Event('change', { bubbles: true }));
    expect(bar?.classList.contains('hidden')).toBe(true);
  });

  it('select-all shows the bulk-actions bar with correct count and marks all rows', () => {
    userList.renderUsers(mockUsers as any);

    const bar = document.getElementById('bulk-actions-bar');
    const selectAll = document.getElementById('select-all-users') as HTMLInputElement;
    selectAll!.checked = true;
    selectAll!.dispatchEvent(new Event('change', { bubbles: true }));

    expect(bar?.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('selected-count')?.textContent).toBe('2');
    expect(
      document.querySelector<HTMLElement>('tr.user-row[data-user-id="1"]')
        ?.classList.contains('row-selected'),
    ).toBe(true);
    expect(
      document.querySelector<HTMLElement>('tr.user-row[data-user-id="2"]')
        ?.classList.contains('row-selected'),
    ).toBe(true);
  });

  it('deselecting via select-all hides the bulk-actions bar and removes row-selected from all rows', () => {
    userList.renderUsers(mockUsers as any);

    const bar = document.getElementById('bulk-actions-bar');
    const selectAll = document.getElementById('select-all-users') as HTMLInputElement;

    // Select all first
    selectAll!.checked = true;
    selectAll!.dispatchEvent(new Event('change', { bubbles: true }));
    expect(bar?.classList.contains('hidden')).toBe(false);

    // Then deselect all
    selectAll!.checked = false;
    selectAll!.dispatchEvent(new Event('change', { bubbles: true }));
    expect(bar?.classList.contains('hidden')).toBe(true);
    expect(
      document.querySelector<HTMLElement>('tr.user-row[data-user-id="1"]')
        ?.classList.contains('row-selected'),
    ).toBe(false);
    expect(
      document.querySelector<HTMLElement>('tr.user-row[data-user-id="2"]')
        ?.classList.contains('row-selected'),
    ).toBe(false);
  });
});

// ============================================================================
// CONTRADICTORY GROUP COMBINATION VALIDATION (issue #1405)
// Validates that the expand panel and the modal both reject view-only +
// write-capable group combinations before hitting the API.
// ============================================================================
describe('contradictory group combination validation (issue #1405)', () => {
  const viewOnly = {
    id: 'v1', name: 'Viewers',
    permissions: [{ action: 'view', resource: 'aws' }],
    description: '', allowed_accounts: [],
  };
  const writeCapable = {
    id: 'w1', name: 'Editors',
    permissions: [{ action: 'create', resource: 'aws' }],
    description: '', allowed_accounts: [],
  };
  const mockGroups = [viewOnly, writeCapable];

  // -------------------------------------------------------------------------
  // Expand panel (inline group-assign checkboxes, issue #1405 + #998 surface)
  // -------------------------------------------------------------------------
  describe('expand panel group toggle', () => {
    // Alice starts with the view-only group.  Adding a write group should be
    // blocked before the API is called.
    const mockUsers = [
      { id: '1', email: 'alice@test.com', groups: ['v1'], mfa_enabled: false },
    ];

    function buildDom(): void {
      document.body.innerHTML = `
        <div id="users-list"></div>
        <div id="user-stats"></div>
        <div id="bulk-actions-bar" class="hidden">
          <span id="selected-count">0</span>
        </div>
      `;
    }

    beforeEach(() => {
      buildDom();
      userState.setAllUsers(mockUsers as any);
      userState.setFilteredUsers(mockUsers as any);
      userState.setAvailableGroups(mockGroups as any);
      userState.clearSelectedUserIds();
      (api.updateUser as jest.Mock).mockResolvedValue({});
      jest.clearAllMocks();
    });

    afterEach(() => {
      jest.useRealTimers();
    });

    it('shows an error toast and does not call the API when adding a write group to a view-only user', async () => {
      jest.useFakeTimers();
      userList.renderUsers(mockUsers as any);

      const expandBtn = document.querySelector<HTMLButtonElement>(
        '.user-expand-btn[data-user-id="1"]',
      );
      expect(expandBtn).toBeTruthy();
      expandBtn!.click();

      // Alice has ['v1'] (view-only). Toggling on 'w1' (write-capable)
      // produces a contradictory combination and must be rejected.
      const cb = document.querySelector<HTMLInputElement>(
        '.group-assign-checkbox[data-user-id="1"][data-group-id="w1"]',
      );
      expect(cb).toBeTruthy();
      cb!.checked = true;
      cb!.dispatchEvent(new Event('change', { bubbles: true }));

      await Promise.resolve();
      await Promise.resolve();

      expect(api.updateUser).not.toHaveBeenCalled();
      const toast = document.querySelector('.toast-error');
      expect(toast).toBeTruthy();
      expect(toast?.querySelector('.toast-message')?.textContent).toContain('Contradictory');
    });

    it('reverts the group-assign checkbox to its pre-toggle state on a contradictory combination', async () => {
      jest.useFakeTimers();
      userList.renderUsers(mockUsers as any);

      const expandBtn = document.querySelector<HTMLButtonElement>(
        '.user-expand-btn[data-user-id="1"]',
      );
      expandBtn!.click();

      const cb = document.querySelector<HTMLInputElement>(
        '.group-assign-checkbox[data-user-id="1"][data-group-id="w1"]',
      );
      cb!.checked = true;
      cb!.dispatchEvent(new Event('change', { bubbles: true }));

      await Promise.resolve();
      await Promise.resolve();

      // Checkbox must be reverted to unchecked (pre-toggle state).
      expect(cb!.checked).toBe(false);
    });

    it('calls the API when adding a write-capable group to a user with no groups (not contradictory)', async () => {
      const user = { id: '3', email: 'carol@test.com', groups: [], mfa_enabled: false };
      userState.setAllUsers([user] as any);
      userState.setFilteredUsers([user] as any);

      jest.useFakeTimers();
      userList.renderUsers([user] as any);

      const expandBtn = document.querySelector<HTMLButtonElement>(
        '.user-expand-btn[data-user-id="3"]',
      );
      expect(expandBtn).toBeTruthy();
      expandBtn!.click();

      const cb = document.querySelector<HTMLInputElement>(
        '.group-assign-checkbox[data-user-id="3"][data-group-id="w1"]',
      );
      expect(cb).toBeTruthy();
      cb!.checked = true;
      cb!.dispatchEvent(new Event('change', { bubbles: true }));

      await Promise.resolve();
      await Promise.resolve();

      expect(api.updateUser).toHaveBeenCalledWith('3', { groups: ['w1'] });
      expect(document.querySelector('.toast-error')).toBeFalsy();
    });
  });

  // -------------------------------------------------------------------------
  // Modal saveUser (create + edit paths, issue #1405)
  // -------------------------------------------------------------------------
  describe('modal saveUser', () => {
    beforeEach(() => {
      document.body.innerHTML = `
        <div id="user-modal" class="hidden">
          <h2 id="user-modal-title">Create user</h2>
          <form id="user-form">
            <input type="hidden" id="user-id" />
            <input type="email" id="user-email" value="test@test.com" />
            <div id="password-fields">
              <input type="password" id="user-password" value="" />
            </div>
            <select id="user-groups" multiple></select>
            <button type="submit">Save</button>
          </form>
        </div>
        <div id="users-list"></div>
        <div id="user-stats"></div>
      `;
      userState.setAvailableGroups(mockGroups as any);
      userState.setCurrentEditingUser(null);
      (api.createUser as jest.Mock).mockResolvedValue({});
      (api.updateUser as jest.Mock).mockResolvedValue({});
      (api.listUsers as jest.Mock).mockResolvedValue({ users: [] });
      (api.listGroups as jest.Mock).mockResolvedValue({ groups: mockGroups });
      jest.clearAllMocks();
    });

    it('shows error and does not call createUser when contradictory groups are selected on create', async () => {
      userModals.openCreateUserModal();

      // Select both v1 (view-only) and w1 (write-capable) -- contradictory.
      const gs = document.getElementById('user-groups') as HTMLSelectElement;
      Array.from(gs.options).forEach(opt => { opt.selected = true; });

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(api.createUser).not.toHaveBeenCalled();
      const toast = document.querySelector('.toast-error');
      expect(toast).toBeTruthy();
      expect(toast?.querySelector('.toast-message')?.textContent).toContain('Contradictory');
    });

    it('shows error and does not call updateUser when contradictory groups are selected on edit', async () => {
      const editUser = {
        id: '1', email: 'alice@test.com', groups: ['v1'], mfa_enabled: false,
      };
      (api.getUser as jest.Mock).mockResolvedValue(editUser);
      await userModals.openEditUserModal('1');

      // Select both options (v1 + w1) -- contradictory.
      const gs = document.getElementById('user-groups') as HTMLSelectElement;
      Array.from(gs.options).forEach(opt => { opt.selected = true; });

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(api.updateUser).not.toHaveBeenCalled();
      const toast = document.querySelector('.toast-error');
      expect(toast).toBeTruthy();
      expect(toast?.querySelector('.toast-message')?.textContent).toContain('Contradictory');
    });

    it('calls createUser when only a single write-capable group is selected (not contradictory)', async () => {
      userModals.openCreateUserModal();
      (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = 'SecurePass123!';

      // Select only w1 (write-capable) -- no view-only group, so no conflict.
      const gs = document.getElementById('user-groups') as HTMLSelectElement;
      Array.from(gs.options).forEach(opt => { opt.selected = opt.value === 'w1'; });

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(api.createUser).toHaveBeenCalledWith(
        expect.objectContaining({ groups: ['w1'] }),
      );
      expect(document.querySelector('.toast-error')).toBeFalsy();
    });
  });
});

describe('synthetic current-user row (issue #274)', () => {
  const real = { id: '1', email: 'a@test.com', groups: [], mfa_enabled: true };
  const synthetic = { id: 'current', email: 'me@test.com', groups: [], mfa_enabled: false };

  beforeEach(() => {
    document.body.innerHTML = '<div id="users-list"></div><div id="user-stats"></div>';
    userState.setAllUsers([synthetic, real] as any);
    userState.setFilteredUsers([synthetic, real] as any);
    userState.clearSelectedUserIds();
  });

  it('disables select, Edit and Delete on the synthetic row only', () => {
    userList.renderUsers([synthetic, real] as any);
    const row = (id: string) => document.querySelector(`tr.user-row[data-user-id="${id}"]`)!;
    expect(row('current').querySelector<HTMLInputElement>('.user-checkbox')!.disabled).toBe(true);
    expect(row('current').querySelector<HTMLButtonElement>('.edit-user-btn')!.disabled).toBe(true);
    expect(row('current').querySelector<HTMLButtonElement>('.delete-user-btn')!.disabled).toBe(true);
    expect(row('1').querySelector<HTMLButtonElement>('.edit-user-btn')!.disabled).toBe(false);
  });

  it('select-all skips the synthetic row', () => {
    userList.renderUsers([synthetic, real] as any);
    const all = document.getElementById('select-all-users') as HTMLInputElement;
    all.checked = true;
    all.dispatchEvent(new Event('change'));
    expect([...userState.selectedUserIds]).toEqual(['1']);
  });

  it('carries the session user mfa_enabled into the synthetic row and the MFA stat', async () => {
    require('../state').setCurrentUser({ id: 'u9', email: 'me@test.com', groups: [], mfa_enabled: true });
    (api.listUsers as jest.Mock).mockResolvedValue({ users: [{ ...real, mfa_enabled: false }] });
    (api.listGroups as jest.Mock).mockResolvedValue({ groups: [] });
    await userActions.loadUsers();
    const row = document.querySelector('tr.user-row[data-user-id="current"]')!;
    expect(row.textContent).toContain('Enabled');
    const card = [...document.querySelectorAll('.stat-card')].find(c => c.textContent!.includes('MFA Enabled'))!;
    expect(card.querySelector('.stat-value')!.textContent).toBe('1');
  });
});

describe('users table checkbox names (issue #746)', () => {
  it('labels select-all and each row checkbox with the user email', () => {
    document.body.innerHTML = '<div id="users-list"></div>';
    const users = [
      { id: 'u1', email: 'a@test.com', groups: [], mfa_enabled: false },
      { id: 'u2', email: 'b@test.com', groups: [], mfa_enabled: false },
    ];
    userList.renderUsers(users as any);
    expect(document.getElementById('select-all-users')!.getAttribute('aria-label')).toBe('Select all users');
    const labels = [...document.querySelectorAll('.user-checkbox')].map(c => c.getAttribute('aria-label'));
    expect(labels).toEqual(['Select a@test.com', 'Select b@test.com']);
  });
});

describe('group toggle confirmation (issue #736)', () => {
  const ADMIN = '00000000-0000-5000-8000-000000000001';
  const users = [
    { id: '1', email: 'me@test.com', groups: [ADMIN], mfa_enabled: false },
    { id: '2', email: 'bob@test.com', groups: [ADMIN], mfa_enabled: false },
  ];
  const groups = [{ id: ADMIN, name: 'Administrators', permissions: [], description: '', allowed_accounts: [] }];

  const toggle = async (userId: string, checked: boolean) => {
    document.querySelector<HTMLButtonElement>(`.user-expand-btn[data-user-id="${userId}"]`)!.click();
    const cb = document.querySelector<HTMLInputElement>(`.group-assign-checkbox[data-user-id="${userId}"]`)!;
    cb.checked = checked;
    cb.dispatchEvent(new Event('change', { bubbles: true }));
    for (let i = 0; i < 5; i++) await Promise.resolve();
    return cb;
  };

  beforeEach(() => {
    document.body.innerHTML = '<div id="users-list"></div>';
    userState.setAllUsers(users as any);
    userState.setFilteredUsers(users as any);
    userState.setAvailableGroups(groups as any);
    (api.updateUser as jest.Mock).mockReset().mockResolvedValue({});
    mockConfirmDialog.mockReset().mockResolvedValue(true);
    require('../state').setCurrentUser({ id: '1', email: 'me@test.com', groups: [ADMIN] });
    userList.renderUsers(users as any);
  });

  it('asks before changing membership and skips the API when declined', async () => {
    mockConfirmDialog.mockResolvedValueOnce(false);
    const cb = await toggle('2', false);
    expect(mockConfirmDialog).toHaveBeenCalledTimes(1);
    expect(api.updateUser).not.toHaveBeenCalled();
    expect(cb.checked).toBe(true);
  });

  it('applies the change once confirmed', async () => {
    await toggle('2', false);
    expect(api.updateUser).toHaveBeenCalledWith('2', { groups: [] });
  });

  it('warns when the admin removes their own Administrators membership', async () => {
    await toggle('1', false);
    expect(mockConfirmDialog.mock.calls[0]![0]).toMatchObject({
      body: expect.stringContaining('removing yourself'),
      destructive: true,
    });
  });

  it('detects self-demotion by user id, not email', async () => {
    require('../state').setCurrentUser({ id: '1', email: 'ME@test.com', groups: [ADMIN] });
    await toggle('1', false);
    expect(mockConfirmDialog.mock.calls[0]![0]).toMatchObject({ body: expect.stringContaining('removing yourself') });
  });

  it('saves against the groups current after the confirm resolves', async () => {
    let release!: (v: boolean) => void;
    mockConfirmDialog.mockReturnValueOnce(new Promise<boolean>((r) => { release = r; }));
    document.querySelector<HTMLButtonElement>('.user-expand-btn[data-user-id="2"]')!.click();
    const cb = document.querySelector<HTMLInputElement>('.group-assign-checkbox[data-user-id="2"]')!;
    cb.checked = false;
    cb.dispatchEvent(new Event('change', { bubbles: true }));
    userState.setAllUsers(users.map(u => (u.id === '2' ? { ...u, groups: [ADMIN, 'g-new'] } : u)) as any);
    release(true);
    for (let i = 0; i < 5; i++) await Promise.resolve();
    expect(api.updateUser).toHaveBeenCalledWith('2', { groups: ['g-new'] });
  });
});

describe('group toggle re-validation after confirm (issue #736)', () => {
  const viewOnly = { id: 'v1', name: 'Viewers', permissions: [{ action: 'view', resource: 'aws' }], description: '', allowed_accounts: [] };
  const writer = { id: 'w1', name: 'Editors', permissions: [{ action: 'create', resource: 'aws' }], description: '', allowed_accounts: [] };
  const user = { id: '2', email: 'bob@test.com', groups: [], mfa_enabled: false };

  it('refuses to save when a concurrent change makes the combination contradictory', async () => {
    document.body.innerHTML = '<div id="users-list"></div>';
    userState.setAllUsers([user] as any);
    userState.setFilteredUsers([user] as any);
    userState.setAvailableGroups([viewOnly, writer] as any);
    (api.updateUser as jest.Mock).mockReset().mockResolvedValue({});
    let release!: (v: boolean) => void;
    mockConfirmDialog.mockReset().mockReturnValueOnce(new Promise<boolean>((r) => { release = r; }));
    userList.renderUsers([user] as any);
    document.querySelector<HTMLButtonElement>('.user-expand-btn[data-user-id="2"]')!.click();
    const cb = document.querySelector<HTMLInputElement>('.group-assign-checkbox[data-user-id="2"][data-group-id="w1"]')!;
    cb.checked = true;
    cb.dispatchEvent(new Event('change', { bubbles: true }));
    userState.setAllUsers([{ ...user, groups: ['v1'] }] as any);
    release(true);
    for (let i = 0; i < 5; i++) await Promise.resolve();
    expect(api.updateUser).not.toHaveBeenCalled();
    expect(cb.checked).toBe(false);
    expect(document.querySelector('.toast-error .toast-message')?.textContent).toContain('Contradictory');
  });
});

describe('select-all with only the synthetic current-user row (issue #274)', () => {
  it('disables the select-all checkbox when no row is selectable', () => {
    document.body.innerHTML = '<div id="users-list"></div>';
    userList.renderUsers([{ id: 'current', email: 'me@test.com', groups: [], mfa_enabled: false }] as any);
    expect((document.getElementById('select-all-users') as HTMLInputElement).disabled).toBe(true);
  });
});

describe('saveUser submit button (issue #735)', () => {
  it('disables Save User while the request is in flight and re-enables it after', async () => {
    document.body.innerHTML = `
      <div id="user-modal" class="hidden">
        <h2 id="user-modal-title"></h2>
        <form id="user-form">
          <input type="hidden" id="user-id" />
          <input type="email" id="user-email" />
          <div id="password-fields"><input type="password" id="user-password" /></div>
          <select id="user-groups" multiple></select>
          <button type="submit">Save</button>
        </form>
      </div>
      <div id="users-list"></div><div id="groups-list"></div><div id="user-stats"></div>`;
    userState.setAvailableGroups([{ id: 'users', name: 'Users', permissions: [], description: '' }] as any);
    userState.setCurrentEditingUser(null);
    userModals.openCreateUserModal();
    const btn = document.querySelector<HTMLButtonElement>('#user-form button[type="submit"]')!;
    (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
    (document.getElementById('user-password') as HTMLInputElement).value = 'SecurePass123!';
    const gs = document.getElementById('user-groups') as HTMLSelectElement;
    if (gs.options.length > 0) gs.options[0]!.selected = true;
    let resolve!: (v: unknown) => void;
    (api.createUser as jest.Mock).mockReturnValueOnce(new Promise((r) => { resolve = r; }));
    (api.listUsers as jest.Mock).mockResolvedValue({ users: [] });
    (api.listGroups as jest.Mock).mockResolvedValue({ groups: [] });

    const event = new Event('submit');
    event.preventDefault = jest.fn();
    const pending = userModals.saveUser(event);
    expect(btn.disabled).toBe(true);
    resolve({});
    await pending;
    expect(btn.disabled).toBe(false);
  });
});
