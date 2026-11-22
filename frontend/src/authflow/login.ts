/**
 * Login modal, pending-MFA step and forgot-password form.
 */

import * as api from '../api';
import { reloadPage } from '../utils';
import { openAuthDialog } from '../authDialog';
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
function validateLoginInputs(email: string, password: string): string | null {
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
function mapServerLoginError(message: string): string {
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

function showLoginError(message: string): void {
  const errorDiv = document.getElementById('login-error');
  if (errorDiv) {
    errorDiv.textContent = message;
    errorDiv.classList.remove('hidden');
  }
}

// Login rate limiting
let lastLoginAttempt = 0;
const LOGIN_COOLDOWN_MS = 2000; // 2 seconds between attempts

// Pending-MFA closure (issue #497). When the server responds with
// `mfa_required` on the first POST /api/auth/login, we keep the
// email + password in memory ONLY for the resubmit. Cleared on
// success, cancel, any terminal error, or after MFA_PENDING_TTL_MS so an
// abandoned prompt does not keep the plaintext password reachable for the
// rest of the page's life. Never persisted.
let pendingMFAEmail = '';
let pendingMFAPassword = '';
// Server-issued continuation token; when present the password is never kept.
let pendingMFAToken = '';
let pendingMFATimer: ReturnType<typeof setTimeout> | undefined;
export const MFA_PENDING_TTL_MS = 5 * 60 * 1000;

function clearPendingMFA(): void {
  pendingMFAEmail = '';
  pendingMFAPassword = '';
  pendingMFAToken = '';
  if (pendingMFATimer !== undefined) {
    clearTimeout(pendingMFATimer);
    pendingMFATimer = undefined;
  }
}

function stashPendingMFA(email: string, password: string, mfaToken?: string): void {
  clearPendingMFA();
  pendingMFAEmail = email;
  if (mfaToken) {
    pendingMFAToken = mfaToken;
  } else {
    pendingMFAPassword = password;
  }
  pendingMFATimer = setTimeout(() => {
    pendingMFATimer = undefined;
    if (document.getElementById('mfa-code')) {
      restartLogin('The two-factor prompt expired. Log in again.');
    } else {
      clearPendingMFA();
    }
  }, MFA_PENDING_TTL_MS);
}

/** Drop the pending credentials and return to the email/password step. */
function restartLogin(notice?: string): void {
  clearPendingMFA();
  document.getElementById('login-modal')?.remove();
  void showLoginModal(notice);
}

async function handleLogin(e: Event): Promise<void> {
  e.preventDefault();

  const emailInput = document.getElementById('login-email') as HTMLInputElement | null;
  const passwordInput = document.getElementById('login-password') as HTMLInputElement | null;
  const email = emailInput?.value.trim() || '';
  const password = passwordInput?.value || '';

  // Client-side pre-flight (issues #455 + #456). Runs before the rate-limit
  // check so an accidental click on an empty form does not burn the
  // cooldown window the user needs for their real attempt.
  const preflightError = validateLoginInputs(email, password);
  if (preflightError !== null) {
    showLoginError(preflightError);
    return;
  }

  // Rate limiting check
  const now = Date.now();
  if (now - lastLoginAttempt < LOGIN_COOLDOWN_MS) {
    showLoginError('Please wait before trying again');
    return;
  }
  lastLoginAttempt = now;

  document.getElementById('login-error')?.classList.add('hidden');

  try {
    await api.login(email, password);
    clearPendingMFA();
    document.getElementById('login-modal')?.remove();
    reloadPage();
  } catch (error) {
    if (error instanceof api.MFALoginError) {
      // First-leg success: password is correct, server wants the
      // TOTP code. Stash credentials in the closure and swap the
      // form to the MFA code prompt.
      stashPendingMFA(email, password, error.mfaToken);
      showMFACodeStep();
      return;
    }
    // `error` is `unknown` per TS strict catch typing. Extract a string
    // defensively so a non-Error rejection (e.g. a thrown string or a
    // plain object) does not produce `undefined.toLowerCase()` inside
    // `mapServerLoginError`.
    const message = error instanceof Error ? error.message : String(error);
    showLoginError(mapServerLoginError(message));
  }
}

/**
 * Render the MFA code prompt as the second step of the login flow.
 * Reuses the existing #login-form container so the rest of the
 * modal chrome (title, layout) stays put. The single 6-char input
 * uses `inputmode="numeric"` + `autocomplete="one-time-code"` so
 * iOS / Android autofill from SMS-like prompts works (issue #497).
 */
function showMFACodeStep(): void {
  const form = document.getElementById('login-form');
  if (!form) return;
  form.innerHTML = `
    <h3>Two-factor code</h3>
    <p class="help-text">Enter the 6-digit code from your authenticator app, or a recovery code.</p>
    <label>Code:
      <input
        type="text"
        id="mfa-code"
        inputmode="numeric"
        autocomplete="one-time-code"
        pattern="[0-9A-Za-z\\-]{6,16}"
        maxlength="16"
        class="code-input"
        autofocus
      >
    </label>
    <div id="login-error" class="error-message hidden" role="alert"></div>
    <button type="submit" class="primary" id="mfa-submit-btn">Verify</button>
    <p><a href="#" id="mfa-cancel-link">Back to login</a></p>
  `;
  document.getElementById('mfa-cancel-link')?.addEventListener('click', (e) => {
    e.preventDefault();
    // Easiest re-render: tear down and re-open the modal.
    restartLogin();
  });
  // Re-bind submit to handleMFACodeSubmit (the form's submit
  // listener attached to handleLogin still fires; replace the form's
  // listener via a fresh handler).
  form.removeEventListener('submit', handleLoginSubmitListener);
  form.addEventListener('submit', handleMFACodeSubmitListener);
  // The swap destroyed the focused field; keep focus inside the dialog (#731).
  document.getElementById('mfa-code')?.focus();
}

// Stored listener references so we can swap them on the form
// without losing reference identity. Function-expression form keeps
// each addEventListener / removeEventListener call referencing the
// same Function — arrow IIFEs would create a new function each time.
const handleLoginSubmitListener = (e: Event): void => { void handleLogin(e); };
const handleMFACodeSubmitListener = (e: Event): void => { void handleMFACodeSubmit(e); };
const handlePasswordResetSubmitListener = (e: Event): void => {
  e.preventDefault();
  void handlePasswordReset();
};

async function handleMFACodeSubmit(e: Event): Promise<void> {
  e.preventDefault();
  const codeInput = document.getElementById('mfa-code') as HTMLInputElement | null;
  const code = codeInput?.value.trim() || '';
  if (code === '') {
    showLoginError('Enter your authenticator code');
    return;
  }
  document.getElementById('login-error')?.classList.add('hidden');
  try {
    await (pendingMFAToken
      ? api.loginWithMFAToken(pendingMFAEmail, pendingMFAToken, code)
      : api.login(pendingMFAEmail, pendingMFAPassword, code));
    clearPendingMFA();
    document.getElementById('login-modal')?.remove();
    reloadPage();
  } catch (error) {
    if (error instanceof api.MFALoginError && error.code === 'invalid_mfa_code') {
      // Wrong code — stay on the MFA step, surface a specific error.
      showLoginError('Incorrect code, try again');
      if (codeInput) { codeInput.value = ''; codeInput.focus(); }
      return;
    }
    if (error instanceof api.MFALoginError && error.code === 'mfa_required') {
      // Shouldn't reach here (we sent a code), but defensively stay.
      showLoginError('Two-factor code required');
      return;
    }
    // Any other failure ends this attempt: a resubmit cannot succeed
    // without the password, so drop it and restart at the first step.
    const message = error instanceof Error ? error.message : String(error);
    restartLogin(mapServerLoginError(message));
  }
}

function showForgotPasswordForm(modal: HTMLElement): void {
  const form = modal.querySelector('#login-form');
  if (!form) return;

  form.innerHTML = `
    <h3>Reset Password</h3>
    <label>Email:
      <input type="email" id="reset-email" placeholder="Your email address" autocomplete="email" autocapitalize="none" autocorrect="off" required>
    </label>
    <p class="help-text">We'll send you a link to reset your password.</p>
    <div id="login-error" class="error-message hidden" role="alert"></div>
    <button type="submit" id="send-reset-btn" class="primary">Send Reset Link</button>
    <p><a href="#" id="back-to-login-link">Back to login</a></p>
  `;

  // Keep the inline "enter your email" message instead of the native bubble.
  (form as HTMLFormElement).noValidate = true;
  form.removeEventListener('submit', handleLoginSubmitListener);
  form.addEventListener('submit', handlePasswordResetSubmitListener);
  document.getElementById('back-to-login-link')?.addEventListener('click', (e) => {
    e.preventDefault();
    reloadPage();
  });
  // The swap destroyed the focused link; keep focus inside the dialog (#731).
  document.getElementById('reset-email')?.focus();
}

async function handlePasswordReset(): Promise<void> {
  const emailInput = document.getElementById('reset-email') as HTMLInputElement | null;
  const errorDiv = document.getElementById('login-error');
  const email = emailInput?.value.trim() || '';

  // Reset any previous error before validating.
  errorDiv?.classList.add('hidden');

  if (!email) {
    if (errorDiv) {
      errorDiv.textContent = 'Please enter your email address';
      errorDiv.classList.remove('hidden');
    }
    return;
  }

  const sendBtn = document.getElementById('send-reset-btn') as HTMLButtonElement | null;
  if (sendBtn) sendBtn.disabled = true;
  try {
    await api.requestPasswordReset(email);
    // Issue #457: previously this surfaced as an alert() that, when
    // closed, left the underlying modal open with nothing for the user
    // to do. Swap the modal body in place with a confirmation panel +
    // single Close button so the user has one explicit exit that
    // returns them to login (option 2 from the issue, agreed cleaner).
    showResetEmailConfirmation();
  } catch (error) {
    console.error('Password reset error:', error);
    if (errorDiv) {
      errorDiv.textContent = 'Failed to send reset email. Please try again.';
      errorDiv.classList.remove('hidden');
    }
    if (sendBtn) sendBtn.disabled = false;
  }
}

// showResetEmailConfirmation swaps the inner #login-form body with a
// confirmation panel after a successful forgot-password submission.
// The wrapping #login-modal stays in the DOM so the user has a single
// explicit Close action; fixes issue #457 (lingering modal after the
// confirmation pop-up closed).
function showResetEmailConfirmation(): void {
  const form = document.querySelector('#login-modal #login-form');
  if (!form) return;

  // Static template, no user-controlled interpolation; same pattern
  // the surrounding modal code uses for every panel.
  form.innerHTML = `
    <h3>Check your email</h3>
    <p>If an account exists with that email, you will receive a password reset link shortly.</p>
    <p class="help-text">The link is valid for one hour. If you don't see it, check your spam folder.</p>
    <button type="button" id="reset-confirmation-close" class="primary">Close</button>
  `;

  document.getElementById('reset-confirmation-close')?.addEventListener('click', () => {
    // Reloading returns the user to the login modal in a clean state
    // (same exit-path as the existing back-to-login-link).
    reloadPage();
  });
  document.getElementById('reset-confirmation-close')?.focus();
}
