import * as api from '../api';
import type { CreateAPIKeyResponse } from '../types';
import { escapeHtml } from '../utils';
import { openModal, closeModal } from '../modal';
import { showSkeletonText } from '../lib/skeleton';
import { parseLocalDateInputValue, toLocalDateInputValue } from '../lib/local-date';
import { loadApiKeys, showError } from './list';

/**
 * Format an (action, resource) permission entry for display in the
 * create-key picker. No catalog of human labels is maintained here --
 * the values are the closed vocabulary the backend already validates
 * (internal/auth/types.go), so the raw "action:resource" pair is
 * unambiguous and never goes stale against a separate label list.
 */
function formatPermissionLabel(entry: api.PermissionEntry): string {
  if (entry.action === 'admin' && entry.resource === '*') {
    return 'Admin (all actions, all resources)';
  }
  return `${entry.action}:${entry.resource}`;
}

/**
 * Render the create-key permission picker as one checkbox per entry the
 * calling user actually holds (issue #61's frontend follow-up): after the
 * backend fix, CreateAPIKey rejects an empty permissions array, so a
 * picker limited to the owner's own effective permissions is both the
 * fix for the resulting "every key creation 400s" regression and the
 * least-privilege UI the per-key scoping feature always intended.
 *
 * Every interpolated value is escaped even though action/resource come
 * from the closed backend vocabulary -- defense in depth for an innerHTML
 * write.
 */
function renderApiKeyPermissionPicker(entries: api.PermissionEntry[]): void {
  const container = document.getElementById('apikey-permissions-list');
  if (!container) return;

  if (entries.length === 0) {
    container.innerHTML = '<p class="text-muted">No permissions available on your account -- an API key cannot be created.</p>';
    return;
  }

  container.innerHTML = entries.map((entry, i) => `
    <label class="apikey-permission-option">
      <input type="checkbox" class="apikey-permission-checkbox" id="apikey-perm-${i}"
             data-action="${escapeHtml(entry.action)}" data-resource="${escapeHtml(entry.resource)}">
      ${escapeHtml(formatPermissionLabel(entry))}
    </label>
  `).join('');

  container.querySelectorAll('.apikey-permission-checkbox').forEach(cb => {
    cb.addEventListener('change', updateCreateKeySubmitState);
  });
}

/**
 * Read the permissions currently checked in the picker.
 */
function collectSelectedApiKeyPermissions(): api.Permission[] {
  const container = document.getElementById('apikey-permissions-list');
  if (!container) return [];

  const permissions: api.Permission[] = [];
  container.querySelectorAll<HTMLInputElement>('.apikey-permission-checkbox:checked').forEach(cb => {
    const action = cb.dataset['action'];
    const resource = cb.dataset['resource'];
    if (action && resource) permissions.push({ action, resource });
  });
  return permissions;
}

/**
 * Enable the Create button only while at least one permission is
 * selected. An unscoped key is now rejected server-side (400), and
 * disabling here catches it before the round-trip rather than after.
 */
function updateCreateKeySubmitState(): void {
  const submitBtn = document.getElementById('create-apikey-submit-btn') as HTMLButtonElement | null;
  if (!submitBtn) return;
  submitBtn.disabled = collectSelectedApiKeyPermissions().length === 0;
}

/**
 * Show create API key modal.
 *
 * Fetches the caller's own effective permissions fresh on every open
 * (rather than reusing a cached snapshot from login) so the picker
 * reflects the account's CURRENT grants -- a group change since login
 * shows up immediately instead of offering a permission the key would
 * then fail to obtain from the intersection check server-side.
 */
