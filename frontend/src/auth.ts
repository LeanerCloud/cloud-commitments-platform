/**
 * Authentication module for CUDly
 */

import * as api from './api';
import * as state from './state';
import { escapeHtml, reloadPage } from './utils';
import { openModal, closeModal } from './modal';
import { openAuthDialog } from './authDialog';
import { canAccess, isAdmin as permissionsIsAdmin } from './permissions';
import { showToast } from './toast';
import { clearMFAFlowError, getMFAFlowHost, renderMFAEnrollCodesStep, renderMFASection, setMFAFlowError, setProfileSecretPending } from './authflow/mfa';
import { describePasswordValidationError, setupPasswordToggle, updatePasswordRequirements } from './authflow/password';

// Login rate limiting
let lastLoginAttempt = 0;
const LOGIN_COOLDOWN_MS = 2000; // 2 seconds between attempts

/**
 * Check if current user is admin. Re-exported from `permissions.ts`
 * so callers that already import from `auth` keep compiling while the
 * canonical definition lives alongside `canAccess` (issue #365).
 */
export function isAdmin(): boolean {
  return permissionsIsAdmin();
}

/**
 * Show reset password modal (for password reset links).
 *
 * Probes the token state first (issues #460, #461) so the user lands
 * on the right view: a form for valid tokens, an "expired link" view
 * for expired tokens, an "already used" view for stale/consumed
 * tokens. On a status-check failure the form is rendered as a
 * fallback so the user still has a path forward; submit-time
 * validation still catches bad tokens server-side.
 */
export async function showResetPasswordModal(token: string): Promise<void> {
  // Remove any existing modal to prevent duplicates
  document.getElementById('reset-password-modal')?.remove();

  const modal = document.createElement('div');
  modal.id = 'reset-password-modal';
  document.body.appendChild(modal);

  let status: { state: string; flow: string };
  try {
    status = await api.getResetTokenStatus(token);
  } catch {
    // Fallback: render the form unconditionally so an offline
    // status endpoint does not strand users who have a valid token.
    renderResetForm(modal, token, 'reset');
    return;
  }

  if (status.state === 'expired') {
    renderExpiredView(modal, status.flow);
    return;
  }
  if (status.state === 'used') {
    renderUsedView(modal, status.flow);
    return;
  }
  // 'valid' (or any unexpected state defaults to the form path).
  renderResetForm(modal, token, status.flow);
}

// renderResetForm builds the password-entry form. Heading/submit copy
// flips between "Reset" and "Set" based on flow (issue #461 invite
// path). Static template, no user-controlled interpolation.
function renderResetForm(modal: HTMLElement, token: string, flow: string): void {
  const heading = flow === 'invite' ? 'Set Your Password' : 'Reset Your Password';
  const submitLabel = flow === 'invite' ? 'Set Password' : 'Reset Password';
  modal.innerHTML = `
    <div class="modal-overlay">
      <div class="modal-content">
        <h2>${heading}</h2>

        <form id="reset-password-form">
          <label>New Password:
            <div class="password-input-wrapper">
              <input type="password" id="new-password" placeholder="Enter new password" autocomplete="new-password" required minlength="12">
              <button type="button" class="toggle-password" data-target="new-password" aria-label="Show password">
                <svg class="eye-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
                  <circle cx="12" cy="12" r="3"></circle>
                </svg>
              </button>
            </div>
          </label>

          <div id="password-requirements" class="password-requirements">
            <div class="requirement" id="req-length">
              <span class="req-icon">○</span>
              <span class="req-text">At least 12 characters</span>
            </div>
            <div class="requirement" id="req-uppercase">
              <span class="req-icon">○</span>
              <span class="req-text">One uppercase letter (A-Z)</span>
            </div>
            <div class="requirement" id="req-lowercase">
              <span class="req-icon">○</span>
              <span class="req-text">One lowercase letter (a-z)</span>
            </div>
            <div class="requirement" id="req-number">
              <span class="req-icon">○</span>
              <span class="req-text">One number (0-9)</span>
            </div>
            <div class="requirement" id="req-special">
              <span class="req-icon">○</span>
              <span class="req-text">One special character (!@#$%^&*)</span>
            </div>
          </div>

          <label>Confirm Password:
            <div class="password-input-wrapper">
              <input type="password" id="confirm-password" placeholder="Confirm new password" autocomplete="new-password" required minlength="12">
              <button type="button" class="toggle-password" data-target="confirm-password" aria-label="Show password">
                <svg class="eye-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
                  <circle cx="12" cy="12" r="3"></circle>
                </svg>
              </button>
            </div>
          </label>

          <div id="reset-error" class="error-message hidden" role="alert"></div>
          <div id="reset-success" class="success-message hidden"></div>
          <button type="submit" class="primary">${submitLabel}</button>
        </form>
      </div>
    </div>
  `;

  const form = document.getElementById('reset-password-form');
  const passwordInput = document.getElementById('new-password') as HTMLInputElement;

  if (form) {
    form.addEventListener('submit', (e) => void handleResetPasswordSubmit(e, token));
  }

  if (passwordInput) {
    passwordInput.addEventListener('input', () => {
      updatePasswordRequirements(passwordInput.value);
    });
  }

  setupPasswordToggle(modal);
  openAuthDialog(modal, '#new-password');
}

