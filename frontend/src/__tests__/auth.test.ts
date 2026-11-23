/**
 * Auth module tests
 */
// The harness registers the module mocks, so it must be imported first.
import { mockShowToast } from './auth-harness';
import { showLoginModal, updateUserUI } from '../auth';
import { reloadPage } from '../utils';
import * as api from '../api';
import * as state from '../state';
import { flushPromises } from './flush-promises';

// Module-level mock time that persists across tests to ensure
// each test gets a time later than any previous rate limit timestamp
let globalMockTime = 1000000000000;

describe('Auth Module', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    jest.clearAllMocks();
    // Advance the global mock time for each test to bypass rate limiting
    globalMockTime += 100000;
    let mockTime = globalMockTime;
    jest.spyOn(Date, 'now').mockImplementation(() => {
      mockTime += 10000; // Advance 10 seconds each call
      return mockTime;
    });
    window.alert = jest.fn();
  });

  afterEach(() => {
    // Restore Date.now
    jest.spyOn(Date, 'now').mockRestore();
  });

  describe('showLoginModal', () => {
    test('creates login modal', async () => {
      await showLoginModal();

      const modal = document.getElementById('login-modal');
      expect(modal).toBeTruthy();
    });

    test('has login form', async () => {
      await showLoginModal();

      const loginForm = document.getElementById('login-form');
      expect(loginForm).toBeTruthy();
    });

    test('has email and password fields', async () => {
      await showLoginModal();

      const emailInput = document.getElementById('login-email');
      const passwordInput = document.getElementById('login-password');
      expect(emailInput).toBeTruthy();
      expect(passwordInput).toBeTruthy();
    });

    test('has forgot password link', async () => {
      await showLoginModal();

      const forgotLink = document.getElementById('forgot-password-link');
      expect(forgotLink).toBeTruthy();
    });

    test('handles login form submission', async () => {
      (api.login as jest.Mock).mockResolvedValue({});
      await showLoginModal();

      const emailInput = document.getElementById('login-email') as HTMLInputElement;
      const passwordInput = document.getElementById('login-password') as HTMLInputElement;
      const loginForm = document.getElementById('login-form');

      emailInput.value = 'test@example.com';
      passwordInput.value = 'password123';

      loginForm?.dispatchEvent(new Event('submit', { cancelable: true }));

      await flushPromises();

      expect(api.login).toHaveBeenCalledWith('test@example.com', 'password123');
    });

    test('displays login error on failure', async () => {
      (api.login as jest.Mock).mockRejectedValue(new Error('Invalid credentials'));
      await showLoginModal();

      const emailInput = document.getElementById('login-email') as HTMLInputElement;
      const passwordInput = document.getElementById('login-password') as HTMLInputElement;
      const loginForm = document.getElementById('login-form');

      emailInput.value = 'test@example.com';
      passwordInput.value = 'wrongpassword';

      loginForm?.dispatchEvent(new Event('submit', { cancelable: true }));

      await flushPromises();

      const errorDiv = document.getElementById('login-error');
      expect(errorDiv?.classList.contains('hidden')).toBe(false);
      expect(errorDiv?.textContent).toBe('Invalid credentials');
    });

    // Pre-flight + server-error mapping tests (issues #455 and #456).
    // Helper: submit the login form with the given values and return the
    // resulting error-div text. Uses the same async settle pattern as the
    // surrounding tests.
    async function submitLogin(email: string, password: string): Promise<string | null | undefined> {
      const emailInput = document.getElementById('login-email') as HTMLInputElement;
      const passwordInput = document.getElementById('login-password') as HTMLInputElement;
      const loginForm = document.getElementById('login-form');
      emailInput.value = email;
      passwordInput.value = password;
      loginForm?.dispatchEvent(new Event('submit', { cancelable: true }));
      await flushPromises();
      const errorDiv = document.getElementById('login-error');
      return errorDiv?.textContent;
    }

    test('empty email shows "Enter email address" without calling api.login', async () => {
      await showLoginModal();
      const text = await submitLogin('', 'somepassword');
      expect(text).toBe('Enter email address');
      expect(api.login).not.toHaveBeenCalled();
    });

    test('empty password shows "Enter password" without calling api.login', async () => {
      await showLoginModal();
      const text = await submitLogin('user@example.com', '');
      expect(text).toBe('Enter password');
      expect(api.login).not.toHaveBeenCalled();
    });

    test('both empty shows single "Enter email and password" message', async () => {
      await showLoginModal();
      const text = await submitLogin('', '');
      expect(text).toBe('Enter email and password');
      expect(api.login).not.toHaveBeenCalled();
    });

    test('malformed email shows "Incorrect email format" without calling api.login', async () => {
      await showLoginModal();
      const text = await submitLogin('not-an-email', 'somepassword');
      expect(text).toBe('Incorrect email format');
      expect(api.login).not.toHaveBeenCalled();
    });

    test('backend "authentication failed" maps to softened copy', async () => {
      (api.login as jest.Mock).mockRejectedValue(new Error('authentication failed'));
      await showLoginModal();
      const text = await submitLogin('user@example.com', 'wrongpassword');
      expect(text).toBe('Check your email address and password and try again');
    });

    test('backend "Check your email address and password and try again" passes through unchanged', async () => {
      (api.login as jest.Mock).mockRejectedValue(
        new Error('Check your email address and password and try again'),
      );
      await showLoginModal();
      const text = await submitLogin('user@example.com', 'wrongpassword');
      expect(text).toBe('Check your email address and password and try again');
    });

    test('backend "invalid email format" maps to "Incorrect email format"', async () => {
      (api.login as jest.Mock).mockRejectedValue(new Error('invalid email format'));
      await showLoginModal();
      // Bypass client-side check with an email that passes the regex but
      // that the backend would reject (e.g. extra-strict server policy).
      const text = await submitLogin('shape-ok@example.com', 'somepassword');
      expect(text).toBe('Incorrect email format');
    });

    test('unknown backend error passes through unchanged (e.g. MFA prompt)', async () => {
      (api.login as jest.Mock).mockRejectedValue(new Error('MFA code required'));
      await showLoginModal();
      const text = await submitLogin('user@example.com', 'somepassword');
      expect(text).toBe('MFA code required');
    });

    test('non-Error rejection (string) is handled defensively', async () => {
      // Guards against a non-Error rejection causing undefined.toLowerCase()
      // inside the server-error mapper.
      (api.login as jest.Mock).mockImplementation(() => Promise.reject('boom'));
      await showLoginModal();
      const text = await submitLogin('user@example.com', 'somepassword');
      expect(text).toBe('boom');
    });

    test('non-Error rejection (plain object) is handled defensively', async () => {
      (api.login as jest.Mock).mockImplementation(() => Promise.reject({ code: 500 }));
      await showLoginModal();
      const text = await submitLogin('user@example.com', 'somepassword');
      // String({...}) returns "[object Object]" — the exact stringification is
      // less important than the fact that no exception is thrown and the
      // login-error div is populated with something.
      expect(text).toBe('[object Object]');
    });

    test('reloads page after successful login', async () => {
      (api.login as jest.Mock).mockResolvedValue({});
      await showLoginModal();

      const emailInput = document.getElementById('login-email') as HTMLInputElement;
      const passwordInput = document.getElementById('login-password') as HTMLInputElement;
      const loginForm = document.getElementById('login-form');

      emailInput.value = 'test@example.com';
      passwordInput.value = 'password123';

      loginForm?.dispatchEvent(new Event('submit', { cancelable: true }));

      await flushPromises();

      expect(reloadPage).toHaveBeenCalled();
    });

    test('removes existing modal before creating new one', async () => {
      await showLoginModal();
      const firstModal = document.getElementById('login-modal');

      await showLoginModal();
      const secondModal = document.getElementById('login-modal');

      expect(firstModal).not.toBe(secondModal);
      expect(document.querySelectorAll('#login-modal').length).toBe(1);
    });

    test('forgot password link shows reset form', async () => {
      await showLoginModal();

      const forgotLink = document.getElementById('forgot-password-link');
      forgotLink?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));

      await flushPromises();

      const resetEmail = document.getElementById('reset-email');
      const sendBtn = document.getElementById('send-reset-btn');
      expect(resetEmail).toBeTruthy();
      expect(sendBtn).toBeTruthy();
    });

    test('submitting the reset form (Enter) sends the reset, not a login error (issue #733)', async () => {
      (api.requestPasswordReset as jest.Mock).mockResolvedValue({});
      await showLoginModal();
      document.getElementById('forgot-password-link')
        ?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
      await flushPromises();

      (document.getElementById('reset-email') as HTMLInputElement).value = 'test@example.com';
      document.getElementById('login-form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await flushPromises();

      expect(api.requestPasswordReset).toHaveBeenCalledWith('test@example.com');
      expect(api.login).not.toHaveBeenCalled();
      expect(document.body.textContent).not.toContain('Enter email and password');
    });

    test('Enter in the reset form never runs the login handler (issue #733)', async () => {
      await showLoginModal();
      document.getElementById('forgot-password-link')
        ?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
      await flushPromises();

      // Both handlers write synchronously and the reset handler writes last,
      // so a stale login handler is only visible in the mutation records.
      const writes: string[] = [];
      const observer = new MutationObserver(records => {
        for (const r of records) r.addedNodes.forEach(n => writes.push(n.textContent ?? ''));
      });
      observer.observe(document.getElementById('login-modal')!, { childList: true, characterData: true, subtree: true });
      document.getElementById('login-form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await flushPromises();
      observer.disconnect();

      expect(writes.some(text => /Enter email|Enter password/.test(text))).toBe(false);
      expect(api.login).not.toHaveBeenCalled();
    });

    test('password reset swaps modal body to confirmation panel (issue #457)', async () => {
      (api.requestPasswordReset as jest.Mock).mockResolvedValue({});
      await showLoginModal();

      const forgotLink = document.getElementById('forgot-password-link');
      forgotLink?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));

      await flushPromises();

      const resetEmail = document.getElementById('reset-email') as HTMLInputElement;
      const sendBtn = document.getElementById('send-reset-btn');

      resetEmail.value = 'test@example.com';
      sendBtn?.click();

      await flushPromises();

      expect(api.requestPasswordReset).toHaveBeenCalledWith('test@example.com');
      // No alert(): the lingering-modal bug came from stacking an
      // alert on top of the modal; we now swap the modal body in place.
      expect(window.alert).not.toHaveBeenCalled();
      // Modal still in DOM with a confirmation panel, NOT the email form.
      const modal = document.getElementById('login-modal');
      expect(modal).toBeTruthy();
      expect(document.getElementById('reset-email')).toBeNull();
      expect(document.getElementById('send-reset-btn')).toBeNull();
      const closeBtn = document.getElementById('reset-confirmation-close');
      expect(closeBtn).toBeTruthy();
      expect(modal?.textContent).toContain('Check your email');
    });

    test('confirmation Close button reloads to return to login (issue #457)', async () => {
      (api.requestPasswordReset as jest.Mock).mockResolvedValue({});
      await showLoginModal();

      const forgotLink = document.getElementById('forgot-password-link');
      forgotLink?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
      await flushPromises();

      const resetEmail = document.getElementById('reset-email') as HTMLInputElement;
      resetEmail.value = 'test@example.com';
      document.getElementById('send-reset-btn')?.click();
      await flushPromises();

      document.getElementById('reset-confirmation-close')?.click();

      expect(reloadPage).toHaveBeenCalled();
    });

    test('password reset handles empty email inline (no alert)', async () => {
      await showLoginModal();

      const forgotLink = document.getElementById('forgot-password-link');
      forgotLink?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));

      await flushPromises();

      const sendBtn = document.getElementById('send-reset-btn');
      sendBtn?.click();

      await flushPromises();

      // Issue #457: error is now inline in the modal, not via alert().
      const errorDiv = document.getElementById('login-error');
      expect(errorDiv?.textContent).toBe('Please enter your email address');
      expect(errorDiv?.classList.contains('hidden')).toBe(false);
      expect(window.alert).not.toHaveBeenCalled();
      expect(api.requestPasswordReset).not.toHaveBeenCalled();
    });

    test('password reset handles API error inline (no alert)', async () => {
      (api.requestPasswordReset as jest.Mock).mockRejectedValue(new Error('Network error'));
      console.error = jest.fn();
      await showLoginModal();

      const forgotLink = document.getElementById('forgot-password-link');
      forgotLink?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));

      await flushPromises();

      const resetEmail = document.getElementById('reset-email') as HTMLInputElement;
      const sendBtn = document.getElementById('send-reset-btn');

      resetEmail.value = 'test@example.com';
      sendBtn?.click();

      await flushPromises();

      const errorDiv = document.getElementById('login-error');
      expect(errorDiv?.textContent).toBe('Failed to send reset email. Please try again.');
      expect(errorDiv?.classList.contains('hidden')).toBe(false);
      expect(window.alert).not.toHaveBeenCalled();
    });

    test('back to login link reloads page', async () => {
      await showLoginModal();

      const forgotLink = document.getElementById('forgot-password-link');
      forgotLink?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));

      await flushPromises();

      const backLink = document.getElementById('back-to-login-link');
      backLink?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));

      await flushPromises();

      expect(reloadPage).toHaveBeenCalled();
    });
  });

  describe('profile modal', () => {
    beforeEach(() => {
      document.body.innerHTML = `
        <div id="user-info">
          <span id="user-email-display"></span>
        </div>
      `;
      (state.getCurrentUser as jest.Mock).mockReturnValue({
        id: 'user-1',
        email: 'test@example.com',
        groups: []
      });
    });

    test('clicking email opens profile modal', async () => {
      updateUserUI();

      const userEmail = document.getElementById('user-email-display') as HTMLElement;
      userEmail.click();

      await flushPromises();

      const modal = document.getElementById('profile-modal');
      expect(modal).toBeTruthy();
      expect(modal?.classList.contains('hidden')).toBe(false);
    });

    test('profile modal populates with current user email', async () => {
      updateUserUI();

      const userEmail = document.getElementById('user-email-display') as HTMLElement;
      userEmail.click();

      await flushPromises();

      const emailInput = document.getElementById('profile-email') as HTMLInputElement;
      expect(emailInput.value).toBe('test@example.com');
    });

    test('cancel button closes profile modal', async () => {
      updateUserUI();

      const userEmail = document.getElementById('user-email-display') as HTMLElement;
      userEmail.click();

      await flushPromises();

      const cancelBtn = document.getElementById('profile-cancel');
      cancelBtn?.click();

      const modal = document.getElementById('profile-modal');
      expect(modal?.classList.contains('hidden')).toBe(true);
    });

    test('save profile requires current password', async () => {
      updateUserUI();

      const userEmail = document.getElementById('user-email-display') as HTMLElement;
      userEmail.click();

      await flushPromises();

      const form = document.getElementById('profile-form');
      form?.dispatchEvent(new Event('submit', { cancelable: true }));

      await flushPromises();

      // alert() replaced by showToast() (finding 11-L3).
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Please enter your current password to save changes', kind: 'error' }));
      expect(api.apiRequest).not.toHaveBeenCalled();
    });

    test('save profile validates new password confirmation', async () => {
      updateUserUI();

      const userEmail = document.getElementById('user-email-display') as HTMLElement;
      userEmail.click();

      await flushPromises();

      const currentPasswordInput = document.getElementById('profile-current-password') as HTMLInputElement;
      const newPasswordInput = document.getElementById('profile-new-password') as HTMLInputElement;
      const confirmPasswordInput = document.getElementById('profile-confirm-password') as HTMLInputElement;

      currentPasswordInput.value = 'oldpassword';
      newPasswordInput.value = 'NewPassword123!';
      confirmPasswordInput.value = 'DifferentPass1!';

      const form = document.getElementById('profile-form');
      form?.dispatchEvent(new Event('submit', { cancelable: true }));

      await flushPromises();

      // Password mismatch is surfaced inline (parity with reset / setup
      // flows). `alert` must not fire for this path.
      const errorDiv = document.getElementById('profile-password-error') as HTMLElement;
      expect(errorDiv.textContent).toBe('New passwords do not match');
      expect(errorDiv.classList.contains('hidden')).toBe(false);
      expect(window.alert).not.toHaveBeenCalled();
      expect(api.apiRequest).not.toHaveBeenCalled();
    });

    test('save profile shows inline error when new password fails complexity rules', async () => {
      updateUserUI();

      const userEmail = document.getElementById('user-email-display') as HTMLElement;
      userEmail.click();

      await flushPromises();

      const currentPasswordInput = document.getElementById('profile-current-password') as HTMLInputElement;
      const newPasswordInput = document.getElementById('profile-new-password') as HTMLInputElement;
      const confirmPasswordInput = document.getElementById('profile-confirm-password') as HTMLInputElement;

      currentPasswordInput.value = 'oldpassword';
      // Too short — fails the length rule first.
      newPasswordInput.value = 'short';
      confirmPasswordInput.value = 'short';

      const form = document.getElementById('profile-form');
      form?.dispatchEvent(new Event('submit', { cancelable: true }));

      await flushPromises();

      const errorDiv = document.getElementById('profile-password-error') as HTMLElement;
      expect(errorDiv.textContent).toBe('Password must be at least 12 characters long');
      expect(errorDiv.classList.contains('hidden')).toBe(false);
      expect(window.alert).not.toHaveBeenCalled();
      expect(api.apiRequest).not.toHaveBeenCalled();
    });

    test('profile modal renders live password-requirement indicators', async () => {
      updateUserUI();

      const userEmail = document.getElementById('user-email-display') as HTMLElement;
      userEmail.click();

      await flushPromises();

      // All five indicators must be present and prefixed `profile-req-`
      // so they cannot collide with the reset / setup flows.
      expect(document.getElementById('profile-req-length')).toBeTruthy();
      expect(document.getElementById('profile-req-uppercase')).toBeTruthy();
      expect(document.getElementById('profile-req-lowercase')).toBeTruthy();
      expect(document.getElementById('profile-req-number')).toBeTruthy();
      expect(document.getElementById('profile-req-special')).toBeTruthy();
    });

    test('typing into new password toggles requirement classes live', async () => {
      updateUserUI();

      const userEmail = document.getElementById('user-email-display') as HTMLElement;
      userEmail.click();

      await flushPromises();

      const newPasswordInput = document.getElementById('profile-new-password') as HTMLInputElement;

      // A fully-valid password should mark every rule as met.
      newPasswordInput.value = 'Abcdefghijk1!';
      newPasswordInput.dispatchEvent(new Event('input'));

      expect(document.getElementById('profile-req-length')?.classList.contains('met')).toBe(true);
      expect(document.getElementById('profile-req-uppercase')?.classList.contains('met')).toBe(true);
      expect(document.getElementById('profile-req-lowercase')?.classList.contains('met')).toBe(true);
      expect(document.getElementById('profile-req-number')?.classList.contains('met')).toBe(true);
      expect(document.getElementById('profile-req-special')?.classList.contains('met')).toBe(true);

      // Drop every rule simultaneously — short, no upper, no number, no
      // special. Lowercase is the only remaining match.
      newPasswordInput.value = 'abc';
      newPasswordInput.dispatchEvent(new Event('input'));

      expect(document.getElementById('profile-req-length')?.classList.contains('unmet')).toBe(true);
      expect(document.getElementById('profile-req-uppercase')?.classList.contains('unmet')).toBe(true);
      expect(document.getElementById('profile-req-lowercase')?.classList.contains('met')).toBe(true);
      expect(document.getElementById('profile-req-number')?.classList.contains('unmet')).toBe(true);
      expect(document.getElementById('profile-req-special')?.classList.contains('unmet')).toBe(true);
    });

    test('reopening profile modal resets stale indicators and error', async () => {
      updateUserUI();

      const userEmail = document.getElementById('user-email-display') as HTMLElement;
      userEmail.click();
      await flushPromises();

      // Dirty the indicators and the inline error.
      const newPasswordInput = document.getElementById('profile-new-password') as HTMLInputElement;
      newPasswordInput.value = 'Abcdefghijk1!';
      newPasswordInput.dispatchEvent(new Event('input'));
      const errorDiv = document.getElementById('profile-password-error') as HTMLElement;
      errorDiv.textContent = 'old error';
      errorDiv.classList.remove('hidden');

      // Close and re-open.
      const cancelBtn = document.getElementById('profile-cancel');
      cancelBtn?.click();
      userEmail.click();
      await flushPromises();

      // Re-query after reopen so we assert on the live DOM node, not a
      // potentially stale pre-close reference.
      const reopenedErrorDiv = document.getElementById('profile-password-error') as HTMLElement;

      // Length applies to empty string ("" < 12) so on reset it should
      // be `unmet`, and the inline error must be cleared + hidden.
      // Re-query the error div because the close-and-reopen cycle
      // re-renders the modal; the pre-reopen reference points at the
      // detached element and would silently pass the assertions even
      // if the new modal regressed (CodeRabbit on #470).
      expect(document.getElementById('profile-req-length')?.classList.contains('unmet')).toBe(true);
      expect(document.getElementById('profile-req-uppercase')?.classList.contains('unmet')).toBe(true);
      expect(reopenedErrorDiv.textContent).toBe('');
      expect(reopenedErrorDiv.classList.contains('hidden')).toBe(true);
    });

    test('save profile updates user info on success', async () => {
      (api.apiRequest as jest.Mock).mockResolvedValue({});
      updateUserUI();

      const userEmail = document.getElementById('user-email-display') as HTMLElement;
      userEmail.click();

      await flushPromises();

      const emailInput = document.getElementById('profile-email') as HTMLInputElement;
      const currentPasswordInput = document.getElementById('profile-current-password') as HTMLInputElement;

      emailInput.value = 'newemail@example.com';
      currentPasswordInput.value = 'oldpassword';

      const form = document.getElementById('profile-form');
      form?.dispatchEvent(new Event('submit', { cancelable: true }));

      await flushPromises();

      expect(api.apiRequest).toHaveBeenCalledWith('/auth/profile', expect.objectContaining({
        method: 'PUT',
        body: expect.any(String)
      }));
      expect(state.setCurrentUser).toHaveBeenCalledWith(expect.objectContaining({
        email: 'newemail@example.com'
      }));
      // alert() replaced by showToast() (finding 11-L3).
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Profile updated successfully', kind: 'success' }));
    });

    test('save profile refreshes the avatar initials with the new email', async () => {
      (api.apiRequest as jest.Mock).mockResolvedValue({});
      updateUserUI();

      const userEmail = document.getElementById('user-email-display') as HTMLElement;
      const before = userEmail.dataset['initials'];
      userEmail.click();
      await flushPromises();

      (document.getElementById('profile-email') as HTMLInputElement).value = 'newemail@example.com';
      (document.getElementById('profile-current-password') as HTMLInputElement).value = 'oldpassword';
      document.getElementById('profile-form')?.dispatchEvent(new Event('submit', { cancelable: true }));
      await flushPromises();

      expect(userEmail.textContent).toBe('newemail@example.com');
      expect(userEmail.dataset['initials']).toBe('NE');
      expect(before).not.toBe('NE');
    });

    test('save profile handles API errors', async () => {
      (api.apiRequest as jest.Mock).mockRejectedValue(new Error('Update failed'));
      console.error = jest.fn();
      updateUserUI();

      const userEmail = document.getElementById('user-email-display') as HTMLElement;
      userEmail.click();

      await flushPromises();

      const emailInput = document.getElementById('profile-email') as HTMLInputElement;
      const currentPasswordInput = document.getElementById('profile-current-password') as HTMLInputElement;

      emailInput.value = 'newemail@example.com';
      currentPasswordInput.value = 'oldpassword';

      const form = document.getElementById('profile-form');
      form?.dispatchEvent(new Event('submit', { cancelable: true }));

      await flushPromises();

      // alert() replaced by showToast() (finding 11-L3).
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Failed to update profile: Update failed', kind: 'error' }));
    });

    test('save profile includes new password when provided', async () => {
      (api.apiRequest as jest.Mock).mockResolvedValue({});
      updateUserUI();

      const userEmail = document.getElementById('user-email-display') as HTMLElement;
      userEmail.click();

      await flushPromises();

      const emailInput = document.getElementById('profile-email') as HTMLInputElement;
      const currentPasswordInput = document.getElementById('profile-current-password') as HTMLInputElement;
      const newPasswordInput = document.getElementById('profile-new-password') as HTMLInputElement;
      const confirmPasswordInput = document.getElementById('profile-confirm-password') as HTMLInputElement;

      emailInput.value = 'test@example.com';
      currentPasswordInput.value = 'oldpassword';
      newPasswordInput.value = 'NewPassword123!';
      confirmPasswordInput.value = 'NewPassword123!';

      const form = document.getElementById('profile-form');
      form?.dispatchEvent(new Event('submit', { cancelable: true }));

      await flushPromises();

      const callArgs = (api.apiRequest as jest.Mock).mock.calls[0];
      const bodyData = JSON.parse(callArgs[1].body);
      expect(bodyData.new_password).toBeDefined();
    });
  });

});
