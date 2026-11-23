/**
 * Auth module: login modal behaviour (modal lifecycle, the forgot-password
 * reset form and its confirmation panel, login error mapping). Split from
 * auth.test.ts to stay under the 500-line ceiling. The showLoginModal
 * describe is re-opened here; it holds no hooks of its own.
 */

// The harness registers the module mocks, so it must be imported first.
import './auth-harness';
import { showLoginModal } from '../auth';
import { reloadPage } from '../utils';
import * as api from '../api';
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
});