// renderExpiredView replaces the reset modal with a "link expired"
// view + CTA to request a new reset email (issue #460). The email
// tied to the expired token is not embedded in the URL, so the CTA
// hands the user back to the forgot-password form where they re-type
// their email.
function renderExpiredView(modal: HTMLElement, flow: string): void {
  const heading = flow === 'invite'
    ? 'Invitation link expired'
    : 'Password reset link expired';
  const windowCopy = flow === 'invite'
    ? 'Invitation links are valid for 7 days.'
    : 'Password reset links are valid for one hour.';
  modal.innerHTML = `
    <div class="modal-overlay">
      <div class="modal-content">
        <h2>${heading}</h2>
        <p>${windowCopy}</p>
        <p>Request a new link to continue.</p>
        <button type="button" id="reset-expired-request-new" class="primary">Send a new reset email</button>
        <p><a href="#" id="reset-expired-back-to-login">Back to login</a></p>
      </div>
    </div>
  `;

  document.getElementById('reset-expired-request-new')?.addEventListener('click', () => {
    void openForgotPasswordFromExpired();
  });
  document.getElementById('reset-expired-back-to-login')?.addEventListener('click', (e) => {
    e.preventDefault();
    void closeResetModalAndShowLogin();
  });
  openAuthDialog(modal, '#reset-expired-request-new');
}

// renderUsedView covers both consumed and never-existed tokens (the
// server collapses them into one state because the row is wiped on
// consumption; issue #461). Offers two exit paths: log in with the
// password they already set, or restart the forgot-password flow.
function renderUsedView(modal: HTMLElement, flow: string): void {
  const heading = flow === 'invite'
    ? 'Invitation link already used'
    : 'Password reset link already used';
  const body = flow === 'invite'
    ? "This invitation link has already been used. Sign in with the password you set, or use Forgot Password if you do not remember it."
    : "This password reset link has already been used. Sign in with the password you set, or use Forgot Password if you do not remember it.";
  modal.innerHTML = `
    <div class="modal-overlay">
      <div class="modal-content">
        <h2>${heading}</h2>
        <p>${body}</p>
        <button type="button" id="reset-used-go-to-login" class="primary">Go to login</button>
        <p><a href="#" id="reset-used-forgot-password">Forgot password?</a></p>
      </div>
    </div>
  `;

  document.getElementById('reset-used-go-to-login')?.addEventListener('click', () => {
    void closeResetModalAndShowLogin();
  });
  document.getElementById('reset-used-forgot-password')?.addEventListener('click', (e) => {
    e.preventDefault();
    void openForgotPasswordFromExpired();
  });
  openAuthDialog(modal, '#reset-used-go-to-login');
}

