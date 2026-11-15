import * as api from '../api';
import { showToast } from '../toast';
import { openModal, closeModal } from '../modal';
import { loadAccountsForProvider } from '../settings';
import { renderAwsBastionTrustPolicy, renderAwsTrustPolicy } from './settings-aws-trust';
import { byId, setInputChecked, setInputValue } from './settings-fields';
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
let accountModalProvider: AccountProvider = 'aws';

// Optional callback for the registrations approval flow.
let accountModalOnSave: AccountModalOptions['onSave'] | undefined;

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
function resolveAwsExternalIdForModal(account?: api.CloudAccount): string {
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
function clearAwsExternalIdDraft(): void {
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

function populateAwsAccountFields(account?: api.CloudAccount): void {
  const authMode = (account?.aws_auth_mode ?? 'workload_identity_federation') as string;
  const authModeSelect = document.getElementById('account-aws-auth-mode') as HTMLSelectElement | null;
  if (authModeSelect) authModeSelect.value = authMode;

  setInputValue('account-aws-access-key-id', '');
  setInputValue('account-aws-secret-access-key', '');
  // aws_role_arn is the backend column used by all three auth modes that need
  // a role: role_arn (direct), bastion (target role assumed via bastion creds),
  // and workload_identity_federation (role assumed via OIDC). Only one of
  // these fields is visible at a time (see updateAwsAuthModeFields), and the
  // save path writes whichever one is visible back to req.aws_role_arn. We
  // pre-fill all three with the same stored value so switching modes mid-edit
  // doesn't blank the input that just became visible.
  setInputValue('account-aws-role-arn', account?.aws_role_arn ?? '');
  // External ID is a CUDly-managed shared secret for cross-account role
  // assumption (issue #18). On edit, keep whatever was stored before; on
  // create, generate a value once and persist it in sessionStorage so a
  // close/reopen cycle returns the same value (issue #126). The field
  // itself is marked readonly in index.html so users can't hand-craft
  // values that would defeat the purpose.
  // Bastion mode shares the same draft External ID value as role_arn mode
  // (issue #129) — both are AWS sts:ExternalId values used as a confused-
  // deputy guard. Sharing the draft key keeps a single source of truth so
  // toggling auth modes mid-edit doesn't surface diverging values, and the
  // generator semantics are identical (CSPRNG + sessionStorage persistence
  // until save). The two readonly inputs are wired to the same value.
  const externalID = resolveAwsExternalIdForModal(account);
  setInputValue('account-aws-external-id', externalID);
  setInputValue('account-aws-external-id-bastion', externalID);
  setInputValue('account-aws-bastion-role-arn', account?.aws_role_arn ?? '');
  setInputValue('account-aws-wif-role-arn', account?.aws_role_arn ?? '');
  setInputValue('account-aws-wif-token-file', account?.aws_web_identity_token_file ?? '');
  setInputChecked('account-aws-is-org-root', account?.aws_is_org_root ?? false);

  updateAwsAuthModeFields(authMode, account?.aws_bastion_id);

  // Render the trust-policy snippet asynchronously — we need the CUDly
  // host account ID from getConfig.source_identity (issue #19). Kept
  // void so the modal doesn't block waiting for the network. The bastion
  // variant (issue #129) is also rendered eagerly so it's ready when the
  // operator switches to that mode mid-edit.
  void renderAwsTrustPolicy();
  void renderAwsBastionTrustPolicy();
}

/**
 * Show/hide AWS auth mode sub-fields
 */
export function updateAwsAuthModeFields(mode: string, bastionId?: string): void {
  const keysFields = document.getElementById('account-aws-keys-fields');
  const roleFields = document.getElementById('account-aws-role-fields');
  const bastionFields = document.getElementById('account-aws-bastion-fields');
  const wifFields = document.getElementById('account-aws-wif-fields');

  keysFields?.classList.toggle('hidden', mode !== 'access_keys');
  roleFields?.classList.toggle('hidden', mode !== 'role_arn');
  bastionFields?.classList.toggle('hidden', mode !== 'bastion');
  wifFields?.classList.toggle('hidden', mode !== 'workload_identity_federation');

  if (mode === 'bastion') {
    // Refresh the dropdown then re-render the trust-policy snippet so
    // a freshly-selected bastion is reflected in the Principal (#129).
    void populateBastionAccountDropdown(bastionId).then(() => renderAwsBastionTrustPolicy());
  }
}

/**
 * Show/hide Azure auth mode sub-fields. Only client_secret mode has an
 * input block; workload_identity_federation is credential-free (CUDly's
 * OIDC issuer + the App Registration's federated-identity-credential
 * handle authentication) and managed_identity is ambient, so neither
 * needs a visible field block here.
 */
export function updateAzureAuthModeFields(mode: string): void {
  const secretFields = document.getElementById('account-azure-secret-fields');
  secretFields?.classList.toggle('hidden', mode !== 'client_secret');
}

/**
 * Show/hide GCP auth mode sub-fields
 */
export function updateGCPAuthModeFields(mode: string): void {
  const keyFields = document.getElementById('account-gcp-key-fields');
  const wifFields = document.getElementById('account-gcp-wif-fields');
  keyFields?.classList.toggle('hidden', mode !== 'service_account_key');
  wifFields?.classList.toggle('hidden', mode !== 'workload_identity_federation');
}

/**
 * Populate bastion account dropdown with AWS accounts, optionally pre-selecting one
 */
async function populateBastionAccountDropdown(selectedId?: string): Promise<void> {
  const select = document.getElementById('account-aws-bastion-id') as HTMLSelectElement | null;
  if (!select) return;
  try {
    const accounts = await api.listAccounts({ provider: 'aws' });
    while (select.options.length > 1) select.remove(1);
    accounts.forEach(a => {
      const opt = document.createElement('option');
      opt.value = a.id;
      opt.textContent = `${a.name} (${a.external_id})`;
      select.appendChild(opt);
    });
    if (selectedId) select.value = selectedId;
  } catch {
    // Non-critical
  }
}

/**
 * Build the account request from the modal form
 */
function buildAccountRequest(provider: AccountProvider): api.CloudAccountRequest {
  const req: api.CloudAccountRequest = {
    name: (byId<HTMLInputElement>('account-name')?.value ?? '').trim(),
    description: (byId<HTMLTextAreaElement>('account-description')?.value ?? '').trim() || undefined,
    contact_email: (byId<HTMLInputElement>('account-contact-email')?.value ?? '').trim() || undefined,
    provider,
    external_id: (byId<HTMLInputElement>('account-external-id')?.value ?? '').trim(),
    enabled: byId<HTMLInputElement>('account-enabled')?.checked ?? true,
  };

  if (provider === 'aws') {
    const authMode = byId<HTMLSelectElement>('account-aws-auth-mode')?.value ?? '';
    req.aws_auth_mode = authMode;
    req.aws_is_org_root = byId<HTMLInputElement>('account-aws-is-org-root')?.checked ?? false;
    if (authMode === 'role_arn') {
      req.aws_role_arn = (byId<HTMLInputElement>('account-aws-role-arn')?.value ?? '').trim();
      req.aws_external_id = (byId<HTMLInputElement>('account-aws-external-id')?.value ?? '').trim() || undefined;
    } else if (authMode === 'bastion') {
      req.aws_bastion_id = byId<HTMLSelectElement>('account-aws-bastion-id')?.value ?? '';
      req.aws_role_arn = (byId<HTMLInputElement>('account-aws-bastion-role-arn')?.value ?? '').trim();
      // Collect the External ID for bastion mode too (issue #129) — the
      // bastion's STS client calls AssumeRole into the target role just
      // like role_arn mode, so the same sts:ExternalId guard applies.
      req.aws_external_id = (byId<HTMLInputElement>('account-aws-external-id-bastion')?.value ?? '').trim() || undefined;
    } else if (authMode === 'workload_identity_federation') {
      req.aws_role_arn = byId<HTMLInputElement>('account-aws-wif-role-arn')?.value.trim();
      req.aws_web_identity_token_file = byId<HTMLInputElement>('account-aws-wif-token-file')?.value.trim() || undefined;
    }
  } else if (provider === 'azure') {
    req.azure_subscription_id = req.external_id; // external_id IS the subscription ID for Azure
    req.azure_auth_mode = byId<HTMLSelectElement>('account-azure-auth-mode')?.value || 'client_secret';
    req.azure_tenant_id = (byId<HTMLInputElement>('account-azure-tenant-id')?.value ?? '').trim();
    req.azure_client_id = (byId<HTMLInputElement>('account-azure-client-id')?.value ?? '').trim();
  } else if (provider === 'gcp') {
    req.gcp_project_id = req.external_id; // external_id IS the project ID for GCP
    req.gcp_auth_mode = byId<HTMLSelectElement>('account-gcp-auth-mode')?.value || 'service_account_key';
    req.gcp_client_email = byId<HTMLInputElement>('account-gcp-client-email')?.value.trim() ?? '';
  }

  return req;
}

/**
 * Build and save credentials from the account modal form (if filled)
 */
async function saveAccountCredentialsIfFilled(accountId: string, provider: AccountProvider): Promise<void> {
  if (provider === 'aws') {
    const authMode = byId<HTMLSelectElement>('account-aws-auth-mode')?.value ?? '';
    if (authMode === 'access_keys') {
      const keyId = (byId<HTMLInputElement>('account-aws-access-key-id')?.value ?? '').trim();
      const secretKey = byId<HTMLInputElement>('account-aws-secret-access-key')?.value ?? '';
      if (keyId && secretKey) {
        await api.saveAccountCredentials(accountId, {
          credential_type: 'aws_access_keys',
          payload: { access_key_id: keyId, secret_access_key: secretKey }
        });
      }
    }
  } else if (provider === 'azure') {
    const azureMode = byId<HTMLSelectElement>('account-azure-auth-mode')?.value ?? 'client_secret';
    // managed_identity is ambient; workload_identity_federation is
    // secret-free (CUDly's OIDC issuer + the Azure App Registration's
    // federated-identity-credential handle authentication, no material
    // stored in CUDly). Only client_secret accepts a stored credential.
    if (azureMode === 'client_secret') {
      const secret = byId<HTMLInputElement>('account-azure-client-secret')?.value ?? '';
      if (secret) {
        await api.saveAccountCredentials(accountId, {
          credential_type: 'azure_client_secret',
          payload: { client_secret: secret }
        });
      }
    }
  } else if (provider === 'gcp') {
    const gcpMode = byId<HTMLSelectElement>('account-gcp-auth-mode')?.value ?? 'service_account_key';
    if (gcpMode === 'application_default') {
      // No credential to store
    } else if (gcpMode === 'workload_identity_federation') {
      const config = byId<HTMLTextAreaElement>('account-gcp-wif-config')?.value.trim();
      if (config) {
        let parsed: Record<string, unknown>;
        try {
          parsed = JSON.parse(config) as Record<string, unknown>;
        } catch {
          throw new Error('GCP WIF config is not valid JSON');
        }
        await api.saveAccountCredentials(accountId, { credential_type: 'gcp_workload_identity_config', payload: parsed });
      }
    } else {
      const jsonText = byId<HTMLTextAreaElement>('account-gcp-service-account-json')?.value.trim() ?? '';
      if (jsonText) {
        let parsed: Record<string, unknown>;
        try {
          parsed = JSON.parse(jsonText) as Record<string, unknown>;
        } catch {
          throw new Error('Service account JSON is not valid JSON');
        }
        await api.saveAccountCredentials(accountId, { credential_type: 'gcp_service_account', payload: parsed });
      }
    }
  }
}

/**
 * Handle account form submission
 */
export async function handleAccountFormSubmit(e: Event): Promise<void> {
  e.preventDefault();
  const submitBtn = document.querySelector<HTMLButtonElement>('#account-form button[type="submit"]');
  if (submitBtn) submitBtn.disabled = true;
  try {
    await submitAccountForm();
  } finally {
    if (submitBtn) submitBtn.disabled = false;
  }
}

async function submitAccountForm(): Promise<void> {
  const provider = accountModalProvider;
  const req = buildAccountRequest(provider);

  // If a custom onSave callback was provided (e.g., registration approval),
  // delegate to it instead of the normal create/update flow.
  if (accountModalOnSave) {
    try {
      await accountModalOnSave(provider, req);
      closeAccountModal();
    } catch (err) {
      console.error('Custom save failed:', err);
      showToast({ message: `Failed to save: ${(err as Error).message}`, kind: 'error' });
    }
    return;
  }

  const accountId = byId<HTMLInputElement>('account-id')?.value ?? '';

  try {
    let savedId = accountId;
    if (accountId) {
      await api.updateAccount(accountId, req);
    } else {
      const created = await api.createAccount(req);
      savedId = created.id;
    }

    // Account persisted — drop the in-progress AWS External ID draft so
    // the next "Add AWS Account" flow generates a fresh value (issue #126).
    if (provider === 'aws') {
      clearAwsExternalIdDraft();
    }

    // Credential save is best-effort: if it fails we still close the modal
    // (the account was already persisted) and show a targeted warning so the
    // user knows to retry just the credential step.
    try {
      await saveAccountCredentialsIfFilled(savedId, provider);
    } catch (credErr) {
      console.error('Account saved but credentials could not be stored:', credErr);
      closeAccountModal();
      await loadAccountsForProvider(provider);
      showToast({
        message: `Account saved, but credentials could not be stored: ${(credErr as Error).message}. Edit the account to re-enter credentials.`,
        kind: 'warning',
        timeout: null,
      });
      return;
    }

    closeAccountModal();
    await loadAccountsForProvider(provider);
  } catch (err) {
    console.error('Failed to save account:', err);
    showToast({ message: `Failed to save account: ${(err as Error).message}`, kind: 'error' });
  }
}

/**
 * Trigger org account discovery from the management (org root) account
 */
export async function handleDiscoverOrgAccounts(): Promise<void> {
  try {
    const result = await api.discoverOrgAccounts();
    showToast({ message: `Org discovery started: ${result.message || result.status}`, kind: 'success' });
    await loadAccountsForProvider('aws');
  } catch (err) {
    console.error('Org discovery failed:', err);
    showToast({ message: `Org discovery failed: ${(err as Error).message}`, kind: 'error' });
  }
}