export async function showCreateKeyModal(): Promise<void> {
  const modal = document.getElementById('create-apikey-modal');
  const form = document.getElementById('create-apikey-form') as HTMLFormElement;
  const errorEl = document.getElementById('create-apikey-error');
  const submitBtn = document.getElementById('create-apikey-submit-btn') as HTMLButtonElement | null;

  if (!modal || !form) return;

  form.reset();
  if (errorEl) errorEl.classList.add('hidden');
  if (submitBtn) submitBtn.disabled = true;

  // Expiration is required (issue #102: no more "never expires"). Prefill
  // 90 days out so the field never starts empty; the actual cap is
  // enforced server-side.
  const expiresAtInput = document.getElementById('apikey-expires-at') as HTMLInputElement | null;
  if (expiresAtInput) {
    const defaultDate = new Date();
    defaultDate.setDate(defaultDate.getDate() + 90);
    expiresAtInput.value = toLocalDateInputValue(defaultDate);
  }

  const permissionsContainer = document.getElementById('apikey-permissions-list');
  // Issue #186: skeleton instead of a "Loading your permissions…" line.
  if (permissionsContainer) showSkeletonText(permissionsContainer, 3);

  // Open the modal before the permissions fetch resolves so the dialog
  // never sits closed while waiting on the network.
  openModal(modal);

  try {
    const { permissions } = await api.getUserPermissions();
    renderApiKeyPermissionPicker(permissions);
  } catch (error) {
    console.error('Failed to load permissions for API key creation:', error);
    if (permissionsContainer) {
      permissionsContainer.innerHTML = '<p class="error">Failed to load your permissions. Close and reopen this dialog to retry.</p>';
    }
  }
  updateCreateKeySubmitState();
}

/**
 * Close create API key modal
 */
export function closeCreateKeyModal(): void {
  const modal = document.getElementById('create-apikey-modal');
  if (modal) closeModal(modal);
}

/**
 * Create new API key. password, expiresAt and permissions are all required
 * server-side: creation re-verifies the caller's password (issue #102), a
 * key can no longer be minted with no expiration (issue #102), and an
 * empty permissions array is rejected outright (issue #61).
 */
export async function createApiKey(name: string, password: string, expiresAt: Date, permissions: api.Permission[]): Promise<CreateAPIKeyResponse> {
  try {
    const request: api.CreateAPIKeyRequest = {
      name,
      password: api.base64Encode(password),
      expires_at: expiresAt.toISOString(),
      permissions,
    };

    const response = await api.createApiKey(request);
    return response;
  } catch (error) {
    console.error('Failed to create API key:', error);
    throw error;
  }
}

/**
 * Handle create API key form submission
 */
let createInFlight = false;

export async function handleCreateApiKey(e: Event): Promise<void> {
  e.preventDefault();
  if (createInFlight) return;

  const errorEl = document.getElementById('create-apikey-error');
  if (errorEl) errorEl.classList.add('hidden');

  const name = (document.getElementById('apikey-name') as HTMLInputElement | null)?.value.trim() ?? '';
  const password = (document.getElementById('apikey-password') as HTMLInputElement | null)?.value ?? '';
  const expiresAtInput = (document.getElementById('apikey-expires-at') as HTMLInputElement | null)?.value ?? '';

  if (!name) {
    showError('API key name is required');
    return;
  }
  if (!password) {
    showError('Your password is required to create an API key');
    return;
  }
  if (!expiresAtInput) {
    showError('An expiration date is required');
    return;
  }

  // Belt-and-suspenders alongside the disabled submit button: the backend
  // now rejects an empty permissions array with 400 (issue #61), so an
  // unscoped key can never be minted. Catching it here avoids the round
  // trip and gives a clearer message than the raw 400 body.
  const permissions = collectSelectedApiKeyPermissions();
  if (permissions.length === 0) {
    showError('Select at least one permission');
    return;
  }

  const expiresAt = parseLocalDateInputValue(expiresAtInput);
  if (expiresAt <= new Date()) {
    showError('Expiration date must be in the future');
    return;
  }

  createInFlight = true;
  const submitBtn = document.getElementById('create-apikey-submit-btn') as HTMLButtonElement | null;
  if (submitBtn) submitBtn.disabled = true;
  try {
    const response = await createApiKey(name, password, expiresAt, permissions);
    closeCreateKeyModal();
    showKeyCreatedModal(response.api_key);
    await loadApiKeys();
  } catch (error) {
    const err = error as Error;
    showError(`Failed to create API key: ${err.message}`);
  } finally {
    createInFlight = false;
    updateCreateKeySubmitState();
  }
}

/**
 * Show key created modal with one-time display
 */