async function closeResetModalAndShowLogin(): Promise<void> {
  document.getElementById('reset-password-modal')?.remove();
  // Clear ?token= so a reload does not bounce back into the reset flow.
  window.history.replaceState({}, document.title, window.location.pathname);
  await showLoginModal();
}

async function openForgotPasswordFromExpired(): Promise<void> {
  document.getElementById('reset-password-modal')?.remove();
  window.history.replaceState({}, document.title, window.location.pathname);
  await showLoginModal();
  // Trigger the forgot-password swap inside the freshly-shown login
  // modal, matching the path a user would take by clicking the
  // "Forgot password?" link manually.
  document.getElementById('forgot-password-link')?.dispatchEvent(
    new MouseEvent('click', { bubbles: true, cancelable: true })
  );
}


async function handleResetPasswordSubmit(e: Event, token: string): Promise<void> {
  e.preventDefault();

  const errorDiv = document.getElementById('reset-error');
  const successDiv = document.getElementById('reset-success');
  errorDiv?.classList.add('hidden');
  successDiv?.classList.add('hidden');

  const newPasswordInput = document.getElementById('new-password') as HTMLInputElement | null;
  const confirmPasswordInput = document.getElementById('confirm-password') as HTMLInputElement | null;
  const newPassword = newPasswordInput?.value || '';
  const confirmPassword = confirmPasswordInput?.value || '';

  const requirementError = describePasswordValidationError(newPassword);
  if (requirementError) {
    if (errorDiv) {
      errorDiv.textContent = requirementError;
      errorDiv.classList.remove('hidden');
    }
    return;
  }

  if (newPassword !== confirmPassword) {
    if (errorDiv) {
      errorDiv.textContent = 'Passwords do not match';
      errorDiv.classList.remove('hidden');
    }
    return;
  }

  try {
    await api.resetPassword(token, newPassword);

    if (successDiv) {
      successDiv.textContent = 'Password reset successful! Redirecting to login...';
      successDiv.classList.remove('hidden');
    }

    // Redirect to login after 2 seconds
    setTimeout(() => {
      document.getElementById('reset-password-modal')?.remove();
      // Clear URL parameters
      window.history.replaceState({}, document.title, window.location.pathname);
      reloadPage();
    }, 2000);
  } catch (error) {
    const err = error as Error;
    if (errorDiv) {
      errorDiv.textContent = err.message || 'Failed to reset password. The link may have expired.';
      errorDiv.classList.remove('hidden');
    }
  }
}

/**
 * Show admin setup modal (first-time bootstrap)
 */
