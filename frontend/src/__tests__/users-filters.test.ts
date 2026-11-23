/**
 * Tests for users filters
 */

// The harness registers the module mocks, so it must be imported first.
import './users-harness';

import * as userState from '../users/state';
import * as userFilters from '../users/filters';

// ============================================================================
// FILTERS TESTS
// ============================================================================
describe('users/filters', () => {
  const mockUsers = [
    { id: '1', email: 'admin@test.com', groups: ['00000000-0000-5000-8000-000000000001'], mfa_enabled: true },
    { id: '2', email: 'user@test.com', groups: ['users'], mfa_enabled: false },
    { id: '3', email: 'viewer@test.com', groups: [], mfa_enabled: true },
    { id: '4', email: 'another.user@example.com', groups: ['users', 'developers'], mfa_enabled: true }
  ];

  beforeEach(() => {
    document.body.innerHTML = `
      <div id="users-list"></div>
      <div id="user-stats"></div>
      <div id="bulk-actions-bar" class="hidden">
        <span id="selected-count">0</span>
      </div>
      <input type="text" id="user-search" />
      <select id="user-mfa-filter"><option value="">All</option></select>
      <select id="user-group-filter"><option value="">All</option></select>
    `;
    userState.setAllUsers(mockUsers as any);
    userState.setSearchQuery('');
    userState.setMfaFilter('');
    userState.setGroupFilter('');
    userState.clearSelectedUserIds();
  });

  describe('applyFilters', () => {
    it('should filter by search term (email)', () => {
      userState.setSearchQuery('admin');
      userFilters.applyFilters();
      expect(userState.filteredUsers.length).toBe(1);
      expect(userState.filteredUsers[0]?.email).toBe('admin@test.com');
    });

    it('should filter by search term case-insensitively', () => {
      userState.setSearchQuery('ADMIN');
      userFilters.applyFilters();
      expect(userState.filteredUsers.length).toBe(1);
      expect(userState.filteredUsers[0]?.email).toBe('admin@test.com');
    });

    it('should filter by partial email match', () => {
      userState.setSearchQuery('@test.com');
      userFilters.applyFilters();
      expect(userState.filteredUsers.length).toBe(3);
    });

    it('should filter by MFA enabled', () => {
      userState.setMfaFilter('enabled');
      userFilters.applyFilters();
      expect(userState.filteredUsers.length).toBe(3);
      expect(userState.filteredUsers.every(u => u.mfa_enabled)).toBe(true);
    });

    it('should filter by MFA disabled', () => {
      userState.setMfaFilter('disabled');
      userFilters.applyFilters();
      expect(userState.filteredUsers.length).toBe(1);
      expect(userState.filteredUsers[0]!.mfa_enabled).toBe(false);
    });

    it('should filter by group', () => {
      userState.setGroupFilter('00000000-0000-5000-8000-000000000001');
      userFilters.applyFilters();
      expect(userState.filteredUsers.length).toBe(1);
      expect(userState.filteredUsers[0]!.groups).toContain('00000000-0000-5000-8000-000000000001');
    });

    it('should filter by users group (multiple users)', () => {
      userState.setGroupFilter('users');
      userFilters.applyFilters();
      expect(userState.filteredUsers.length).toBe(2);
    });

    it('should filter users with no groups (empty filter shows users without the specified group)', () => {
      userState.setGroupFilter('nonexistent');
      userFilters.applyFilters();
      expect(userState.filteredUsers.length).toBe(0);
    });

    it('should combine all filters', () => {
      userState.setSearchQuery('user');
      userState.setMfaFilter('enabled');
      userState.setGroupFilter('users');
      userFilters.applyFilters();
      expect(userState.filteredUsers.length).toBe(1);
      expect(userState.filteredUsers[0]?.email).toBe('another.user@example.com');
    });

    it('should return all users when no filters applied', () => {
      userFilters.applyFilters();
      expect(userState.filteredUsers.length).toBe(4);
    });

    it('should return empty when no users match filters', () => {
      userState.setSearchQuery('nonexistent');
      userFilters.applyFilters();
      expect(userState.filteredUsers.length).toBe(0);
    });
  });

  describe('handleUserSearch', () => {
    it('should update search query and apply filters', () => {
      userFilters.handleUserSearch('admin');
      expect(userState.searchQuery).toBe('admin');
      expect(userState.filteredUsers.length).toBe(1);
    });

    it('should render users after search', () => {
      userFilters.handleUserSearch('test');
      const container = document.getElementById('users-list');
      expect(container?.innerHTML).toContain('test');
    });

    it('should clear search when empty string', () => {
      userFilters.handleUserSearch('admin');
      userFilters.handleUserSearch('');
      expect(userState.searchQuery).toBe('');
      expect(userState.filteredUsers.length).toBe(4);
    });
  });

  describe('handleFilterChange', () => {
    it('should handle mfa filter change', () => {
      userFilters.handleFilterChange('mfa', 'enabled');
      expect(userState.mfaFilter).toBe('enabled');
      expect(userState.filteredUsers.length).toBe(3);
    });

    it('should handle group filter change', () => {
      userFilters.handleFilterChange('group', '00000000-0000-5000-8000-000000000001');
      expect(userState.groupFilter).toBe('00000000-0000-5000-8000-000000000001');
      expect(userState.filteredUsers.length).toBe(1);
    });

    it('should ignore unknown filter types', () => {
      userFilters.handleFilterChange('unknown', 'value');
      // Should not throw and filters should remain unchanged
      expect(userState.mfaFilter).toBe('');
      expect(userState.groupFilter).toBe('');
    });
  });
});
