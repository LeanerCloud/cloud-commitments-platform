/**
 * Two-factor authentication enrollment and lifecycle UI inside the profile modal.
 */

import * as api from '../api';
import * as state from '../state';
import { renderMFADisableFlow, renderMFAEnrollQRStep, renderMFARegenerateFlow } from '../auth';

// ----------------------------------------------------------------
// MFA enrollment / lifecycle UI (issue #497).
//
// The "Two-factor authentication" section in the profile modal has
// two top-level states (disabled / enabled) and three transient
// flows reachable from those states (enroll, disable, regenerate
// recovery codes). The implementation renders the section's HTML
// into a single host element (#profile-mfa-section) and re-renders
// the whole thing on every state change rather than mutating in
// place — easier to reason about, and the section is small.
// ----------------------------------------------------------------

export function renderMFASection(): void {
  const host = document.getElementById('profile-mfa-section');
  if (!host) return;
  const currentUser = state.getCurrentUser();
  const mfaEnabled = currentUser?.mfa_enabled === true;
  if (mfaEnabled) {
    host.innerHTML = `
      <h3>Two-factor authentication</h3>
      <p class="help-text">Two-factor authentication is <strong>enabled</strong> for your account.</p>
      <div class="modal-buttons">
        <button type="button" id="mfa-disable-btn">Disable</button>
        <button type="button" id="mfa-regenerate-btn">Regenerate recovery codes</button>
      </div>
      <div id="mfa-flow-host"></div>
    `;
    document.getElementById('mfa-disable-btn')?.addEventListener('click', () => renderMFADisableFlow());
    document.getElementById('mfa-regenerate-btn')?.addEventListener('click', () => renderMFARegenerateFlow());
  } else {
    host.innerHTML = `
      <h3>Two-factor authentication</h3>
      <p class="help-text">Add an extra layer of security by requiring a 6-digit code from an authenticator app at sign-in.</p>
      <div class="modal-buttons">
        <button type="button" id="mfa-enable-btn" class="primary">Set up two-factor authentication</button>
      </div>
      <div id="mfa-flow-host"></div>
    `;
    document.getElementById('mfa-enable-btn')?.addEventListener('click', () => renderMFAEnrollPasswordStep());
  }
}

export function getMFAFlowHost(): HTMLElement | null {
  return document.getElementById('mfa-flow-host');
}

export function setMFAFlowError(message: string): void {
  const errEl = document.getElementById('mfa-flow-error');
  if (errEl) {
    errEl.textContent = message;
    errEl.classList.remove('hidden');
  }
}

export function clearMFAFlowError(): void {
  document.getElementById('mfa-flow-error')?.classList.add('hidden');
}

/**
 * Step 1 of enrollment: re-prompt for the user's password. We don't
 * trust the session token alone to authorise an MFA change — a
 * stolen tab/cookie alone shouldn't let an attacker re-key MFA.
 */
function renderMFAEnrollPasswordStep(): void {
  const host = getMFAFlowHost();
  if (!host) return;
  host.innerHTML = `
    <div class="mfa-panel">
      <h4>Confirm password</h4>
      <p class="help-text">Re-enter your password to begin setting up two-factor authentication.</p>
      <label>Password:
        <input type="password" id="mfa-enroll-password" autocomplete="current-password">
      </label>
      <div id="mfa-flow-error" class="error-message hidden" role="alert"></div>
      <div class="modal-buttons">
        <button type="button" id="mfa-enroll-cancel">Cancel</button>
        <button type="button" id="mfa-enroll-continue" class="primary">Continue</button>
      </div>
    </div>
  `;
  document.getElementById('mfa-enroll-cancel')?.addEventListener('click', () => { host.innerHTML = ''; });
  document.getElementById('mfa-enroll-continue')?.addEventListener('click', () => { void handleMFAEnrollStart(); });
}

async function handleMFAEnrollStart(): Promise<void> {
  const pwInput = document.getElementById('mfa-enroll-password') as HTMLInputElement | null;
  const password = pwInput?.value || '';
  if (password === '') {
    setMFAFlowError('Enter your password');
    return;
  }
  clearMFAFlowError();
  try {
    const setupRes = await api.setupMFA(password);
    await renderMFAEnrollQRStep(setupRes.secret, setupRes.provisioning_uri);
  } catch (err) {
    setMFAFlowError(err instanceof Error ? err.message : String(err));
  }
}

/**
 * Step 2 of enrollment: show the QR code + manual secret + a code
 * input. Dynamically imports the `qrcode` library so the
 * ~30KB-gzipped dep only loads when a user actually starts an
 * enrollment.
 */
