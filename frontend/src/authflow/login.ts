/**
 * Login modal, pending-MFA step and forgot-password form.
 */

import { openAuthDialog } from '../authDialog';
import { handleLoginSubmitListener, showForgotPasswordForm } from '../auth';
import { setupPasswordToggle } from './password';

/**
 * Show login modal. `notice`, when given, is shown in the form's error line
 * on open (e.g. the API could not be reached during startup).
 */
export async function showLoginModal(notice?: string): Promise<void> {
  // Remove any existing login modal to prevent duplicates
  document.getElementById('login-modal')?.remove();

  const modal = document.createElement('div');
  modal.id = 'login-modal';
  modal.innerHTML = `
    <div class="modal-overlay">
      <div class="modal-content">
        <h2>CUDly Login</h2>

        <form id="login-form">
          <label>Email:
            <input type="email" id="login-email" placeholder="admin@example.com" autocomplete="email" autocapitalize="none" autocorrect="off">
          </label>
          <label>Password:
            <div class="password-input-wrapper">
              <input type="password" id="login-password" placeholder="Password" autocomplete="current-password">
              <button type="button" class="toggle-password" data-target="login-password" aria-label="Show password">
                <svg class="eye-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
                  <circle cx="12" cy="12" r="3"></circle>
                </svg>
              </button>
            </div>
          </label>
          <p class="help-text"><a href="#" id="forgot-password-link">Forgot password?</a></p>

          <div id="login-error" class="error-message hidden" role="alert"></div>
          <button type="submit" class="primary">Login</button>
        </form>
      </div>
    </div>
  `;
  document.body.appendChild(modal);

  setupLoginModalHandlers(modal);
  openAuthDialog(modal, '#login-email');
  if (notice) showLoginError(notice);
}

function setupLoginModalHandlers(modal: HTMLElement): void {
  // Forgot password link
  const forgotLink = document.getElementById('forgot-password-link');
  if (forgotLink) {
    forgotLink.addEventListener('click', (e) => {
      e.preventDefault();
      showForgotPasswordForm(modal);
    });
  }

  // Login form submission. Reuse the named listener constant so the
  // MFA two-step swap (issue #497) can swap to handleMFACodeSubmitListener
  // by reference without leaking the original closure.
  const loginForm = document.getElementById('login-form');
  if (loginForm) {
    loginForm.addEventListener('submit', handleLoginSubmitListener);
  }

  // Setup password toggle
  setupPasswordToggle(modal);
}

// Basic email shape check used to short-circuit obviously-malformed input
// client-side before sending it to the server. Intentionally permissive: it
// only catches "obviously not an email" (missing @, missing TLD, whitespace).
// The backend's regex remains the authoritative validator
// (internal/api/validation.go validateEmailFormat); keep the two in sync.
const EMAIL_SHAPE_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

/**
 * Pre-flight validation for the login form. Returns a user-facing message
 * naming the specific problem, or null if the inputs are submittable.
 *
 * Issue #455: empty fields previously produced "Invalid email format" or
 * "Invalid email or password" which hid which field was actually blank.
 * Issue #456 (case 3.3): malformed-email + valid-password previously came
 * back from the server as "authentication failed", which made it look like
 * a credential problem rather than a typo in the email.
 */
export function validateLoginInputs(email: string, password: string): string | null {
  if (email === '' && password === '') {
    return 'Enter email and password';
  }
  if (email === '') {
    return 'Enter email address';
  }
  if (password === '') {
    return 'Enter password';
  }
  if (!EMAIL_SHAPE_RE.test(email)) {
    return 'Incorrect email format';
  }
  return null;
}

/**
 * Translate the known generic backend error strings into clearer
 * user-facing copy. The mapping deliberately collapses both
 * "authentication failed" (user not found) and "check your email address
 * and password" (wrong password / inactive / locked) into the same
 * client-side message so we do not regress the account-enumeration
 * mitigation called out in #456 / #550.
 * Anything else (MFA prompts, rate-limit, server errors) passes through
 * unchanged so operational signals are not suppressed.
 */
export function mapServerLoginError(message: string): string {
  const lower = message.toLowerCase();
  if (lower.includes('invalid email format')) {
    return 'Incorrect email format';
  }
  if (
    lower.includes('authentication failed') ||
    lower.includes('check your email address and password')
  ) {
    return 'Check your email address and password and try again';
  }
  if (lower === 'invalid_mfa_token') {
    return 'The two-factor prompt expired. Log in again.';
  }
  return message;
}

export function showLoginError(message: string): void {
  const errorDiv = document.getElementById('login-error');
  if (errorDiv) {
    errorDiv.textContent = message;
    errorDiv.classList.remove('hidden');
  }
}
