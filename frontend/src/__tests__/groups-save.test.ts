/**
 * Tests for groups module (saveGroup and addPermission)
 */

import { mockGroups } from './groups-harness';
import { loadUsers } from '../users/userActions';
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

  describe('saveGroup', () => {
    beforeEach(() => {
      document.body.innerHTML = `
        <div id="group-modal" class="hidden">
          <span id="group-modal-title"></span>
          <form id="group-form">
            <input id="group-id" value="" />
            <input id="group-name" value="New Group" />
            <textarea id="group-description">New description</textarea>
            <div id="permissions-list">
              <div class="permission-item">
                <select class="perm-action"><option value="execute" selected>Execute</option></select>
                <select class="perm-resource"><option value="*" selected>All</option></select>
                <input class="perm-providers" value="aws, azure" />
                <input class="perm-services" value="ec2" />
                <input class="perm-regions" value="us-east-1" />
                <input class="perm-max-amount" value="10000" />
              </div>
            </div>
          </form>
        </div>
      `;
      groupState.setCurrentEditingGroup(null);
      (api.createGroup as jest.Mock).mockResolvedValue({ id: 'new-group' });
      (api.updateGroup as jest.Mock).mockResolvedValue({ id: 'group-1' });
      (loadUsers as jest.Mock).mockResolvedValue(undefined);
    });

    it('should prevent default form submission', async () => {
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveGroup(event);
      expect(event.preventDefault).toHaveBeenCalled();
    });

    it('should create new group when no current editing group', async () => {
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveGroup(event);

      expect(api.createGroup).toHaveBeenCalledWith({
        name: 'New Group',
        description: 'New description',
        permissions: [
          {
            action: 'execute',
            resource: '*',
            constraints: {
              providers: ['aws', 'azure'],
              services: ['ec2'],
              regions: ['us-east-1'],
              max_amount: 10000,
            },
          },
        ],
      });
    });

    it('should update existing group when editing', async () => {
      const group = mockGroups[0];
      if (!group) throw new Error('Test data missing');
      groupState.setCurrentEditingGroup(group);

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveGroup(event);

      expect(api.updateGroup).toHaveBeenCalledWith('group-1', expect.objectContaining({
        name: 'New Group',
        description: 'New description',
      }));
    });

    it('should show success message after creating group', async () => {
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveGroup(event);

      expect(document.querySelector('.toast-success')?.textContent).toContain('Group created successfully');
    });

    it('should show success message after updating group', async () => {
      const group = mockGroups[0];
      if (!group) throw new Error('Test data missing');
      groupState.setCurrentEditingGroup(group);

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveGroup(event);

      expect(document.querySelector('.toast-success')?.textContent).toContain('Group updated successfully');
    });

    it('should close modal after successful save', async () => {
      const modal = document.getElementById('group-modal');
      modal?.classList.remove('hidden');

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveGroup(event);

      expect(modal?.classList.contains('hidden')).toBe(true);
    });

    it('should reload users after successful save', async () => {
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveGroup(event);

      expect(loadUsers).toHaveBeenCalled();
    });

    it('should handle API errors gracefully', async () => {
      (api.createGroup as jest.Mock).mockRejectedValue(new Error('Server error'));

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveGroup(event);

      expect(document.querySelector('.toast-error')?.textContent).toContain('Failed to save group');
    });

    it('should collect permissions without constraints', async () => {
      document.body.innerHTML = `
        <div id="group-modal">
          <form id="group-form">
            <input id="group-name" value="Simple Group" />
            <textarea id="group-description">Simple description</textarea>
            <div id="permissions-list">
              <div class="permission-item">
                <select class="perm-action"><option value="view" selected>View</option></select>
                <select class="perm-resource"><option value="*" selected>All</option></select>
                <input class="perm-providers" value="" />
                <input class="perm-services" value="" />
                <input class="perm-regions" value="" />
                <input class="perm-max-amount" value="" />
              </div>
            </div>
          </form>
        </div>
      `;

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveGroup(event);

      expect(api.createGroup).toHaveBeenCalledWith({
        name: 'Simple Group',
        description: 'Simple description',
        permissions: [{ action: 'view', resource: '*' }],
      });
    });

    it('refuses a permission row without an action instead of dropping it', async () => {
      document.body.innerHTML = `
        <div id="group-modal">
          <form id="group-form">
            <input id="group-name" value="Test Group" />
            <textarea id="group-description"></textarea>
            <div id="permissions-list">
              <div class="permission-item">
                <select class="perm-action"><option value="" selected></option></select>
                <select class="perm-resource"><option value="*" selected>All</option></select>
              </div>
              <div class="permission-item">
                <select class="perm-action"><option value="execute" selected>Execute</option></select>
                <select class="perm-resource"><option value="*" selected>All</option></select>
              </div>
            </div>
          </form>
        </div>
      `;

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveGroup(event);

      // Issue #213: dropping an authorization row silently is not
      // acceptable; the row is refused with a note until fixed or removed.
      expect(api.createGroup).not.toHaveBeenCalled();
      expect(document.querySelector('.perm-action')?.getAttribute('aria-invalid')).toBe('true');
      expect(document.querySelector('.permission-item .field-error')?.textContent)
        .toContain('This permission has no action');
    });

    it('includes permissions with default resource value', async () => {
      // With a select dropdown, resource always has a value (defaults to *)
      // so permissions are never skipped due to missing resource
      document.body.innerHTML = '<div id="group-modal"><form id="group-form">'
        + '<input id="group-name" value="Test Group" />'
        + '<textarea id="group-description"></textarea>'
        + '<div id="permissions-list"><div class="permission-item">'
        + '<select class="perm-action"><option value="execute" selected>Execute</option></select>'
        + '<select class="perm-resource"><option value="*" selected>All</option></select>'
        + '</div></div></form></div>';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveGroup(event);

      expect(api.createGroup).toHaveBeenCalledWith(expect.objectContaining({
        permissions: [expect.objectContaining({ action: 'execute', resource: '*' })],
      }));
    });

    it('should handle a missing permissions list without throwing, and refuse to save (issue #237)', async () => {
      document.body.innerHTML = `
        <div id="group-modal">
          <form id="group-form">
            <input id="group-name" value="Test Group" />
            <textarea id="group-description"></textarea>
          </form>
        </div>
      `;

      const event = { preventDefault: jest.fn() } as unknown as Event;
      // Must not throw even though #permissions-list is entirely absent from
      // the DOM (collectPermissions() returns [] rather than erroring).
      await expect(groupModals.saveGroup(event)).resolves.toBeUndefined();

      // A group must grant at least one permission -- an empty list is
      // refused rather than submitted as a group with zero permissions
      // (issue #237: the update path's [] means "leave unchanged", so
      // submitting it here would be indistinguishable from that).
      expect(api.createGroup).not.toHaveBeenCalled();
    });
  });

  describe('addPermission', () => {
    it('should add permission item to list', () => {
      const permissionsList = document.getElementById('permissions-list');
      permissionsList!.innerHTML = '';

      groupModals.addPermission();

      const items = permissionsList?.querySelectorAll('.permission-item');
      expect(items?.length).toBe(1);
    });

    it('should add permission with default values', () => {
      const permissionsList = document.getElementById('permissions-list');
      permissionsList!.innerHTML = '';

      groupModals.addPermission();

      const resourceInput = permissionsList?.querySelector('.perm-resource') as HTMLInputElement;
      expect(resourceInput?.value).toBe('*');
    });

    it('should add permission with provided values', () => {
      const permissionsList = document.getElementById('permissions-list');
      permissionsList!.innerHTML = '';

      const permission: api.Permission = {
        action: 'approve',
        resource: 'purchases',
        constraints: {
          providers: ['aws'],
          services: ['ec2', 'rds'],
          regions: ['us-east-1'],
          max_amount: 5000,
        },
      };

      groupModals.addPermission(permission);

      const actionSelect = permissionsList?.querySelector('.perm-action') as HTMLSelectElement;
      const resourceSelect = permissionsList?.querySelector('.perm-resource') as HTMLSelectElement;
      const providersInput = permissionsList?.querySelector('.perm-providers') as HTMLInputElement;
      const servicesInput = permissionsList?.querySelector('.perm-services') as HTMLInputElement;
      const regionsInput = permissionsList?.querySelector('.perm-regions') as HTMLInputElement;
      const maxAmountInput = permissionsList?.querySelector('.perm-max-amount') as HTMLInputElement;

      expect(actionSelect.value).toBe('approve');
      expect(resourceSelect.value).toBe('purchases');
      expect(providersInput.value).toBe('aws');
      expect(servicesInput.value).toBe('ec2, rds');
      expect(regionsInput.value).toBe('us-east-1');
      expect(maxAmountInput.value).toBe('5000');
    });

    it('should add remove button with click handler', () => {
      const permissionsList = document.getElementById('permissions-list');
      permissionsList!.innerHTML = '';

      groupModals.addPermission();

      const removeBtn = permissionsList?.querySelector('.remove-permission-btn');
      expect(removeBtn).toBeTruthy();

      removeBtn?.dispatchEvent(new Event('click'));

      expect(permissionsList?.querySelectorAll('.permission-item').length).toBe(0);
    });

    it('should handle missing permissions list', () => {
      document.body.innerHTML = '';
      expect(() => groupModals.addPermission()).not.toThrow();
    });

    it('should render all action options', () => {
      const permissionsList = document.getElementById('permissions-list');
      permissionsList!.innerHTML = '';

      groupModals.addPermission();

      const actionSelect = permissionsList?.querySelector('.perm-action') as HTMLSelectElement;
      const options = Array.from(actionSelect.options).map(opt => opt.value);

      expect(options).toContain('execute');
      expect(options).toContain('approve');
      expect(options).toContain('create');
      expect(options).toContain('update');
      expect(options).toContain('delete');
      expect(options).toContain('view');
      expect(options).toContain('update');
    });

    it('should handle permission without constraints', () => {
      const permissionsList = document.getElementById('permissions-list');
      permissionsList!.innerHTML = '';

      const permission: api.Permission = {
        action: 'view',
        resource: '*',
      };

      groupModals.addPermission(permission);

      const providersInput = permissionsList?.querySelector('.perm-providers') as HTMLInputElement;
      expect(providersInput.value).toBe('');
    });
  });
});
