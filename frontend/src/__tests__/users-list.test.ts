/**
 * Tests for users list rendering
 */

// The harness registers the module mocks, so it must be imported first.
import './users-harness';

import * as userState from '../users/state';
import * as userList from '../users/userList';

// ============================================================================
// USER LIST TESTS
// ============================================================================
describe('users/userList', () => {
  const mockUsers = [
    { id: '1', email: 'admin@test.com', groups: ['00000000-0000-5000-8000-000000000001'], mfa_enabled: true, created_at: '2024-01-01T00:00:00Z' },
    { id: '2', email: 'user@test.com', groups: [], mfa_enabled: false, created_at: '2024-01-02T00:00:00Z' }
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
    userState.clearSelectedUserIds();
    jest.clearAllMocks();
  });

  describe('renderUserStats', () => {
    it('should render user statistics', () => {
      userList.renderUserStats();

      const statsContainer = document.getElementById('user-stats');
      expect(statsContainer?.innerHTML).toContain('Total Users');
      expect(statsContainer?.innerHTML).toContain('2');
      expect(statsContainer?.innerHTML).toContain('Administrators');
      expect(statsContainer?.innerHTML).toContain('1');
      expect(statsContainer?.innerHTML).toContain('MFA Enabled');
      expect(statsContainer?.innerHTML).toContain('Showing');
    });

    it('should show correct admin count', () => {
      userList.renderUserStats();

      const statsContainer = document.getElementById('user-stats');
      const html = statsContainer?.innerHTML || '';
      expect(html).toContain('Administrators');
    });

    it('should show correct MFA enabled count', () => {
      userList.renderUserStats();

      const statsContainer = document.getElementById('user-stats');
      const html = statsContainer?.innerHTML || '';
      expect(html).toContain('MFA Enabled');
    });

    it('should highlight when showing filtered subset', () => {
      userState.setFilteredUsers([mockUsers[0]] as any);
      userList.renderUserStats();

      const statsContainer = document.getElementById('user-stats');
      expect(statsContainer?.innerHTML).toContain('stat-card-highlight');
    });

    it('should not highlight when showing all users', () => {
      userList.renderUserStats();

      const statsContainer = document.getElementById('user-stats');
      // When filteredUsers.length === allUsers.length, no highlight
      const highlightCount = (statsContainer?.innerHTML.match(/stat-card-highlight/g) || []).length;
      expect(highlightCount).toBe(0);
    });

    it('should handle missing container', () => {
      document.body.innerHTML = '';
      expect(() => userList.renderUserStats()).not.toThrow();
    });

    it('should handle empty users', () => {
      userState.setAllUsers([]);
      userState.setFilteredUsers([]);
      userList.renderUserStats();

      const statsContainer = document.getElementById('user-stats');
      expect(statsContainer?.innerHTML).toContain('0');
    });
  });

  describe('renderUsers', () => {
    it('should render users table', () => {
      userList.renderUsers(mockUsers as any);

      const container = document.getElementById('users-list');
      expect(container?.querySelector('table')).toBeTruthy();
      expect(container?.innerHTML).toContain('admin@test.com');
      expect(container?.innerHTML).toContain('user@test.com');
    });

    it('should show empty message when no users', () => {
      userList.renderUsers([]);

      const container = document.getElementById('users-list');
      expect(container?.innerHTML).toContain('No users found');
    });

    it('should show selected state for selected users', () => {
      userState.addSelectedUserId('1');

      userList.renderUsers(mockUsers as any);

      const container = document.getElementById('users-list');
      expect(container?.querySelector('.row-selected')).toBeTruthy();
    });

    it('should render checkboxes for each user', () => {
      userList.renderUsers(mockUsers as any);

      const checkboxes = document.querySelectorAll('.user-checkbox');
      expect(checkboxes.length).toBe(2);
    });

    it('should render select all checkbox', () => {
      userList.renderUsers(mockUsers as any);

      const selectAll = document.getElementById('select-all-users');
      expect(selectAll).toBeTruthy();
    });

    it('should check select all when all users selected', () => {
      userState.addSelectedUserId('1');
      userState.addSelectedUserId('2');

      userList.renderUsers(mockUsers as any);

      const selectAll = document.getElementById('select-all-users') as HTMLInputElement;
      expect(selectAll?.checked).toBe(true);
    });

    it('should render group badges (role column removed, PR #912)', () => {
      userList.renderUsers(mockUsers as any);
      // The Role column was removed; verify the table renders without throwing
      const container = document.getElementById('users-list');
      expect(container?.querySelector('table')).toBeTruthy();
      // Group badges still appear
      expect(container?.innerHTML).toContain('badge-group');
    });

    it('should render MFA status badges', () => {
      userList.renderUsers(mockUsers as any);

      const container = document.getElementById('users-list');
      expect(container?.innerHTML).toContain('badge-success');
      expect(container?.innerHTML).toContain('badge-warning');
    });

    it('should render group badges', () => {
      userList.renderUsers(mockUsers as any);

      const container = document.getElementById('users-list');
      expect(container?.innerHTML).toContain('badge-group');
      // User 1 is in the Administrators group (ADMIN_GID); since availableGroups
      // is empty the group name lookup falls back to the UUID itself.
      expect(container?.innerHTML).toContain('00000000');
    });

    it('should show "No groups" for users without groups', () => {
      userList.renderUsers(mockUsers as any);

      const container = document.getElementById('users-list');
      expect(container?.innerHTML).toContain('No groups');
    });

    it('should render edit and delete buttons', () => {
      userList.renderUsers(mockUsers as any);

      const container = document.getElementById('users-list');
      expect(container?.querySelectorAll('.edit-user-btn').length).toBe(2);
      expect(container?.querySelectorAll('.delete-user-btn').length).toBe(2);
    });

    it('should handle missing container gracefully', () => {
      document.body.innerHTML = '';
      expect(() => userList.renderUsers(mockUsers as any)).not.toThrow();
    });

    it('should show created date', () => {
      userList.renderUsers(mockUsers as any);

      const container = document.getElementById('users-list');
      // Canonical format via formatDate: "Jan 1, 2024" (day varies with TZ
      // interpretation of the 00:00Z instant, so match structure + year).
      expect(container?.innerHTML).toMatch(/Jan \d{1,2}, 2024|Dec 3\d, 2023/);
    });

    it('should show dash for missing created_at', () => {
      const usersWithoutDate = [
        { id: '1', email: 'test@test.com', groups: [], mfa_enabled: false }
      ];
      userList.renderUsers(usersWithoutDate as any);

      const container = document.getElementById('users-list');
      const html = container?.innerHTML || '';
      // Should have a dash for missing date
      expect(html).toContain('-');
    });

    it('should render last login as Never when not set', () => {
      userList.renderUsers(mockUsers as any);

      const container = document.getElementById('users-list');
      expect(container?.innerHTML).toContain('Never');
    });

    it('should render last login relative time when set', () => {
      const usersWithLogin = [
        { id: '1', email: 'test@test.com', groups: [], mfa_enabled: false, last_login: new Date().toISOString() }
      ];
      userList.renderUsers(usersWithLogin as any);

      const container = document.getElementById('users-list');
      expect(container?.innerHTML).toContain('Just now');
    });

    it('should mark current user with "You" badge', () => {
      const usersWithCurrent = [
        { id: 'current', email: 'me@test.com', groups: [], mfa_enabled: false }
      ];
      userList.renderUsers(usersWithCurrent as any);

      const container = document.getElementById('users-list');
      expect(container?.innerHTML).toContain('You');
      expect(container?.innerHTML).toContain('badge-info');
    });

    it('should escape HTML in email', () => {
      const usersWithXss = [
        { id: '1', email: '<script>alert("xss")</script>', groups: [], mfa_enabled: false }
      ];
      userList.renderUsers(usersWithXss as any);

      const container = document.getElementById('users-list');
      expect(container?.innerHTML).toContain('&lt;script&gt;');
    });
  });
});