export async function showAdminSetupModal(apiKeyHint?: string): Promise<void> {
  // Remove any existing modal to prevent duplicates
  document.getElementById('admin-setup-modal')?.remove();

  const modal = document.createElement('div');
  modal.id = 'admin-setup-modal';
  modal.innerHTML = `
    <div class="modal-overlay">
      <div class="modal-content">
        <h2>Welcome to CUDly</h2>
        <p>No admin account exists yet. Set up the first admin to get started.</p>
        ${apiKeyHint ? `<p class="help-text">API Key can be found at: <code>${escapeHtml(apiKeyHint)}</code></p>` : ''}

        <form id="admin-setup-form">
          <label>API Key:
            <input type="password" id="setup-api-key" placeholder="Enter your API key" autocomplete="off" autocapitalize="none" autocorrect="off" spellcheck="false" required>
          </label>
          <label>Admin Email:
            <input type="email" id="setup-email" placeholder="admin@example.com" autocomplete="email" autocapitalize="none" autocorrect="off" required>
          </label>
          <label>Password:
            <div class="password-input-wrapper">
              <input type="password" id="setup-password" placeholder="Enter password" autocomplete="new-password" required minlength="12">
              <button type="button" class="toggle-password" data-target="setup-password" aria-label="Show password">
                <svg class="eye-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
                  <circle cx="12" cy="12" r="3"></circle>
                </svg>
              </button>
            </div>
          </label>

          <div id="setup-password-requirements" class="password-requirements">
            <div class="requirement" id="setup-req-length">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">At least 12 characters</span>
            </div>
            <div class="requirement" id="setup-req-uppercase">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">One uppercase letter (A-Z)</span>
            </div>
            <div class="requirement" id="setup-req-lowercase">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">One lowercase letter (a-z)</span>
            </div>
            <div class="requirement" id="setup-req-number">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">One number (0-9)</span>
            </div>
            <div class="requirement" id="setup-req-special">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">One special character (!@#$%^&*)</span>
            </div>
          </div>

          <label>Confirm Password:
            <div class="password-input-wrapper">
              <input type="password" id="setup-confirm-password" placeholder="Confirm password" autocomplete="new-password" required minlength="12">
              <button type="button" class="toggle-password" data-target="setup-confirm-password" aria-label="Show password">
                <svg class="eye-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
                  <circle cx="12" cy="12" r="3"></circle>
                </svg>
              </button>
            </div>
          </label>

          <div id="setup-error" class="error-message hidden" role="alert"></div>
          <button type="submit" class="primary">Create Admin Account</button>
        </form>
        <p class="help-text"><a href="#" id="admin-setup-login-link">Already have an account? Log in</a></p>
      </div>
    </div>
  `;
  document.body.appendChild(modal);

  const form = document.getElementById('admin-setup-form');
  const passwordInput = document.getElementById('setup-password') as HTMLInputElement;

  if (form) {
    form.addEventListener('submit', (e) => void handleAdminSetupSubmit(e));
  }

  if (passwordInput) {
    passwordInput.addEventListener('input', () => {
      updatePasswordRequirements(passwordInput.value, 'setup-req-');
    });
  }

  const loginLink = document.getElementById('admin-setup-login-link');
  if (loginLink) {
    loginLink.addEventListener('click', (e) => {
      e.preventDefault();
      modal.remove();
      void showLoginModal();
    });
  }

  setupPasswordToggle(modal);
  openAuthDialog(modal, '#setup-api-key');
}

async function handleAdminSetupSubmit(e: Event): Promise<void> {
  e.preventDefault();

  const errorDiv = document.getElementById('setup-error');
  errorDiv?.classList.add('hidden');

  const apiKey = (document.getElementById('setup-api-key') as HTMLInputElement)?.value.trim() || '';
  const email = (document.getElementById('setup-email') as HTMLInputElement)?.value.trim() || '';
  const password = (document.getElementById('setup-password') as HTMLInputElement)?.value || '';
  const confirmPassword = (document.getElementById('setup-confirm-password') as HTMLInputElement)?.value || '';

  const requirementError = describePasswordValidationError(password);
  if (requirementError) {
    if (errorDiv) {
      errorDiv.textContent = requirementError;
      errorDiv.classList.remove('hidden');
    }
    return;
  }

  if (password !== confirmPassword) {
    if (errorDiv) {
      errorDiv.textContent = 'Passwords do not match';
      errorDiv.classList.remove('hidden');
    }
    return;
  }

  const submitBtn = document.querySelector('#admin-setup-form button[type="submit"]') as HTMLButtonElement | null;
  if (submitBtn) {
    submitBtn.disabled = true;
    submitBtn.textContent = 'Creating...';
  }

  try {
    await api.setupAdmin(apiKey, email, password);
    document.getElementById('admin-setup-modal')?.remove();
    reloadPage();
  } catch (error) {
    const err = error as Error;
    if (errorDiv) {
      errorDiv.textContent = err.message || 'Failed to create admin account';
      errorDiv.classList.remove('hidden');
    }
  } finally {
    if (submitBtn) {
      submitBtn.disabled = false;
      submitBtn.textContent = 'Create Admin Account';
    }
  }
}

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

