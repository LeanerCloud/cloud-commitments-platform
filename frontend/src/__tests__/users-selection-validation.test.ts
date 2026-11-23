/**
 * Tests for users group validation and selection UX
 */

// The harness registers the module mocks, so it must be imported first.
import './users-harness';

import * as userUtils from '../users/utils';
import * as userState from '../users/state';
import * as userList from '../users/userList';

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