export function showKeyCreatedModal(apiKey: string): void {
  // Remove any existing modal to prevent duplicates
  document.getElementById('apikey-created-modal')?.remove();

  const modal = document.createElement('div');
  modal.id = 'apikey-created-modal';
  modal.className = 'modal';
  modal.dataset.requireAck = 'true';
  modal.setAttribute('role', 'dialog');
  modal.setAttribute('aria-modal', 'true');
  modal.setAttribute('aria-labelledby', 'apikey-created-title');
  modal.innerHTML = `
    <div class="modal-content">
      <div class="modal-header"><h2 id="apikey-created-title">API Key Created Successfully</h2></div>
      <div class="warning-box">
        <strong>Important:</strong> This is the only time you'll see this API key.
        Please copy it now and store it securely.
      </div>
      <div class="apikey-display">
        <label>Your API Key:</label>
        <div class="apikey-value-container">
          <code id="apikey-value" class="apikey-value">${escapeHtml(apiKey)}</code>
          <button type="button" id="copy-apikey-btn" class="btn-small primary">Copy</button>
        </div>
        <p id="apikey-copy-error" class="field-error hidden" role="alert"></p>
      </div>
      <div class="modal-info">
        <p>Use this key in the <code>X-API-Key</code> header when making API requests:</p>
        <pre class="code-example">curl -H "X-API-Key: ${escapeHtml(apiKey)}" https://your-api-endpoint.com/api/...</pre>
      </div>
      <div class="modal-buttons">
        <button type="button" id="close-apikey-created-btn" class="primary">I've Copied the Key</button>
      </div>
    </div>
  `;

  document.body.appendChild(modal);
  openModal(modal);

  // Setup copy button
  const copyBtn = document.getElementById('copy-apikey-btn');
  if (copyBtn) {
    copyBtn.addEventListener('click', () => {
      navigator.clipboard.writeText(apiKey).then(() => {
        document.getElementById('apikey-copy-error')?.classList.add('hidden');
        copyBtn.textContent = 'Copied!';
        copyBtn.classList.add('copied');
        setTimeout(() => {
          copyBtn.textContent = 'Copy';
          copyBtn.classList.remove('copied');
        }, 2000);
      }).catch(err => {
        console.error('Failed to copy:', err);
        // Inline message instead of a native alert() (#705): select the key so
        // Ctrl+C / Cmd+C copies it without leaving the modal.
        const valueEl = document.getElementById('apikey-value');
        if (valueEl) window.getSelection()?.selectAllChildren(valueEl);
        const copyError = document.getElementById('apikey-copy-error');
        if (copyError) {
          copyError.textContent = 'Could not copy automatically. The key is selected: press Ctrl+C (Cmd+C on Mac) to copy it.';
          copyError.classList.remove('hidden');
        }
      });
    });
  }

  // Setup close button. closeModal first so the focus trap is torn
  // down and focus is restored to the original trigger; then remove
  // the dynamically-injected element from the DOM.
  const closeBtn = document.getElementById('close-apikey-created-btn');
  if (closeBtn) {
    closeBtn.addEventListener('click', () => {
      delete modal.dataset.requireAck;
      closeModal(modal);
      modal.remove();
    });
  }
}

/**
 * Initialize API keys management
 */
export function initApiKeys(): void {
  // Setup create key button
  const createKeyBtn = document.getElementById('create-apikey-btn');
  if (createKeyBtn) {
    createKeyBtn.addEventListener('click', () => void showCreateKeyModal());
  }

  // Setup close modal button
  const closeModalBtn = document.getElementById('close-create-apikey-modal-btn');
  if (closeModalBtn) {
    closeModalBtn.addEventListener('click', () => closeCreateKeyModal());
  }

  // Setup form submission
  const form = document.getElementById('create-apikey-form');
  if (form) {
    form.addEventListener('submit', (e) => void handleCreateApiKey(e));
  }

  // Close modal when clicking outside
  const modal = document.getElementById('create-apikey-modal');
  if (modal) {
    modal.addEventListener('click', (e) => {
      if (e.target === modal) closeCreateKeyModal();
    });
  }
}