/**
 * Up to two letters for the topbar avatar: the first letters of the first
 * two dot/underscore/dash separated parts of the address's local part
 * ("dana.okafor@..." -> "DO"), else its first two characters.
 */
export function emailInitials(email: string): string {
  const local = email.split('@')[0] ?? '';
  const parts = local.split(/[._\-+]+/).filter(Boolean);
  const letters = parts.length >= 2
    ? `${parts[0]!.charAt(0)}${parts[1]!.charAt(0)}`
    : local.slice(0, 2);
  return letters.toUpperCase();
}

/**
 * Update user UI after login
 */
export function updateUserUI(): void {
  const currentUser = state.getCurrentUser();
  const userEmailEl = document.getElementById('user-email-display');
  const userInfoEl = document.getElementById('user-info');
  const logoutBtn = document.getElementById('logout-btn');

  const roleEl = document.getElementById('user-role-display');

  if (currentUser) {
    // Update the user email display with click-to-edit functionality
    if (userEmailEl) {
      userEmailEl.textContent = currentUser.email;
      // Drawn as an avatar by CSS (::before), so it stays out of the text.
      userEmailEl.dataset['initials'] = emailInitials(currentUser.email);
      userEmailEl.removeAttribute('aria-label');
      userEmailEl.hidden = false;
      userEmailEl.title = 'Click to edit your profile';
      userEmailEl.classList.add('cursor-pointer');
      // Replace element to avoid duplicate listeners on repeated calls
      const freshEmailEl = userEmailEl.cloneNode(true) as HTMLElement;
      userEmailEl.parentNode?.replaceChild(freshEmailEl, userEmailEl);
      freshEmailEl.addEventListener('click', () => void openProfileModal());
    }
    // Show admin badge when the user is a member of the Administrators
    // group. PR #912 removed user.role from the API response; use the
    // group-membership-based isAdmin() predicate instead.
    if (roleEl) {
      if (permissionsIsAdmin()) {
        roleEl.textContent = 'admin';
        roleEl.classList.remove('hidden');
      } else {
        roleEl.textContent = '';
        roleEl.classList.add('hidden');
      }
    }
    // Show the user info section
    if (userInfoEl) {
      userInfoEl.classList.remove('hidden');
    }

    const adminOnly = permissionsIsAdmin();
    document.querySelectorAll<HTMLElement>('.admin-only').forEach(el => {
      el.classList.toggle('visible', adminOnly);
    });

    // Gate nav entries and page containers that require view:purchases.
    // Mirrors the admin-only pattern: the CSS class hides by default;
    // .visible makes the element visible. Direct URL navigation into a
    // gated page is handled in navigation.ts switchTab().
    const canViewPurchases = canAccess('view', 'purchases');
    document.querySelectorAll<HTMLElement>('.requires-purchases').forEach(el => {
      el.classList.toggle('visible', canViewPurchases);
    });
  } else {
    // Hide user info when not logged in
    if (userInfoEl) {
      userInfoEl.classList.add('hidden');
    }
    if (roleEl) {
      roleEl.classList.add('hidden');
    }
    if (userEmailEl) {
      userEmailEl.hidden = true;
    }
  }

  // Setup logout handler
  if (logoutBtn) {
    logoutBtn.addEventListener('click', () => void logout());
  }
}

/**
 * Open profile edit modal
 */
