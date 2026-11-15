import * as api from '../api';
import { openModal, closeModal } from '../modal';
import { populateAwsAccountFields, updateAzureAuthModeFields, updateGCPAuthModeFields } from '../settings';
import { setInputChecked, setInputValue } from './settings-fields';
import type { AccountProvider } from './settings-state';

// sessionStorage key for the in-progress (unsaved) AWS External ID. Persists
// across modal close/reopen cycles within the same session so the operator
// can copy the value into AWS, close the modal, and come back to the same
// value (issue #126). Cleared on successful save.
const AWS_DRAFT_EXTERNAL_ID_KEY = 'cudly:aws-account-modal:draft-external-id';

/** Options for the account modal (used by the registrations approval flow). */
export interface AccountModalOptions {
  onSave?: (provider: AccountProvider, request: api.CloudAccountRequest) => Promise<void>;
}

// Track which provider's account is being edited (for the modal)
export let accountModalProvider: AccountProvider = 'aws';

// Optional callback for the registrations approval flow.
export let accountModalOnSave: AccountModalOptions['onSave'] | undefined;

/**
 * Open account modal for add or edit
 */
export function openAccountModal(provider: AccountProvider, account?: api.CloudAccount, options?: AccountModalOptions): void {
  accountModalProvider = provider;
  accountModalOnSave = options?.onSave;
  const modal = document.getElementById('account-modal');
  if (!modal) return;

  const title = document.getElementById('account-modal-title');
  if (title) title.textContent = account ? 'Edit account' : 'Add account';

  setInputValue('account-id', account?.id ?? '');
  setInputValue('account-provider', provider);
  setInputValue('account-name', account?.name ?? '');
  setInputValue('account-description', account?.description ?? '');
  setInputValue('account-contact-email', account?.contact_email ?? '');
  setInputValue('account-external-id', account?.external_id ?? '');
  setInputChecked('account-enabled', account?.enabled ?? true);
  // Approving a registration auto-enables the account server-side, so the toggle would be a no-op there.
  document.getElementById('account-enabled-row')?.classList.toggle('hidden', !!options?.onSave);

  // Show/hide provider-specific fields
  const awsFields = document.getElementById('account-aws-fields');
  const azureFields = document.getElementById('account-azure-fields');
  const gcpFields = document.getElementById('account-gcp-fields');
  if (awsFields) awsFields.classList.toggle('hidden', provider !== 'aws');
  if (azureFields) azureFields.classList.toggle('hidden', provider !== 'azure');
  if (gcpFields) gcpFields.classList.toggle('hidden', provider !== 'gcp');

  if (provider === 'aws') {
    populateAwsAccountFields(account);
  } else if (provider === 'azure') {
    const azureAuthMode = account?.azure_auth_mode ?? 'workload_identity_federation';
    const azureAuthSelect = document.getElementById('account-azure-auth-mode') as HTMLSelectElement | null;
    if (azureAuthSelect) azureAuthSelect.value = azureAuthMode;
    setInputValue('account-azure-tenant-id', account?.azure_tenant_id ?? '');
    setInputValue('account-azure-client-id', account?.azure_client_id ?? '');
    setInputValue('account-azure-client-secret', '');
    updateAzureAuthModeFields(azureAuthMode);
  } else if (provider === 'gcp') {
    const gcpMode = account?.gcp_auth_mode ?? 'workload_identity_federation';
    const gcpAuthSelect = document.getElementById('account-gcp-auth-mode') as HTMLSelectElement | null;
    if (gcpAuthSelect) gcpAuthSelect.value = gcpMode;
    const gcpEmailEl = document.getElementById('account-gcp-client-email') as HTMLInputElement | null;
    if (gcpEmailEl) gcpEmailEl.value = account?.gcp_client_email ?? '';
    const gcpJsonEl = document.getElementById('account-gcp-service-account-json') as HTMLTextAreaElement | null;
    if (gcpJsonEl) gcpJsonEl.value = '';
    const wifConfig = document.getElementById('account-gcp-wif-config') as HTMLTextAreaElement | null;
    if (wifConfig) wifConfig.value = '';
    updateGCPAuthModeFields(gcpMode);
  }

  openModal(modal);
}

/**
 * Populate AWS-specific fields in the account modal
 */
function generateExternalID(): string {
  // crypto.randomUUID is available in all supported browsers (Chrome
  // 92+, Firefox 95+, Safari 15.4+). Fall back to crypto.getRandomValues
  // (a strict superset of randomUUID's availability) for older runtimes.
  // The External ID is a security primitive that protects against the
  // confused-deputy problem on cross-account sts:AssumeRole, so the
  // fallback MUST also be a CSPRNG — never Math.random() (issue #127).
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  if (typeof crypto !== 'undefined' && typeof crypto.getRandomValues === 'function') {
    const bytes = new Uint8Array(16);
    crypto.getRandomValues(bytes);
    return Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
  }
  // Last-resort fallback: a runtime with no Web Crypto at all (jsdom
  // without polyfill, very old webviews). Not security-secret-quality,
  // but "never empty" matters for the form to submit and the backend
  // length/charset validator (issue #128) will then catch obviously-
  // weak inputs. Math.random is intentionally NOT used here — see #127.
  return `cudly-fallback-${Date.now().toString(36)}`;
}

/**
 * Return the AWS External ID for the current modal session.
 *
 * - When editing an existing account: always use the persisted value.
 * - When creating a new account: reuse the same draft value across modal
 *   close/reopen cycles so an operator who copied the ID into their IAM
 *   trust policy and then reopens the modal sees the same value (issue
 *   #126). The draft is keyed in sessionStorage (cleared on tab close)
 *   and is reset on a successful save.
 *
 * Falls back to in-memory generation if sessionStorage is unavailable
 * (e.g., privacy-mode browsers, tests with no Storage shim) — that path
 * preserves the legacy regenerate-on-reopen behaviour but never breaks.
 */
export function resolveAwsExternalIdForModal(account?: api.CloudAccount): string {
  if (account?.aws_external_id) return account.aws_external_id;

  let storage: Storage | undefined;
  try {
    storage = typeof sessionStorage !== 'undefined' ? sessionStorage : undefined;
  } catch {
    storage = undefined;
  }

  if (storage) {
    try {
      const existing = storage.getItem(AWS_DRAFT_EXTERNAL_ID_KEY);
      if (existing) return existing;
      const fresh = generateExternalID();
      storage.setItem(AWS_DRAFT_EXTERNAL_ID_KEY, fresh);
      return fresh;
    } catch {
      // Quota / permission errors — fall through to in-memory generation.
    }
  }
  return generateExternalID();
}

/** Clear the in-progress AWS External ID draft. Called after successful save. */
export function clearAwsExternalIdDraft(): void {
  try {
    if (typeof sessionStorage !== 'undefined') {
      sessionStorage.removeItem(AWS_DRAFT_EXTERNAL_ID_KEY);
    }
  } catch {
    // sessionStorage unavailable — nothing to clear.
  }
}

/**
 * Close the account modal
 */
export function closeAccountModal(): void {
  const modal = document.getElementById('account-modal');
  if (modal) closeModal(modal);
  accountModalOnSave = undefined;
}
