/**
 * Auth module: user UI state, logout and the reset-password modal. Split from
 * auth.test.ts to stay under the 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast } from './auth-harness';
import { showResetPasswordModal, logout } from '../auth';
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

  describe('logout', () => {
    test('calls API logout and clears user state', async () => {
      (api.logout as jest.Mock).mockResolvedValue({});

      await logout();

      expect(api.logout).toHaveBeenCalled();
      expect(state.setCurrentUser).toHaveBeenCalledWith(null);
      expect(reloadPage).toHaveBeenCalled();
    });

    test('a failed API logout keeps the user signed in, toasts and does not reload', async () => {
      (api.logout as jest.Mock).mockRejectedValue(new Error('Sign out failed, please try again.'));

      await logout();

      expect(state.setCurrentUser).not.toHaveBeenCalled();
      expect(reloadPage).not.toHaveBeenCalled();
      expect(mockShowToast).toHaveBeenCalledWith({ message: 'Sign out failed, please try again.', kind: 'error' });
    });
  });

  // Issues #460 and #461: branch the reset modal on token status BEFORE
  // rendering the form, so expired / already-used tokens land on a
  // dedicated UX rather than a form that can never submit.
  describe('showResetPasswordModal', () => {
    test('valid + reset flow renders the form with "Reset Your Password" heading', async () => {
      (api.getResetTokenStatus as jest.Mock).mockResolvedValue({ state: 'valid', flow: 'reset' });

      await showResetPasswordModal('valid-token');

      const modal = document.getElementById('reset-password-modal');
      expect(modal).toBeTruthy();
      expect(modal?.textContent).toContain('Reset Your Password');
      expect(document.getElementById('reset-password-form')).toBeTruthy();
      expect(document.getElementById('new-password')).toBeTruthy();
    });

    test('valid + invite flow uses "Set Your Password" wording (issue #461)', async () => {
      (api.getResetTokenStatus as jest.Mock).mockResolvedValue({ state: 'valid', flow: 'invite' });

      await showResetPasswordModal('valid-invite-token');

      const modal = document.getElementById('reset-password-modal');
      expect(modal).toBeTruthy();
      expect(modal?.textContent).toContain('Set Your Password');
      expect(modal?.textContent).not.toContain('Reset Your Password');
      // Form still renders so the user can complete the invite.
      expect(document.getElementById('reset-password-form')).toBeTruthy();
    });

    test('expired token renders the expired view, not the form (issue #460)', async () => {
      (api.getResetTokenStatus as jest.Mock).mockResolvedValue({ state: 'expired', flow: 'reset' });

      await showResetPasswordModal('expired-token');

      const modal = document.getElementById('reset-password-modal');
      expect(modal).toBeTruthy();
      expect(modal?.textContent).toContain('expired');
      // The password-entry form must NOT render.
      expect(document.getElementById('reset-password-form')).toBeNull();
      expect(document.getElementById('new-password')).toBeNull();
      // The CTA to request a new email is present.
      expect(document.getElementById('reset-expired-request-new')).toBeTruthy();
    });

    test('used token renders the used view, not the form (issue #461)', async () => {
      (api.getResetTokenStatus as jest.Mock).mockResolvedValue({ state: 'used', flow: 'reset' });

      await showResetPasswordModal('stale-token');

      const modal = document.getElementById('reset-password-modal');
      expect(modal).toBeTruthy();
      expect(modal?.textContent).toContain('already been used');
      expect(document.getElementById('reset-password-form')).toBeNull();
      expect(document.getElementById('reset-used-go-to-login')).toBeTruthy();
    });

    test('used + invite flow uses invitation wording (issue #461)', async () => {
      (api.getResetTokenStatus as jest.Mock).mockResolvedValue({ state: 'used', flow: 'invite' });

      await showResetPasswordModal('used-invite-token');

      const modal = document.getElementById('reset-password-modal');
      expect(modal?.textContent).toContain('Invitation link already used');
    });

    test('status-check failure falls back to rendering the form', async () => {
      (api.getResetTokenStatus as jest.Mock).mockRejectedValue(new Error('Network error'));

      await showResetPasswordModal('uncertain-token');

      const modal = document.getElementById('reset-password-modal');
      expect(modal).toBeTruthy();
      // Form renders so the user is not stranded on a transient outage.
      expect(document.getElementById('reset-password-form')).toBeTruthy();
      expect(document.getElementById('new-password')).toBeTruthy();
    });

    test('expired view "Send a new reset email" clears the token and routes to login', async () => {
      (api.getResetTokenStatus as jest.Mock).mockResolvedValue({ state: 'expired', flow: 'reset' });

      // Stub window.history.replaceState since jsdom's implementation
      // does not noop on absent listeners.
      const replaceState = jest.fn();
      Object.defineProperty(window, 'history', {
        writable: true,
        value: { replaceState }
      });

      await showResetPasswordModal('expired-token');
      document.getElementById('reset-expired-request-new')?.click();

      await flushPromises();

      // The reset modal is gone; the login modal is up.
      expect(document.getElementById('reset-password-modal')).toBeNull();
      expect(document.getElementById('login-modal')).toBeTruthy();
      // URL query string is cleaned so a reload does not re-enter the
      // reset flow with the stale token.
      expect(replaceState).toHaveBeenCalled();
    });
  });
});