async function openProfileModal(): Promise<void> {
  const currentUser = state.getCurrentUser();
  if (!currentUser) return;

  // Create modal if it doesn't exist
  let modal = document.getElementById('profile-modal');
  if (!modal) {
    modal = document.createElement('div');
    modal.id = 'profile-modal';
    modal.className = 'modal hidden';
    modal.innerHTML = `
      <div class="modal-content">
        <h2>Edit Profile</h2>
        <form id="profile-form">
          <label>
            Email
            <input type="email" id="profile-email" autocomplete="email" autocapitalize="none" autocorrect="off" required>
          </label>
          <label>
            Current Password (required to save changes)
            <div class="password-input-wrapper">
              <input type="password" id="profile-current-password" placeholder="Enter current password" autocomplete="current-password">
              <button type="button" class="toggle-password" data-target="profile-current-password" aria-label="Show password">
                <svg class="eye-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
                  <circle cx="12" cy="12" r="3"></circle>
                </svg>
              </button>
            </div>
          </label>
          <label>
            New Password (leave blank to keep current)
            <div class="password-input-wrapper">
              <input type="password" id="profile-new-password" placeholder="Enter new password" autocomplete="new-password">
              <button type="button" class="toggle-password" data-target="profile-new-password" aria-label="Show password">
                <svg class="eye-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
                  <circle cx="12" cy="12" r="3"></circle>
                </svg>
              </button>
            </div>
          </label>

          <div id="profile-password-requirements" class="password-requirements">
            <div class="requirement" id="profile-req-length">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">At least 12 characters</span>
            </div>
            <div class="requirement" id="profile-req-uppercase">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">One uppercase letter (A-Z)</span>
            </div>
            <div class="requirement" id="profile-req-lowercase">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">One lowercase letter (a-z)</span>
            </div>
            <div class="requirement" id="profile-req-number">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">One number (0-9)</span>
            </div>
            <div class="requirement" id="profile-req-special">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">One special character (!@#$%^&*)</span>
            </div>
          </div>

          <label>
            Confirm New Password
            <div class="password-input-wrapper">
              <input type="password" id="profile-confirm-password" placeholder="Confirm new password" autocomplete="new-password">
              <button type="button" class="toggle-password" data-target="profile-confirm-password" aria-label="Show password">
                <svg class="eye-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
                  <circle cx="12" cy="12" r="3"></circle>
                </svg>
              </button>
            </div>
          </label>
          <div id="profile-password-error" class="error-message hidden"></div>
          <div class="modal-buttons">
            <button type="button" id="profile-cancel">Cancel</button>
            <button type="submit" class="primary">Save Changes</button>
          </div>
        </form>
        <hr class="profile-section-divider">
        <div id="profile-mfa-section">
          <!-- Populated by renderMFASection() on open -->
        </div>
      </div>
    `;
    document.body.appendChild(modal);

    // Add event listeners
    document.getElementById('profile-cancel')?.addEventListener('click', closeProfileModal);
    document.getElementById('profile-form')?.addEventListener('submit', (e) => void saveProfile(e));

    // Live password-strength indicator. Mirrors the reset / admin-setup
    // flows so the user sees criterion check-marks as they type instead
    // of only on submit. Uses a flow-specific prefix so the IDs never
    // collide with other modals that might be mounted on the same page.
    const newPasswordInput = document.getElementById('profile-new-password') as HTMLInputElement | null;
    if (newPasswordInput) {
      newPasswordInput.addEventListener('input', () => {
        updatePasswordRequirements(newPasswordInput.value, 'profile-req-');
      });
    }

    // Setup password toggle
    setupPasswordToggle(modal);
  }

  // Populate with current values
  (document.getElementById('profile-email') as HTMLInputElement).value = currentUser.email;
  (document.getElementById('profile-current-password') as HTMLInputElement).value = '';
  (document.getElementById('profile-new-password') as HTMLInputElement).value = '';
  (document.getElementById('profile-confirm-password') as HTMLInputElement).value = '';

  // (Re-)render the MFA section so its state reflects whatever the
  // user's mfa_enabled flag is right now (could have flipped since
  // the modal was last opened). See renderMFASection below.
  setProfileSecretPending(false);
  renderMFASection();

  // Reset live indicators and any stale error so the previous open
  // doesn't bleed into this one (modal is created once and reused).
  updatePasswordRequirements('', 'profile-req-');
  const errorDiv = document.getElementById('profile-password-error');
  if (errorDiv) {
    errorDiv.textContent = '';
    errorDiv.classList.add('hidden');
  }

  // Show modal
  openModal(modal);
}

