/**
 * Authentication module for CUDly
 */

import * as api from './api';
import { escapeHtml, reloadPage } from './utils';
import { openAuthDialog } from './authDialog';
import { isAdmin as permissionsIsAdmin } from './permissions';
import { showLoginModal } from './authflow/login';
import { describePasswordValidationError, setupPasswordToggle, updatePasswordRequirements } from './authflow/password';

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

export { describePasswordValidationError } from './authflow/password';
export { emailInitials, logout, updateUserUI } from './authflow/profile';
export { MFA_PENDING_TTL_MS, showLoginModal } from './authflow/login';
