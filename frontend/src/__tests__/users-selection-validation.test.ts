/**
 * Tests for users group validation and selection UX
 */

// The harness registers the module mocks, so it must be imported first.
import './users-harness';

import * as userUtils from '../users/utils';

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
