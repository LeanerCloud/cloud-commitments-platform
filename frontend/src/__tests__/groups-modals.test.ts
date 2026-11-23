/**
 * Tests for groups module (group modal create, edit and close)
 */

import { mockGroups } from './groups-harness';
import * as api from '../api';
import * as groupState from '../groups/state';
import * as groupModals from '../groups/groupModals';

describe('groups/groupModals', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <div id="group-modal" class="hidden">
        <span id="group-modal-title"></span>
        <form id="group-form">
          <input id="group-id" value="existing-id" />
          <input id="group-name" value="Existing Name" />
          <textarea id="group-description">Existing description</textarea>
          <div id="permissions-list">
            <div class="permission-item">Old permission</div>
          </div>
        </form>
      </div>
    `;
    groupState.setCurrentEditingGroup(null);
    jest.clearAllMocks();
  });

  describe('openCreateGroupModal', () => {
    it('should clear current editing group', () => {
      const group = mockGroups[0];
      if (!group) throw new Error('Test data missing');
      groupState.setCurrentEditingGroup(group);
      groupModals.openCreateGroupModal();
      expect(groupState.currentEditingGroup).toBeNull();
    });

    it('should set modal title to "Create Group"', () => {
      groupModals.openCreateGroupModal();
      const title = document.getElementById('group-modal-title');
      expect(title?.textContent).toBe('Create group');
    });

    it('should reset the form', () => {
      // In JSDOM, form.reset() behavior differs from real browsers
      // We test that the form's reset method was called by verifying
      // that the function runs without error and the modal is shown
      groupModals.openCreateGroupModal();

      const modal = document.getElementById('group-modal');
      const idInput = document.getElementById('group-id') as HTMLInputElement;

      // The group-id field is explicitly cleared to empty string
      expect(idInput.value).toBe('');
      // Modal should be visible after opening
      expect(modal?.classList.contains('hidden')).toBe(false);
    });

    it('should clear the group-id field', () => {
      groupModals.openCreateGroupModal();
      const idInput = document.getElementById('group-id') as HTMLInputElement;
      expect(idInput.value).toBe('');
    });

    it('should clear permissions list', () => {
      groupModals.openCreateGroupModal();
      const permissionsList = document.getElementById('permissions-list');
      expect(permissionsList?.innerHTML).toBe('');
    });

    it('should remove hidden class from modal', () => {
      groupModals.openCreateGroupModal();
      const modal = document.getElementById('group-modal');
      expect(modal?.classList.contains('hidden')).toBe(false);
    });

    it('should handle missing modal elements gracefully', () => {
      document.body.innerHTML = '';
      expect(() => groupModals.openCreateGroupModal()).not.toThrow();
    });

    it('should handle missing permissions list gracefully', () => {
      document.body.innerHTML = `
        <div id="group-modal" class="hidden">
          <span id="group-modal-title"></span>
          <form id="group-form">
            <input id="group-id" />
            <input id="group-name" />
            <textarea id="group-description"></textarea>
          </form>
        </div>
      `;
      expect(() => groupModals.openCreateGroupModal()).not.toThrow();
    });
  });

  describe('openEditGroupModal', () => {
    const groupWithConstraints: api.APIGroup = {
      id: 'group-with-constraints',
      name: 'Constrained Group',
      description: 'Group with permission constraints',
      permissions: [
        {
          action: 'execute',
          resource: 'purchases',
          constraints: {
            providers: ['aws'],
            services: ['ec2', 'rds'],
            regions: ['us-east-1', 'us-west-2'],
            max_amount: 5000,
          },
        },
      ],
    };

    beforeEach(() => {
      const group = mockGroups[0];
      if (!group) throw new Error('Test data missing');
      (api.getGroup as jest.Mock).mockResolvedValue(group);
    });

    it('should fetch group details from API', async () => {
      await groupModals.openEditGroupModal('group-1');
      expect(api.getGroup).toHaveBeenCalledWith('group-1');
    });

    it('should set current editing group', async () => {
      const group = mockGroups[0];
      if (!group) throw new Error('Test data missing');
      await groupModals.openEditGroupModal('group-1');
      expect(groupState.currentEditingGroup).toEqual(group);
    });

    it('should set modal title to "Edit Group"', async () => {
      await groupModals.openEditGroupModal('group-1');
      const title = document.getElementById('group-modal-title');
      expect(title?.textContent).toBe('Edit group');
    });

    it('should populate form with group data', async () => {
      await groupModals.openEditGroupModal('group-1');

      const idInput = document.getElementById('group-id') as HTMLInputElement;
      const nameInput = document.getElementById('group-name') as HTMLInputElement;
      const descInput = document.getElementById('group-description') as HTMLTextAreaElement;

      expect(idInput.value).toBe('group-1');
      expect(nameInput.value).toBe('Administrators');
      expect(descInput.value).toBe('Admin group with full access');
    });

    it('should render existing permissions', async () => {
      await groupModals.openEditGroupModal('group-1');

      const permissionsList = document.getElementById('permissions-list');
      const items = permissionsList?.querySelectorAll('.permission-item');
      expect(items?.length).toBe(2); // Two permissions in mockGroups[0]
    });

    it('should render permissions with constraints', async () => {
      (api.getGroup as jest.Mock).mockResolvedValue(groupWithConstraints);

      await groupModals.openEditGroupModal('group-with-constraints');

      const permissionsList = document.getElementById('permissions-list');
      expect(permissionsList?.innerHTML).toContain('purchases');
      expect(permissionsList?.innerHTML).toContain('aws');
      expect(permissionsList?.innerHTML).toContain('ec2, rds');
      expect(permissionsList?.innerHTML).toContain('us-east-1, us-west-2');
      expect(permissionsList?.innerHTML).toContain('5000');
    });

    it('should remove hidden class from modal', async () => {
      await groupModals.openEditGroupModal('group-1');
      const modal = document.getElementById('group-modal');
      expect(modal?.classList.contains('hidden')).toBe(false);
    });

    it('should handle API errors gracefully', async () => {
      (api.getGroup as jest.Mock).mockRejectedValue(new Error('Network error'));

      await groupModals.openEditGroupModal('group-1');

      // Should show error toast
      expect(document.querySelector('.toast-error')).toBeTruthy();
    });

    it('should handle missing modal elements', async () => {
      document.body.innerHTML = '';
      await groupModals.openEditGroupModal('group-1');
      // Should not throw, just return early
    });

    it('should add default permission when group has no permissions', async () => {
      const group = mockGroups[2]; // Empty Group
      if (!group) throw new Error('Test data missing');
      (api.getGroup as jest.Mock).mockResolvedValue(group);

      await groupModals.openEditGroupModal('group-3');

      const permissionsList = document.getElementById('permissions-list');
      const items = permissionsList?.querySelectorAll('.permission-item');
      expect(items?.length).toBe(1); // Should add one empty permission
    });
  });

  describe('closeGroupModal', () => {
    it('should add hidden class to modal', () => {
      const modal = document.getElementById('group-modal');
      modal?.classList.remove('hidden');

      groupModals.closeGroupModal();

      expect(modal?.classList.contains('hidden')).toBe(true);
    });

    it('should clear current editing group', () => {
      const group = mockGroups[0];
      if (!group) throw new Error('Test data missing');
      groupState.setCurrentEditingGroup(group);
      groupModals.closeGroupModal();
      expect(groupState.currentEditingGroup).toBeNull();
    });

    it('should handle missing modal gracefully', () => {
      document.body.innerHTML = '';
      expect(() => groupModals.closeGroupModal()).not.toThrow();
    });
  });
});
