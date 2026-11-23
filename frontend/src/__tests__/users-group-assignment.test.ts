/**
 * Tests for users group assignment UX
 */

// The harness registers the module mocks, so it must be imported first.
import './users-harness';

import * as api from '../api';
import * as userState from '../users/state';
import * as userList from '../users/userList';

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
