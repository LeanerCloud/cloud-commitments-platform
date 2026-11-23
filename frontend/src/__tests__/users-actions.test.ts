/**
 * Tests for users actions
 */

// The harness registers the module mocks, so it must be imported first.
import { mockConfirmDialog } from './users-harness';

import * as api from '../api';
import { renderGroups } from '../groups/groupList';
import * as userState from '../users/state';
import * as userActions from '../users/userActions';

// ============================================================================
// USER ACTIONS TESTS
// ============================================================================
describe('users/userActions', () => {
  const mockUsers = [
    { id: '1', email: 'user1@test.com', groups: [], mfa_enabled: false },
    { id: '2', email: 'user2@test.com', groups: ['admins'], mfa_enabled: true }
  ];

  // `allowed_accounts: []` matches the backend Group shape after the
  // multi-cloud account-scoping work; renderGroups + state hydration
  // now expect this field on every group, so the fixture has to carry
  // it or deep-equal assertions diverge.
  const mockGroups = [
    { id: 'admins', name: 'Admins', permissions: [], description: '', allowed_accounts: [] },
    { id: 'developers', name: 'Developers', permissions: [], description: '', allowed_accounts: [] }
  ];

  beforeEach(() => {
    document.body.innerHTML = `
      <div id="users-list"></div>
      <div id="groups-list"></div>
      <div id="user-stats"></div>
      <div id="bulk-actions-bar" class="hidden">
        <span id="selected-count">0</span>
      </div>
    `;

    (api.listUsers as jest.Mock).mockResolvedValue({ users: mockUsers });
    (api.listGroups as jest.Mock).mockResolvedValue({ groups: mockGroups });
    (api.deleteUser as jest.Mock).mockResolvedValue({});
    (api.updateUser as jest.Mock).mockResolvedValue({});
    (api.createUser as jest.Mock).mockResolvedValue({});

    userState.setAllUsers([]);
    userState.setFilteredUsers([]);
    userState.setAvailableGroups([]);
    userState.clearSelectedUserIds();
    jest.clearAllMocks();
  });

  describe('loadUsers', () => {
    it('should load users and groups', async () => {
      await userActions.loadUsers();

      expect(api.listUsers).toHaveBeenCalled();
      expect(api.listGroups).toHaveBeenCalled();
      expect(userState.allUsers).toEqual(mockUsers);
      expect(userState.availableGroups).toEqual(mockGroups);
    });

    it('should render users after loading', async () => {
      await userActions.loadUsers();

      const container = document.getElementById('users-list');
      expect(container?.innerHTML).toContain('user1@test.com');
    });

    it('should render groups after loading', async () => {
      await userActions.loadUsers();

      expect(renderGroups).toHaveBeenCalledWith(mockGroups);
    });

    it('should render user stats after loading', async () => {
      await userActions.loadUsers();

      const statsContainer = document.getElementById('user-stats');
      expect(statsContainer?.innerHTML).toContain('Total Users');
    });

    it('should handle API error gracefully', async () => {
      (api.listUsers as jest.Mock).mockRejectedValue(new Error('Network error'));

      await userActions.loadUsers();

      // Should show error toast
      expect(document.querySelector('.toast-error')).toBeTruthy();
    });

    it('should handle groups API error gracefully', async () => {
      (api.listGroups as jest.Mock).mockRejectedValue(new Error('Groups error'));

      await userActions.loadUsers();

      expect(document.querySelector('.toast-error')).toBeTruthy();
    });
  });

  describe('deleteUser', () => {
    beforeEach(() => {
      userState.setAllUsers(mockUsers as any);
      (global.confirm as jest.Mock).mockReturnValue(true);
    });

    it('should delete user when confirmed', async () => {
      await userActions.deleteUser('1');

      expect(api.deleteUser).toHaveBeenCalledWith('1');
    });

    it('should show confirmation dialog', async () => {
      await userActions.deleteUser('1');

      expect(mockConfirmDialog).toHaveBeenCalledWith(
        expect.objectContaining({ title: expect.stringContaining('user1@test.com') })
      );
    });

    it('shows the server reason when the delete fails (issue #748)', async () => {
      (api.deleteUser as jest.Mock).mockRejectedValueOnce(new Error('cannot delete the last administrator'));
      await userActions.deleteUser('1');

      expect(document.querySelector('.toast-error')?.textContent).toContain('Failed to delete user: cannot delete the last administrator');
    });

    it('should reload users after deletion', async () => {
      await userActions.deleteUser('1');

      expect(api.listUsers).toHaveBeenCalled();
    });

    it('should show success message', async () => {
      jest.useFakeTimers();
      await userActions.deleteUser('1');

      expect(document.querySelector('.toast-success')).toBeTruthy();
      jest.useRealTimers();
    });

    it('should not delete when user not found', async () => {
      await userActions.deleteUser('nonexistent');

      expect(api.deleteUser).not.toHaveBeenCalled();
    });

    it('should not delete when cancelled', async () => {
      mockConfirmDialog.mockResolvedValueOnce(false);

      await userActions.deleteUser('1');

      expect(api.deleteUser).not.toHaveBeenCalled();
    });

    it('should handle delete error', async () => {
      (api.deleteUser as jest.Mock).mockRejectedValue(new Error('Delete failed'));

      await userActions.deleteUser('1');

      expect(document.querySelector('.toast-error')).toBeTruthy();
    });
  });

  describe('bulkAddToGroup', () => {
    beforeEach(() => {
      userState.setAllUsers(mockUsers as any);
      userState.setAvailableGroups(mockGroups as any);
      (global.confirm as jest.Mock).mockReturnValue(true);
    });

    it('should add selected users to group', async () => {
      userState.addSelectedUserId('1');

      await userActions.bulkAddToGroup('admins');

      expect(api.updateUser).toHaveBeenCalledWith('1', { groups: ['admins'] });
    });

    it('should not duplicate groups', async () => {
      userState.addSelectedUserId('2'); // Already in 'admins' group

      await userActions.bulkAddToGroup('admins');

      expect(api.updateUser).toHaveBeenCalledWith('2', { groups: ['admins'] });
    });

    it('should add to existing groups', async () => {
      userState.addSelectedUserId('2');

      await userActions.bulkAddToGroup('developers');

      expect(api.updateUser).toHaveBeenCalledWith('2', { groups: ['admins', 'developers'] });
    });

    it('should not add when no users selected', async () => {
      await userActions.bulkAddToGroup('admins');

      expect(api.updateUser).not.toHaveBeenCalled();
    });

    it('should not add when group not found', async () => {
      userState.addSelectedUserId('1');

      await userActions.bulkAddToGroup('nonexistent');

      expect(api.updateUser).not.toHaveBeenCalled();
    });

    it('should show confirmation with group name', async () => {
      userState.addSelectedUserId('1');

      await userActions.bulkAddToGroup('admins');

      expect(mockConfirmDialog).toHaveBeenCalledWith(
        expect.objectContaining({ title: expect.stringContaining('Admins') })
      );
    });

    it('should not add when cancelled', async () => {
      mockConfirmDialog.mockResolvedValueOnce(false);
      userState.addSelectedUserId('1');

      await userActions.bulkAddToGroup('admins');

      expect(api.updateUser).not.toHaveBeenCalled();
    });

    it('should clear selection after adding', async () => {
      userState.addSelectedUserId('1');

      await userActions.bulkAddToGroup('admins');

      expect(userState.selectedUserIds.size).toBe(0);
    });

    it('should show success message', async () => {
      jest.useFakeTimers();
      userState.addSelectedUserId('1');

      await userActions.bulkAddToGroup('admins');

      expect(document.querySelector('.toast-success')).toBeTruthy();
      jest.useRealTimers();
    });

    it('should handle update error', async () => {
      userState.addSelectedUserId('1');
      (api.updateUser as jest.Mock).mockRejectedValue(new Error('Update failed'));

      await userActions.bulkAddToGroup('admins');

      expect(document.querySelector('.toast-error')).toBeTruthy();
    });

    it('should skip users that are not found in allUsers', async () => {
      userState.addSelectedUserId('1');
      userState.addSelectedUserId('nonexistent');

      await userActions.bulkAddToGroup('admins');

      // Should only update user '1'
      expect(api.updateUser).toHaveBeenCalledTimes(1);
      expect(api.updateUser).toHaveBeenCalledWith('1', { groups: ['admins'] });
    });

    // -----------------------------------------------------------------------
    // Contradictory group combination guard on the bulk path (issue #1405).
    // Regression: before the fix, bulkAddToGroup wrote the contradictory
    // combination without validation. These assert the bulk path enforces
    // the same rule as the inline expand panel and the modal.
    // -----------------------------------------------------------------------
    describe('contradictory group combination guard (issue #1405)', () => {
      const permGroups = [
        {
          id: 'viewers', name: 'Viewers',
          permissions: [{ action: 'view', resource: 'aws' }],
          description: '', allowed_accounts: [],
        },
        {
          id: 'editors', name: 'Editors',
          permissions: [{ action: 'create', resource: 'aws' }],
          description: '', allowed_accounts: [],
        },
      ];
      const permUsers = [
        // Alice already holds the view-only group.
        { id: 'a', email: 'alice@test.com', groups: ['viewers'], mfa_enabled: false },
        // Bob has no groups yet.
        { id: 'b', email: 'bob@test.com', groups: [], mfa_enabled: false },
      ];

      beforeEach(() => {
        userState.setAllUsers(permUsers as any);
        userState.setAvailableGroups(permGroups as any);
        (global.confirm as jest.Mock).mockReturnValue(true);
        (api.listUsers as jest.Mock).mockResolvedValue({ users: permUsers });
        (api.listGroups as jest.Mock).mockResolvedValue({ groups: permGroups });
      });

      it('does not call updateUser and shows an error when bulk-adding a write group to a view-only user', async () => {
        userState.addSelectedUserId('a');

        await userActions.bulkAddToGroup('editors');

        // Pre-fix: this called updateUser('a', { groups: ['viewers', 'editors'] }).
        // Post-fix: the contradictory combination is skipped, no API call.
        expect(api.updateUser).not.toHaveBeenCalled();
        const toast = document.querySelector('.toast-error');
        expect(toast).toBeTruthy();
        expect(toast?.querySelector('.toast-message')?.textContent).toContain('contradictory');
        expect(toast?.querySelector('.toast-message')?.textContent).toContain('alice@test.com');
      });

      it('keeps the skipped contradictory user selected so the admin can fix it', async () => {
        userState.addSelectedUserId('a');

        await userActions.bulkAddToGroup('editors');

        expect(userState.selectedUserIds.has('a')).toBe(true);
      });

      it('applies to compatible users and skips only the contradictory ones in a mixed selection', async () => {
        userState.addSelectedUserId('a'); // view-only -> would conflict
        userState.addSelectedUserId('b'); // no groups -> safe to add

        await userActions.bulkAddToGroup('editors');

        // Bob gets the write group; Alice is skipped.
        expect(api.updateUser).toHaveBeenCalledTimes(1);
        expect(api.updateUser).toHaveBeenCalledWith('b', { groups: ['editors'] });
        // Compatible user is deselected, contradictory user stays selected.
        expect(userState.selectedUserIds.has('b')).toBe(false);
        expect(userState.selectedUserIds.has('a')).toBe(true);
        // The error toast names the skipped user.
        expect(document.querySelector('.toast-error')?.textContent).toContain('alice@test.com');
      });

      it('bulk-adding a compatible write group to a user with only write groups still works', async () => {
        const writeOnly = [
          { id: 'c', email: 'carol@test.com', groups: ['editors'], mfa_enabled: false },
        ];
        const extraGroups = [
          ...permGroups,
          {
            id: 'admins2', name: 'Admins2',
            permissions: [{ action: 'delete', resource: 'aws' }],
            description: '', allowed_accounts: [],
          },
        ];
        userState.setAllUsers(writeOnly as any);
        userState.setAvailableGroups(extraGroups as any);
        (api.listUsers as jest.Mock).mockResolvedValue({ users: writeOnly });
        (api.listGroups as jest.Mock).mockResolvedValue({ groups: extraGroups });
        userState.addSelectedUserId('c');

        await userActions.bulkAddToGroup('admins2');

        expect(api.updateUser).toHaveBeenCalledWith('c', { groups: ['editors', 'admins2'] });
        expect(document.querySelector('.toast-error')).toBeFalsy();
      });
    });
  });
});
