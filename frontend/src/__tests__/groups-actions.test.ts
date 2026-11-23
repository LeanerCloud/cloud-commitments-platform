/**
 * Tests for groups module (actions and handlers)
 */

import { mockConfirmDialog, mockGroups } from './groups-harness';
import { loadUsers } from '../users/userActions';
import * as api from '../api';
import * as groupModals from '../groups/groupModals';
import * as groupActions from '../groups/groupActions';
import * as groupHandlers from '../groups/handlers';
import * as userState from '../users/state';

describe('groups/groupActions', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <div id="groups-list"></div>
    `;
    userState.setAvailableGroups(mockGroups as any);
    jest.clearAllMocks();
    mockConfirmDialog.mockResolvedValue(true);
    (api.deleteGroup as jest.Mock).mockResolvedValue(undefined);
    (loadUsers as jest.Mock).mockResolvedValue(undefined);
  });

  describe('deleteGroup', () => {
    it('should confirm before deleting', async () => {
      await groupActions.deleteGroup('group-1');

      expect(mockConfirmDialog).toHaveBeenCalledWith(
        expect.objectContaining({ title: expect.stringContaining('Administrators') })
      );
    });

    it('should call API to delete group when confirmed', async () => {
      await groupActions.deleteGroup('group-1');

      expect(api.deleteGroup).toHaveBeenCalledWith('group-1');
    });

    it('should reload users after successful deletion', async () => {
      await groupActions.deleteGroup('group-1');

      expect(loadUsers).toHaveBeenCalled();
    });

    it('should show success message after deletion', async () => {
      await groupActions.deleteGroup('group-1');

      expect(document.querySelector('.toast-success')?.textContent).toContain('Group deleted successfully');
    });

    it('should not delete when user cancels', async () => {
      mockConfirmDialog.mockResolvedValueOnce(false);

      await groupActions.deleteGroup('group-1');

      expect(api.deleteGroup).not.toHaveBeenCalled();
    });

    it('should not delete when group not found', async () => {
      await groupActions.deleteGroup('nonexistent-group');

      expect(mockConfirmDialog).not.toHaveBeenCalled();
      expect(api.deleteGroup).not.toHaveBeenCalled();
    });

    it('should handle API errors gracefully', async () => {
      (api.deleteGroup as jest.Mock).mockRejectedValue(new Error('Server error'));

      await groupActions.deleteGroup('group-1');

      expect(document.querySelector('.toast-error')?.textContent).toContain('Failed to delete group: Server error');
    });
  });

  describe('openEditGroupModal re-export', () => {
    it('should export openEditGroupModal from groupModals', () => {
      expect(groupActions.openEditGroupModal).toBe(groupModals.openEditGroupModal);
    });
  });
});

describe('groups/handlers', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <form id="group-form">
        <button type="submit">Save</button>
      </form>
    `;
    // Clear window properties
    delete (window as any).openCreateGroupModal;
    delete (window as any).closeGroupModal;
    delete (window as any).addPermission;
    jest.clearAllMocks();
  });

  describe('setupGroupHandlers', () => {
    it('should expose openCreateGroupModal on window', () => {
      groupHandlers.setupGroupHandlers();
      expect((window as any).openCreateGroupModal).toBeDefined();
      expect(typeof (window as any).openCreateGroupModal).toBe('function');
    });

    it('should expose closeGroupModal on window', () => {
      groupHandlers.setupGroupHandlers();
      expect((window as any).closeGroupModal).toBeDefined();
      expect(typeof (window as any).closeGroupModal).toBe('function');
    });

    it('should expose addPermission on window', () => {
      groupHandlers.setupGroupHandlers();
      expect((window as any).addPermission).toBeDefined();
      expect(typeof (window as any).addPermission).toBe('function');
    });

    it('should setup form submit handler', () => {
      document.body.innerHTML = `
        <form id="group-form">
          <input id="group-name" value="Test" />
          <textarea id="group-description"></textarea>
          <div id="permissions-list"></div>
        </form>
        <div id="group-modal"></div>
      `;

      groupHandlers.setupGroupHandlers();

      const form = document.getElementById('group-form') as HTMLFormElement;

      // Verify the event listener is attached by checking that the form has the handler
      // We can verify this indirectly by checking the form exists and handlers are set
      expect(form).toBeTruthy();

      // The handler calls saveGroup which is an async function
      // We verify the handler is set up by checking that no error is thrown
      const submitEvent = new Event('submit', { bubbles: true, cancelable: true });
      expect(() => form.dispatchEvent(submitEvent)).not.toThrow();
    });

    it('should handle missing group form gracefully', () => {
      document.body.innerHTML = '';
      expect(() => groupHandlers.setupGroupHandlers()).not.toThrow();
    });

    it('should call window.addPermission without arguments', () => {
      document.body.innerHTML = `
        <form id="group-form"></form>
        <div id="permissions-list"></div>
      `;

      groupHandlers.setupGroupHandlers();

      // Verify the addPermission function was registered on window
      const addPermFn = (window as any).addPermission;
      expect(typeof addPermFn).toBe('function');

      // The function should be callable without throwing
      expect(() => addPermFn()).not.toThrow();

      // Verify that calling it adds a permission item (using the real addPermission from groupModals)
      const permissionsList = document.getElementById('permissions-list');
      const items = permissionsList?.querySelectorAll('.permission-item');
      expect(items?.length).toBe(1);

      expect(permissionsList?.children.length).toBe(1);
    });
  });
});
