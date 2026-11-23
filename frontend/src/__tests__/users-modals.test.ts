/**
 * Tests for users modals
 */

// The harness registers the module mocks, so it must be imported first.
import './users-harness';

import * as api from '../api';
import * as userState from '../users/state';
import * as userModals from '../users/userModals';

// ============================================================================
// USER MODALS TESTS
// ============================================================================
describe('users/userModals', () => {
  const mockUser = {
    id: '1',
    email: 'test@test.com',
    // role removed: PR #912 -- authorization is group-membership based.
    groups: ['users'],
    mfa_enabled: false
  };

  const mockGroups = [
    { id: 'admins', name: 'Admins', permissions: [], description: '' },
    { id: 'users', name: 'Users', permissions: [], description: '' }
  ];

  beforeEach(() => {
    document.body.innerHTML = `
      <div id="user-modal" class="hidden">
        <h2 id="user-modal-title">Create user</h2>
        <form id="user-form">
          <input type="hidden" id="user-id" />
          <input type="email" id="user-email" />
          <div id="password-fields">
            <input type="password" id="user-password" />
          </div>
          <!-- role selector removed in PR #912 -->
          <select id="user-groups" multiple></select>
          <button type="submit">Save</button>
        </form>
      </div>
      <div id="users-list"></div>
      <div id="groups-list"></div>
      <div id="user-stats"></div>
    `;

    userState.setAvailableGroups(mockGroups as any);
    userState.setCurrentEditingUser(null);
    userState.setAllUsers([mockUser] as any);
    userState.setFilteredUsers([mockUser] as any);

    (api.getUser as jest.Mock).mockResolvedValue(mockUser);
    (api.createUser as jest.Mock).mockResolvedValue({});
    (api.updateUser as jest.Mock).mockResolvedValue({});
    (api.listUsers as jest.Mock).mockResolvedValue({ users: [mockUser] });
    (api.listGroups as jest.Mock).mockResolvedValue({ groups: mockGroups });

    jest.clearAllMocks();
  });

  describe('openCreateUserModal', () => {
    it('should open modal for creating user', () => {
      userModals.openCreateUserModal();

      const modal = document.getElementById('user-modal');
      expect(modal?.classList.contains('hidden')).toBe(false);
    });

    it('should set title to Create User', () => {
      userModals.openCreateUserModal();

      const title = document.getElementById('user-modal-title');
      expect(title?.textContent).toBe('Create user');
    });

    it('should reset form', () => {
      (document.getElementById('user-email') as HTMLInputElement).value = 'old@test.com';

      userModals.openCreateUserModal();

      expect((document.getElementById('user-email') as HTMLInputElement).value).toBe('');
    });

    it('should clear user-id', () => {
      (document.getElementById('user-id') as HTMLInputElement).value = '123';

      userModals.openCreateUserModal();

      expect((document.getElementById('user-id') as HTMLInputElement).value).toBe('');
    });

    it('should show password field', () => {
      userModals.openCreateUserModal();

      const passwordFields = document.getElementById('password-fields');
      expect(passwordFields?.classList.contains('hidden')).toBe(false);
    });

    it('should not mark password as required (blank invites the user)', () => {
      // Issue #348 made password optional on create: leaving it blank
      // emails an invite that lets the user set their own password on
      // first login. Marking the field `required` on the form would
      // block that flow at HTML-validation time, so userModals
      // explicitly sets required=false on openCreateUserModal.
      userModals.openCreateUserModal();

      const passwordInput = document.getElementById('user-password') as HTMLInputElement;
      expect(passwordInput?.required).toBe(false);
    });

    it('should populate groups dropdown', () => {
      userModals.openCreateUserModal();

      const groupsSelect = document.getElementById('user-groups') as HTMLSelectElement;
      expect(groupsSelect.options.length).toBe(2);
    });

    it('should clear current editing user', () => {
      userState.setCurrentEditingUser(mockUser as any);

      userModals.openCreateUserModal();

      expect(userState.currentEditingUser).toBeNull();
    });

    it('should handle missing elements gracefully', () => {
      document.body.innerHTML = '';
      expect(() => userModals.openCreateUserModal()).not.toThrow();
    });
  });

  describe('openEditUserModal', () => {
    it('should open modal for editing user', async () => {
      await userModals.openEditUserModal('1');

      const modal = document.getElementById('user-modal');
      expect(modal?.classList.contains('hidden')).toBe(false);
    });

    it('should set title to Edit User', async () => {
      await userModals.openEditUserModal('1');

      const title = document.getElementById('user-modal-title');
      expect(title?.textContent).toBe('Edit user');
    });

    it('should load user from API', async () => {
      await userModals.openEditUserModal('1');

      expect(api.getUser).toHaveBeenCalledWith('1');
    });

    it('should populate form with user data', async () => {
      await userModals.openEditUserModal('1');

      expect((document.getElementById('user-id') as HTMLInputElement).value).toBe('1');
      expect((document.getElementById('user-email') as HTMLInputElement).value).toBe('test@test.com');
      // role field removed in PR #912
    });

    it('should hide password field when editing', async () => {
      await userModals.openEditUserModal('1');

      const passwordFields = document.getElementById('password-fields');
      expect(passwordFields?.classList.contains('hidden')).toBe(true);
    });

    it('should make password not required when editing', async () => {
      await userModals.openEditUserModal('1');

      const passwordInput = document.getElementById('user-password') as HTMLInputElement;
      expect(passwordInput?.required).toBe(false);
    });

    it('should select user groups in dropdown', async () => {
      await userModals.openEditUserModal('1');

      const groupsSelect = document.getElementById('user-groups') as HTMLSelectElement;
      const selectedValues = Array.from(groupsSelect.selectedOptions).map(o => o.value);
      expect(selectedValues).toContain('users');
    });

    it('should set current editing user', async () => {
      await userModals.openEditUserModal('1');

      expect(userState.currentEditingUser).toEqual(mockUser);
    });

    it('should handle API error', async () => {
      (api.getUser as jest.Mock).mockRejectedValue(new Error('Not found'));

      await userModals.openEditUserModal('1');

      expect(document.querySelector('.toast-error')).toBeTruthy();
    });

    it('should handle missing elements gracefully', async () => {
      document.body.innerHTML = '';
      await userModals.openEditUserModal('1');
      // Should not throw, just log error
    });
  });

  describe('closeUserModal', () => {
    it('should hide modal', () => {
      const modal = document.getElementById('user-modal');
      modal?.classList.remove('hidden');

      userModals.closeUserModal();

      expect(modal?.classList.contains('hidden')).toBe(true);
    });

    it('should clear current editing user', () => {
      userState.setCurrentEditingUser(mockUser as any);

      userModals.closeUserModal();

      expect(userState.currentEditingUser).toBeNull();
    });

    it('should handle missing modal gracefully', () => {
      document.body.innerHTML = '';
      expect(() => userModals.closeUserModal()).not.toThrow();
    });
  });

  describe('saveUser', () => {
    it('should create new user', async () => {
      jest.useFakeTimers();
      userModals.openCreateUserModal();

      (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = 'SecurePass123!';

      // PR #912: >= 1 group required. Select the first option populated by openCreateUserModal.
      const gs7 = document.getElementById('user-groups') as HTMLSelectElement;
      if (gs7.options.length > 0) gs7.options[0]!.selected = true;

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(api.createUser).toHaveBeenCalledWith(
        expect.objectContaining({
          email: 'new@test.com',
          password: 'SecurePass123!',
        })
      );
      jest.useRealTimers();
    });

    it('should update existing user', async () => {
      jest.useFakeTimers();
      await userModals.openEditUserModal('1');

      (document.getElementById('user-email') as HTMLInputElement).value = 'updated@test.com';
      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(api.updateUser).toHaveBeenCalledWith('1', {
        email: 'updated@test.com',
        // role removed: PR #912
        groups: ['users']
      });
      jest.useRealTimers();
    });

    it('should prevent default form submission', async () => {
      const event = new Event('submit');
      event.preventDefault = jest.fn();

      userModals.openCreateUserModal();
      (document.getElementById('user-email') as HTMLInputElement).value = 'test@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = 'SecurePass123!';

      await userModals.saveUser(event);

      expect(event.preventDefault).toHaveBeenCalled();
    });

    it('should validate password length for new user', async () => {
      userModals.openCreateUserModal();

      (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = 'short';

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(api.createUser).not.toHaveBeenCalled();
      expect(document.querySelector('.toast-error')).toBeTruthy();
    });

    it('should allow empty password for new user (invite flow, issue #348)', async () => {
      // Empty password used to be a validation error; after issue #348
      // it triggers the invite flow -- createUser is called with an
      // empty password and the backend emails a set-password link.
      userModals.openCreateUserModal();

      (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = '';

      // PR #912: >= 1 group required. Select first option so validation passes.
      const gs5 = document.getElementById('user-groups') as HTMLSelectElement;
      if (gs5.options.length > 0) gs5.options[0]!.selected = true;

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(api.createUser).toHaveBeenCalled();
      const callArg = (api.createUser as jest.Mock).mock.calls[0][0];
      expect(callArg.email).toBe('new@test.com');
      expect(callArg.password).toBe('');
      // No error toast on the invite-path success.
      expect(document.querySelector('.toast-error')).toBeNull();
    });


    it('should reject save when zero groups selected (PR #912 required-group validation)', async () => {
      userModals.openCreateUserModal();

      (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = 'SecurePass123!';

      // Ensure no groups are selected
      const groupsSelect = document.getElementById('user-groups') as HTMLSelectElement;
      Array.from(groupsSelect.options).forEach(o => { o.selected = false; });

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      // Backend would reject with 400; frontend should show a clear validation message.
      expect(api.createUser).not.toHaveBeenCalled();
      expect(document.querySelector('.toast-error')).toBeTruthy();
    });
    it('should close modal after save', async () => {
      jest.useFakeTimers();
      userModals.openCreateUserModal();

      (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = 'SecurePass123!';

      const gs2 = document.getElementById('user-groups') as HTMLSelectElement;
      if (gs2.options.length > 0) gs2.options[0]!.selected = true;

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      const modal = document.getElementById('user-modal');
      expect(modal?.classList.contains('hidden')).toBe(true);
      jest.useRealTimers();
    });

    it('should reload users after save', async () => {
      jest.useFakeTimers();
      userModals.openCreateUserModal();

      (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = 'SecurePass123!';

      const gs3 = document.getElementById('user-groups') as HTMLSelectElement;
      if (gs3.options.length > 0) gs3.options[0]!.selected = true;

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(api.listUsers).toHaveBeenCalled();
      jest.useRealTimers();
    });

    it('should show success message on create', async () => {
      jest.useFakeTimers();
      userModals.openCreateUserModal();

      (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = 'SecurePass123!';

      const gs4 = document.getElementById('user-groups') as HTMLSelectElement;
      if (gs4.options.length > 0) gs4.options[0]!.selected = true;

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(document.querySelector('.toast-success')).toBeTruthy();
      jest.useRealTimers();
    });

    it('should show success message on update', async () => {
      jest.useFakeTimers();
      await userModals.openEditUserModal('1');

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(document.querySelector('.toast-success')).toBeTruthy();
      jest.useRealTimers();
    });

    it('should handle save error', async () => {
      (api.createUser as jest.Mock).mockRejectedValue(new Error('Create failed'));

      userModals.openCreateUserModal();

      (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = 'SecurePass123!';

      // PR #912: select a group so the required-group check passes and
      // the error comes from the API call, not the validation.
      const gs6 = document.getElementById('user-groups') as HTMLSelectElement;
      if (gs6.options.length > 0) gs6.options[0]!.selected = true;

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(document.querySelector('.toast-error')).toBeTruthy();
      expect(document.querySelector('.toast-error')?.textContent).toContain('Create failed');
    });

    it('should include selected groups', async () => {
      jest.useFakeTimers();
      userModals.openCreateUserModal();

      (document.getElementById('user-email') as HTMLInputElement).value = 'new@test.com';
      (document.getElementById('user-password') as HTMLInputElement).value = 'SecurePass123!';

      const groupsSelect = document.getElementById('user-groups') as HTMLSelectElement;
      if (groupsSelect.options[0]) {
        groupsSelect.options[0].selected = true; // Select 'admins'
      }

      const event = new Event('submit');
      event.preventDefault = jest.fn();

      await userModals.saveUser(event);

      expect(api.createUser).toHaveBeenCalledWith(
        expect.objectContaining({
          groups: ['admins']
        })
      );
      jest.useRealTimers();
    });
  });
});
