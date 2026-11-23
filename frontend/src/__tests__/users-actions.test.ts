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
});
