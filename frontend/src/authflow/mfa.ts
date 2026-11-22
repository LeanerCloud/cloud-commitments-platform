/**
 * Two-factor authentication enrollment and lifecycle UI inside the profile modal.
 */

import * as api from '../api';
import * as state from '../state';
import { escapeHtml } from '../utils';
import { downloadRecoveryCodes, renderMFADisableFlow, renderMFARegenerateFlow } from '../auth';

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
async function renderMFAEnrollQRStep(secret: string, provisioningURI: string): Promise<void> {
  const host = getMFAFlowHost();
  if (!host) return;
  host.innerHTML = `
    <div class="mfa-panel">
      <h4>Scan QR code</h4>
      <p class="help-text">Scan this code with your authenticator app (Google Authenticator, Authy, Bitwarden, 1Password, etc.).</p>
      <div id="mfa-qr" class="mfa-qr"></div>
      <p class="help-text">Can't scan? Enter this secret manually:</p>
      <div class="mfa-secret">
        <span id="mfa-secret-display">${escapeHtml(formatSecretForDisplay(secret))}</span>
        <button type="button" id="mfa-secret-copy">Copy</button>
      </div>
      <label>Enter the 6-digit code shown in your app:
        <input
          type="text"
          id="mfa-enroll-code"
          inputmode="numeric"
          autocomplete="one-time-code"
          pattern="[0-9]{6}"
          maxlength="6"
          class="code-input"
        >
      </label>
      <div id="mfa-flow-error" class="error-message hidden" role="alert"></div>
      <div class="modal-buttons">
        <button type="button" id="mfa-qr-cancel">Cancel</button>
        <button type="button" id="mfa-qr-verify" class="primary">Verify and enable</button>
      </div>
    </div>
  `;

  document.getElementById('mfa-secret-copy')?.addEventListener('click', () => {
    void navigator.clipboard?.writeText(secret);
  });
  document.getElementById('mfa-qr-cancel')?.addEventListener('click', () => { host.innerHTML = ''; });
  document.getElementById('mfa-qr-verify')?.addEventListener('click', () => { void handleMFAEnrollVerify(); });

  // Render the QR. Lazy-imported so the dep stays out of the initial
  // bundle until the user actually starts an enrollment.
  try {
    const qrcode = await import('qrcode');
    const container = document.getElementById('mfa-qr');
    if (container) {
      const dataUrl = await qrcode.toDataURL(provisioningURI, { width: 200, margin: 1 });
      const img = document.createElement('img');
      img.src = dataUrl;
      img.alt = 'Two-factor authentication QR code';
      container.appendChild(img);
    }
  } catch (err) {
    setMFAFlowError('QR rendering failed; use the manual secret above. ' + (err instanceof Error ? err.message : ''));
  }
}

// formatSecretForDisplay groups the secret into 4-char chunks
// separated by spaces so a user typing it into an authenticator app
// has visual landmarks to track their place.
function formatSecretForDisplay(secret: string): string {
  return secret.match(/.{1,4}/g)?.join(' ') ?? secret;
}

async function handleMFAEnrollVerify(): Promise<void> {
  const codeInput = document.getElementById('mfa-enroll-code') as HTMLInputElement | null;
  const code = codeInput?.value.trim() || '';
  if (!/^\d{6}$/.test(code)) {
    setMFAFlowError('Enter a 6-digit code');
    return;
  }
  clearMFAFlowError();
  try {
    const res = await api.enableMFA(code);
    // Reflect the new state in the cached current user so the
    // section re-renders into the Enabled view when we exit the
    // recovery-codes pane.
    const cur = state.getCurrentUser();
    if (cur) {
      state.setCurrentUser({ ...cur, mfa_enabled: true });
    }
    renderMFAEnrollCodesStep(res.recovery_codes);
  } catch (err) {
    setMFAFlowError(err instanceof Error ? err.message : String(err));
  }
}

export function setProfileSecretPending(pending: boolean): void {
  const modal = document.getElementById('profile-modal');
  if (modal) {
    if (pending) modal.dataset.requireAck = 'true';
    else delete modal.dataset.requireAck;
  }
  const cancel = document.getElementById('profile-cancel') as HTMLButtonElement | null;
  if (cancel) cancel.disabled = pending;
  const save = document.querySelector<HTMLButtonElement>('#profile-form button[type="submit"]');
  if (save) save.disabled = pending;
}

/**
 * Step 3 of enrollment: display the plaintext recovery codes. Shown
 * exactly once; backend stores only bcrypt hashes. Offers a download
 * button so the user has a way to keep them somewhere other than a
 * password manager.
 */
export function renderMFAEnrollCodesStep(codes: string[]): void {
  const host = getMFAFlowHost();
  if (!host) return;
  const codesList = codes.map((c) => `<li>${escapeHtml(c)}</li>`).join('');
  host.innerHTML = `
    <div class="mfa-panel mfa-panel-emphasis">
      <h4>Save your recovery codes</h4>
      <p class="help-text"><strong>Each code can be used once</strong> if you lose access to your authenticator app. Save them somewhere safe — this is the only time they'll be shown.</p>
      <ul class="recovery-codes" aria-label="Recovery codes">
        ${codesList}
      </ul>
      <div class="modal-buttons">
        <button type="button" id="mfa-codes-download">Download .txt</button>
        <button type="button" id="mfa-codes-done" class="primary">I've saved them</button>
      </div>
    </div>
  `;
  setProfileSecretPending(true);
  document.getElementById('mfa-codes-download')?.addEventListener('click', () => downloadRecoveryCodes(codes));
  document.getElementById('mfa-codes-done')?.addEventListener('click', () => {
    // Acknowledged — re-render the section so it now shows the
    // Enabled view (Disable / Regenerate buttons).
    setProfileSecretPending(false);
    renderMFASection();
  });
}