/**
 * Close profile modal
 */
function closeProfileModal(): void {
  const modal = document.getElementById('profile-modal');
  if (modal) closeModal(modal);
}

export function downloadRecoveryCodes(codes: string[]): void {
  const body = [
    'CUDly two-factor recovery codes',
    `Generated: ${new Date().toISOString()}`,
    '',
    'Each code can be used only once. Save these somewhere safe.',
    '',
    ...codes,
  ].join('\n');
  const blob = new Blob([body], { type: 'text/plain' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = 'cudly-recovery-codes.txt';
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

/**
 * Disable flow: prompt for password + (TOTP or recovery code).
 */
export function renderMFADisableFlow(): void {
  const host = getMFAFlowHost();
  if (!host) return;
  host.innerHTML = `
    <div class="mfa-panel">
      <h4>Disable two-factor authentication</h4>
      <p class="help-text">Enter your password and a current code (or a recovery code) to disable.</p>
      <label>Password:
        <input type="password" id="mfa-disable-password" autocomplete="current-password">
      </label>
      <label>Authenticator code or recovery code:
        <input
          type="text"
          id="mfa-disable-code"
          inputmode="numeric"
          autocomplete="one-time-code"
          maxlength="16"
          class="code-input"
        >
      </label>
      <div id="mfa-flow-error" class="error-message hidden" role="alert"></div>
      <div class="modal-buttons">
        <button type="button" id="mfa-disable-cancel">Cancel</button>
        <button type="button" id="mfa-disable-submit" class="primary">Disable</button>
      </div>
    </div>
  `;
  document.getElementById('mfa-disable-cancel')?.addEventListener('click', () => { host.innerHTML = ''; });
  document.getElementById('mfa-disable-submit')?.addEventListener('click', () => { void handleMFADisableSubmit(); });
}

async function handleMFADisableSubmit(): Promise<void> {
  const pwInput = document.getElementById('mfa-disable-password') as HTMLInputElement | null;
  const codeInput = document.getElementById('mfa-disable-code') as HTMLInputElement | null;
  const password = pwInput?.value || '';
  const code = codeInput?.value.trim() || '';
  if (password === '') { setMFAFlowError('Enter your password'); return; }
  if (code === '') { setMFAFlowError('Enter your authenticator code or a recovery code'); return; }
  clearMFAFlowError();
  try {
    await api.disableMFA(password, code);
    const cur = state.getCurrentUser();
    if (cur) {
      state.setCurrentUser({ ...cur, mfa_enabled: false });
    }
    renderMFASection();
  } catch (err) {
    setMFAFlowError(err instanceof Error ? err.message : String(err));
  }
}

/**
 * Regenerate-recovery-codes flow: prompt for a current TOTP code,
 * call the endpoint, surface the new plaintext codes.
 */
export function renderMFARegenerateFlow(): void {
  const host = getMFAFlowHost();
  if (!host) return;
  host.innerHTML = `
    <div class="mfa-panel">
      <h4>Regenerate recovery codes</h4>
      <p class="help-text">Enter a current 6-digit code from your authenticator app. Existing recovery codes will stop working.</p>
      <label>Code:
        <input
          type="text"
          id="mfa-regen-code"
          inputmode="numeric"
          autocomplete="one-time-code"
          pattern="[0-9]{6}"
          maxlength="6"
          class="code-input"
        >
      </label>
      <div id="mfa-flow-error" class="error-message hidden" role="alert"></div>
      <div class="modal-buttons">
        <button type="button" id="mfa-regen-cancel">Cancel</button>
        <button type="button" id="mfa-regen-submit" class="primary">Regenerate</button>
      </div>
    </div>
  `;
  document.getElementById('mfa-regen-cancel')?.addEventListener('click', () => { host.innerHTML = ''; });
  document.getElementById('mfa-regen-submit')?.addEventListener('click', () => { void handleMFARegenerateSubmit(); });
}

async function handleMFARegenerateSubmit(): Promise<void> {
  const codeInput = document.getElementById('mfa-regen-code') as HTMLInputElement | null;
  const code = codeInput?.value.trim() || '';
  if (!/^\d{6}$/.test(code)) { setMFAFlowError('Enter a 6-digit code'); return; }
  clearMFAFlowError();
  try {
    const res = await api.regenerateMFARecoveryCodes(code);
    // Reuse the codes-display step from enrollment — same UX,
    // identical contents.
    renderMFAEnrollCodesStep(res.recovery_codes);
  } catch (err) {
    setMFAFlowError(err instanceof Error ? err.message : String(err));
  }
}

/**
 * Save profile changes
 */
async function saveProfile(e: Event): Promise<void> {
  e.preventDefault();
  if (document.getElementById('profile-modal')?.dataset.requireAck === 'true') return;

  const email = (document.getElementById('profile-email') as HTMLInputElement).value;
  const currentPassword = (document.getElementById('profile-current-password') as HTMLInputElement).value;
  const newPassword = (document.getElementById('profile-new-password') as HTMLInputElement).value;
  const confirmPassword = (document.getElementById('profile-confirm-password') as HTMLInputElement).value;

  // Clear any stale password-validation error from a prior submit so we
  // start with a clean slate on every attempt.
  const passwordErrorDiv = document.getElementById('profile-password-error');
  if (passwordErrorDiv) {
    passwordErrorDiv.textContent = '';
    passwordErrorDiv.classList.add('hidden');
  }

  if (!currentPassword) {
    // Use showToast instead of blocking alert() (finding 11-L3).
    showToast({ message: 'Please enter your current password to save changes', kind: 'error' });
    return;
  }

  if (newPassword) {
    // Password-validation failures render inline (matches reset and
    // admin-setup flows) so the criterion text appears next to the
    // requirements indicator the user is already looking at.
    const requirementError = describePasswordValidationError(newPassword);
    if (requirementError) {
      if (passwordErrorDiv) {
        passwordErrorDiv.textContent = requirementError;
        passwordErrorDiv.classList.remove('hidden');
      }
      return;
    }
    if (newPassword !== confirmPassword) {
      if (passwordErrorDiv) {
        passwordErrorDiv.textContent = 'New passwords do not match';
        passwordErrorDiv.classList.remove('hidden');
      }
      return;
    }
  }

  try {
    // Call API to update profile
    await api.apiRequest('/auth/profile', {
      method: 'PUT',
      body: JSON.stringify({
        email,
        current_password: api.base64Encode(currentPassword),
        new_password: newPassword ? api.base64Encode(newPassword) : undefined
      })
    });

    // Update local state
    const currentUser = state.getCurrentUser();
    if (currentUser) {
      state.setCurrentUser({ ...currentUser, email });
      const userEmailEl = document.getElementById('user-email-display');
      if (userEmailEl) {
        userEmailEl.textContent = email;
        userEmailEl.dataset['initials'] = emailInitials(email);
      }
    }

    closeProfileModal();
    // Use showToast instead of blocking alert() (finding 11-L3).
    showToast({ message: 'Profile updated successfully', kind: 'success', timeout: 5_000 });
  } catch (error) {
    console.error('Failed to update profile:', error);
    const err = error as Error;
    showToast({ message: `Failed to update profile: ${err.message}`, kind: 'error' });
  }
}

/**
 * Logout handler
 */
export async function logout(): Promise<void> {
  try {
    await api.logout();
  } catch (error) {
    // The session is still alive server-side: stay signed in and say so.
    showToast({ message: (error as Error).message, kind: 'error' });
    return;
  }
  state.setCurrentUser(null);
  reloadPage();
}

export { describePasswordValidationError } from './authflow/password';
