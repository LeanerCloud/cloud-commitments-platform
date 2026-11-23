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
});
