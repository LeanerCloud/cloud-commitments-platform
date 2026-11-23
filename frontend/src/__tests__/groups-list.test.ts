/**
 * Tests for groups module (state and list)
 */

import { mockConfirmDialog, mockGroups, mockUsers } from './groups-harness';
import { ADMIN_CARVED_OUTS } from '../permissions';
import * as api from '../api';
import * as groupState from '../groups/state';
import * as groupList from '../groups/groupList';
import * as userState from '../users/state';

describe('groups/state', () => {
  beforeEach(() => {
    groupState.setCurrentEditingGroup(null);
  });

  describe('currentEditingGroup', () => {
    it('should initialize as null', () => {
      expect(groupState.currentEditingGroup).toBeNull();
    });

    it('should set and get current editing group', () => {
      const group = mockGroups[0];
      if (!group) throw new Error('Test data missing');
      groupState.setCurrentEditingGroup(group);
      expect(groupState.currentEditingGroup).toEqual(group);
    });

    it('should clear current editing group', () => {
      const group = mockGroups[0];
      if (!group) throw new Error('Test data missing');
      groupState.setCurrentEditingGroup(group);
      groupState.setCurrentEditingGroup(null);
      expect(groupState.currentEditingGroup).toBeNull();
    });
  });
});

describe('groups/groupList', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <div id="groups-list"></div>
    `;
    userState.setAllUsers(mockUsers as any);
    groupState.setCurrentEditingGroup(null);
  });

  describe('renderGroups', () => {
    it('should show empty message when no groups', () => {
      groupList.renderGroups([]);

      const container = document.getElementById('groups-list');
      expect(container?.innerHTML).toContain('No groups yet');
      expect(container?.querySelector('.empty')).toBeTruthy();
    });

    it('should render group cards with correct structure', () => {
      groupList.renderGroups(mockGroups);

      const container = document.getElementById('groups-list');
      const cards = container?.querySelectorAll('.group-card');
      expect(cards?.length).toBe(3);
      expect(container?.querySelector('.group-card-header')).toBeTruthy();
      expect(container?.querySelector('.group-card-body')).toBeTruthy();
      expect(container?.querySelector('.group-members')).toBeTruthy();
    });

    it('should render group names in h4 elements', () => {
      groupList.renderGroups(mockGroups);

      const container = document.getElementById('groups-list');
      const headings = container?.querySelectorAll('.group-card h4');
      expect(headings?.length).toBe(3);
      expect(headings?.item(0)?.textContent).toBe('Administrators');
      expect(headings?.item(1)?.textContent).toBe('Viewers');
      expect(headings?.item(2)?.textContent).toBe('Empty Group');
    });

    it('should render group descriptions', () => {
      groupList.renderGroups(mockGroups);

      const container = document.getElementById('groups-list');
      expect(container?.innerHTML).toContain('Admin group with full access');
      expect(container?.innerHTML).toContain('Read-only access');
    });

    it('should render member count with badge', () => {
      groupList.renderGroups(mockGroups);

      const container = document.getElementById('groups-list');
      const badges = container?.querySelectorAll('.badge');
      // group-1 has 2 members (user-1 and user-3)
      // group-2 has 2 members (user-2 and user-3)
      // group-3 has 0 members
      expect(badges?.length).toBe(3);
      expect(badges?.item(0)?.textContent).toBe('2 members');
      expect(badges?.item(1)?.textContent).toBe('2 members');
      expect(badges?.item(2)?.textContent).toBe('0 members');
    });

    it('should use singular "member" when count is 1', () => {
      // Set up users so group-1 has exactly 1 member
      userState.setAllUsers([
        { id: 'user-1', email: 'admin@test.com', groups: ['00000000-0000-5000-8000-000000000001', 'group-1'], mfa_enabled: true },
      ] as any);

      const group = mockGroups[0];
      if (!group) throw new Error('Test data missing');
      groupList.renderGroups([group]);

      const container = document.getElementById('groups-list');
      const badge = container?.querySelector('.badge');
      expect(badge?.textContent).toBe('1 member');
    });

    it('should render permission badges', () => {
      groupList.renderGroups(mockGroups);

      const container = document.getElementById('groups-list');
      const badges = container?.querySelectorAll('.permission-badge');
      // group-1 has 2 permissions, group-2 has 1, group-3 has 0
      expect(badges?.length).toBe(3);
      expect(badges?.item(0)?.textContent).toBe('Execute all resources');
      expect(badges?.item(1)?.textContent).toBe('Update all resources');
      expect(badges?.item(2)?.textContent).toBe('View all resources');
      expect(badges?.item(0)?.getAttribute('title')).toBe('execute:*');
    });

    it('labels admin:* as full access with a tooltip naming the raw grant', () => {
      groupList.renderGroups([{ id: 'a', name: 'A', description: '', permissions: [{ action: 'admin', resource: '*' }] } as any]);
      const badge = document.querySelector('#groups-list .permission-badge');
      expect(badge?.textContent).toBe('Full access');
      expect(badge?.getAttribute('title')).toBe(
        `admin:*: full access to all resources, except ${[...ADMIN_CARVED_OUTS].join(', ')}, which need an explicit grant`,
      );
      for (const name of ['execute:purchases', 'approve-any:purchases', 'retry-any:purchases', 'execute:ri-exchange']) {
        expect(badge?.getAttribute('title')).toContain(name);
      }
    });

    it.each([
      ['', 'plans'], ['view', ''], ['*', 'plans'], ['*', '*'], ['View', 'plans'], ['view', 'a b'],
    ])('falls back to the raw grant for unlabelable %j:%j', (action, resource) => {
      groupList.renderGroups([{ id: 'a', name: 'A', description: '', permissions: [{ action, resource }] } as any]);
      const badge = document.querySelector('#groups-list .permission-badge');
      expect(badge?.textContent).toBe(`${action}:${resource}`);
      expect(badge?.getAttribute('title')).toBe(`${action}:${resource}`);
    });

    it('keeps constraints in the tooltip next to the raw grant', () => {
      groupList.renderGroups([{ id: 'a', name: 'A', description: '', permissions: [{ action: 'approve-any', resource: 'purchase_plans', constraints: { providers: ['aws'] } }] } as any]);
      const badge = document.querySelector('#groups-list .permission-badge');
      expect(badge?.textContent).toBe('Approve any purchase plans');
      expect(badge?.getAttribute('title')).toBe('approve-any:purchase_plans (providers: aws)');
    });

    it('should show "No permissions" for group with no permissions', () => {
      const group = mockGroups[2]; // Empty Group
      if (!group) throw new Error('Test data missing');
      groupList.renderGroups([group]);

      const container = document.getElementById('groups-list');
      expect(container?.innerHTML).toContain('No permissions');
    });

    it('should render member pills with email addresses', () => {
      groupList.renderGroups(mockGroups);

      const container = document.getElementById('groups-list');
      const pills = Array.from(container?.querySelectorAll('.member-pill') ?? []).map(p => p.textContent);
      // group-1: admin@test.com, both@test.com; group-2: viewer@test.com, both@test.com; group-3: none
      expect(pills.filter(t => t !== 'No members').length).toBe(4);
    });

    it('should show "No members" for group with no members', () => {
      const group = mockGroups[2]; // Empty Group
      if (!group) throw new Error('Test data missing');
      groupList.renderGroups([group]);

      const container = document.getElementById('groups-list');
      expect(container?.innerHTML).toContain('No members');
      const pill = container?.querySelector('.group-members .member-pill');
      expect(pill?.textContent).toBe('No members');
    });

    it('should render edit and delete buttons with data-group-id', () => {
      groupList.renderGroups(mockGroups);

      const container = document.getElementById('groups-list');
      const editButtons = container?.querySelectorAll('.edit-group-btn');
      const deleteButtons = container?.querySelectorAll('.delete-group-btn');

      expect(editButtons?.length).toBe(3);
      expect(deleteButtons?.length).toBe(3);

      const firstEditBtn = editButtons?.item(0) as HTMLElement | null;
      const firstDeleteBtn = deleteButtons?.item(0) as HTMLElement | null;
      expect(firstEditBtn?.dataset.groupId).toBe('group-1');
      expect(firstDeleteBtn?.dataset.groupId).toBe('group-1');
    });

    it('should escape HTML in group names to prevent XSS', () => {
      const xssGroup: api.APIGroup = {
        id: 'xss-group',
        name: '<script>alert("xss")</script>',
        description: '<img src=x onerror=alert("xss")>',
        permissions: [],
      };

      groupList.renderGroups([xssGroup]);

      const container = document.getElementById('groups-list');
      // The name and description must stay text: no element is created
      // from them anywhere in the card.
      expect(container?.querySelector('script, img')).toBeNull();
      expect(container?.querySelector('h4')?.textContent).toBe(xssGroup.name);
      expect(container?.querySelector('.group-description')?.textContent).toBe(xssGroup.description);
      // The icon buttons carry the name in aria-label. The attribute must
      // round-trip the raw string, so a quote cannot end it early. (innerHTML
      // serialises attribute values without escaping < and >, which is why
      // this checks the DOM rather than the serialised markup.)
      const edit = container?.querySelector('.edit-group-btn');
      expect(edit?.getAttribute('aria-label')).toBe(`Edit group ${xssGroup.name}`);
      expect(edit?.getAttributeNames().sort()).toEqual(['aria-label', 'class', 'data-group-id', 'title']);
    });

    it('should not render if container not found', () => {
      document.body.innerHTML = ''; // Remove the container

      // Should not throw
      expect(() => groupList.renderGroups(mockGroups)).not.toThrow();
    });

    it('should attach click handlers to edit buttons', async () => {
      const group = mockGroups[0];
      if (!group) throw new Error('Test data missing');
      (api.getGroup as jest.Mock).mockResolvedValue(group);

      document.body.innerHTML = `
        <div id="groups-list"></div>
        <div id="group-modal" class="hidden">
          <span id="group-modal-title"></span>
          <form id="group-form">
            <input id="group-id" />
            <input id="group-name" />
            <textarea id="group-description"></textarea>
            <div id="permissions-list"></div>
          </form>
        </div>
      `;

      groupList.renderGroups(mockGroups);

      const editBtn = document.querySelector('.edit-group-btn') as HTMLButtonElement;
      editBtn.click();

      // Wait for async operation
      await new Promise(resolve => setTimeout(resolve, 0));

      expect(api.getGroup).toHaveBeenCalledWith('group-1');
    });

    it('should attach click handlers to delete buttons', async () => {
      (api.deleteGroup as jest.Mock).mockResolvedValue(undefined);
      mockConfirmDialog.mockResolvedValue(true);
      userState.setAvailableGroups(mockGroups as any);

      groupList.renderGroups(mockGroups);

      const deleteBtn = document.querySelector('.delete-group-btn') as HTMLButtonElement;
      deleteBtn.click();

      // Wait for the async confirmDialog + API call chain
      await new Promise(resolve => setTimeout(resolve, 0));
      await new Promise(resolve => setTimeout(resolve, 0));

      expect(mockConfirmDialog).toHaveBeenCalled();
      expect(api.deleteGroup).toHaveBeenCalledWith('group-1');
    });
  });
});
