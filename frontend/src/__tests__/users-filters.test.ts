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

  describe('clearFilters', () => {
    it('should clear all filters', () => {
      userState.setSearchQuery('test');
      userState.setMfaFilter('enabled');
      userState.setGroupFilter('00000000-0000-5000-8000-000000000001');

      userFilters.clearFilters();

      expect(userState.searchQuery).toBe('');
      expect(userState.mfaFilter).toBe('');
      expect(userState.groupFilter).toBe('');
    });

    it('should reset filter inputs', () => {
      const searchInput = document.getElementById('user-search') as HTMLInputElement;
      const mfaSelect = document.getElementById('user-mfa-filter') as HTMLSelectElement;
      const groupSelect = document.getElementById('user-group-filter') as HTMLSelectElement;

      searchInput.value = 'test';
      mfaSelect.value = 'enabled';
      groupSelect.value = 'admins';

      userFilters.clearFilters();

      expect(searchInput.value).toBe('');
      expect(mfaSelect.value).toBe('');
      expect(groupSelect.value).toBe('');
    });

    it('should re-render all users after clearing', () => {
      userState.setMfaFilter('disabled');
      userFilters.applyFilters();
      // Only 1 user has MFA disabled
      expect(userState.filteredUsers.length).toBe(1);

      userFilters.clearFilters();
      expect(userState.filteredUsers.length).toBe(4);
    });

    it('should handle missing DOM elements gracefully', () => {
      document.body.innerHTML = '';
      expect(() => userFilters.clearFilters()).not.toThrow();
    });
  });

  describe('updateGroupFilterDropdown', () => {
    beforeEach(() => {
      document.body.innerHTML = `
        <select id="user-group-filter">
          <option value="">All Groups</option>
        </select>
      `;
      userState.setAvailableGroups([
        { id: 'g1', name: 'Admins', permissions: [], description: '' },
        { id: 'g2', name: 'Users', permissions: [], description: '' }
      ] as any);
    });

    it('should populate group dropdown', () => {
      userFilters.updateGroupFilterDropdown();

      const select = document.getElementById('user-group-filter') as HTMLSelectElement;
      expect(select.options.length).toBe(3); // All Groups + 2 groups
      expect(select.options[1]?.value).toBe('g1');
      expect(select.options[1]?.textContent?.trim()).toBe('Admins');
      expect(select.options[2]?.value).toBe('g2');
      expect(select.options[2]?.textContent?.trim()).toBe('Users');
    });

    it('should preserve current selection', () => {
      const select = document.getElementById('user-group-filter') as HTMLSelectElement;
      select.innerHTML = '<option value="">All</option><option value="g1">Admins</option>';
      select.value = 'g1';

      userFilters.updateGroupFilterDropdown();

      expect(select.value).toBe('g1');
    });

    it('should handle missing element gracefully', () => {
      document.body.innerHTML = '';
      expect(() => userFilters.updateGroupFilterDropdown()).not.toThrow();
    });

    it('should escape HTML in group names', () => {
      userState.setAvailableGroups([
        { id: 'g1', name: '<script>alert("xss")</script>', permissions: [], description: '' }
      ] as any);

      userFilters.updateGroupFilterDropdown();

      const select = document.getElementById('user-group-filter') as HTMLSelectElement;
      expect(select.innerHTML).toContain('&lt;script&gt;');
    });

    it('11-M2: escapes HTML special chars in group id (value attribute)', () => {
      // Regression: group.id was injected raw into value="..." in the innerHTML
      // template. An id containing quotes or angle brackets could break the
      // option element boundary. Verify escapeHtml() is applied to the id too.
      userState.setAvailableGroups([
        { id: '"><img src=x onerror=alert(1)>', name: 'Evil Group', permissions: [], description: '' }
      ] as any);

      userFilters.updateGroupFilterDropdown();

      const select = document.getElementById('user-group-filter') as HTMLSelectElement;
      // No element may be injected out of the value attribute.
      expect(select.querySelector('img')).toBeNull();
      // The raw payload must not appear verbatim inside the rendered HTML.
      expect(select.innerHTML).not.toContain('"><img src=x');
      // With full escaping (issue #1195 made escapeHtml quote-safe), the
      // option value round-trips the original id intact instead of being
      // truncated at the first double quote.
      const opt = select.options[1];
      expect(opt?.value).toBe('"><img src=x onerror=alert(1)>');
    });
  });
});
