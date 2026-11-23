/**
 * Tests for users module
 * Comprehensive tests for 80%+ coverage
 */

// The harness registers the module mocks, so it must be imported first.
import { mockConfirmDialog } from './users-harness';

import * as api from '../api';
import * as userState from '../users/state';
import * as userList from '../users/userList';
import * as userActions from '../users/userActions';
import * as userModals from '../users/userModals';

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
