/**
 * Shared setup for the settings account suites (settings-accounts*.test.ts):
 * the module mocks, the mock handles and the DOM helpers. Import this module
 * FIRST so the mocks register before the suites load ../settings. This file is
 * not a test (jest only collects *.test.ts).
 */

jest.mock('../api', () => ({
  getConfig: jest.fn(),
  updateConfig: jest.fn(),
  listAccounts: jest.fn(),
  createAccount: jest.fn(),
  updateAccount: jest.fn(),
  deleteAccount: jest.fn(),
  testAccountCredentials: jest.fn(),
  saveAccountCredentials: jest.fn(),
  listAccountServiceOverrides: jest.fn(),
  saveAccountServiceOverride: jest.fn(),
  deleteAccountServiceOverride: jest.fn(),
  // Issue #606 — Cancel-All-Then-Delete UX calls cancelPurchase per
  // pending execution before retrying the account delete.
  cancelPurchase: jest.fn(),
  // Issue #734 — the cascade confirm lists each pending purchase.
  getPurchaseDetails: jest.fn()
}));

export const mockShowToast = jest.fn<{ dismiss: () => void }, [unknown]>(() => ({ dismiss: jest.fn() }));
jest.mock('../toast', () => ({
  showToast: (opts: unknown) => mockShowToast(opts)
}));

export const mockConfirmDialog = jest.fn<Promise<boolean>, [unknown]>();
jest.mock('../confirmDialog', () => ({
  confirmDialog: (opts: unknown) => mockConfirmDialog(opts)
}));

// Mock the recommendations module to keep the settings tests focused on
// the settings flows. Issue #196 wires settings.ts to call loadRecommendations
// after override mutations; without this mock the test bundle would pull in
// the full recommendations page module (~2k LOC) and its DOM expectations.
export const mockLoadRecommendations = jest.fn<Promise<void>, []>().mockResolvedValue(undefined);
jest.mock('../recommendations', () => ({
  loadRecommendations: () => mockLoadRecommendations()
}));

// ---------------------------------------------------------------------------
// DOM helpers
// ---------------------------------------------------------------------------

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs: Record<string, string> = {},
  id?: string
): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (id) e.id = id;
  Object.entries(attrs).forEach(([k, v]) => e.setAttribute(k, v));
  return e;
}

function input(id: string, type = 'text'): HTMLInputElement {
  return el('input', { type }, id) as HTMLInputElement;
}

function select(id: string, values: string[]): HTMLSelectElement {
  const s = el('select', {}, id) as HTMLSelectElement;
  values.forEach(v => {
    const opt = document.createElement('option');
    opt.value = v;
    opt.textContent = v;
    s.appendChild(opt);
  });
  return s;
}

function div(id: string, cls = ''): HTMLDivElement {
  const d = el('div', {}, id) as HTMLDivElement;
  if (cls) d.className = cls;
  return d;
}

function btn(id: string): HTMLButtonElement {
  return el('button', {}, id) as HTMLButtonElement;
}

/** Build the account-management DOM needed for these tests. */
export function buildAccountsDOM(): void {
  document.body.replaceChildren();

  // Account list containers
  document.body.appendChild(div('aws-accounts-list'));
  document.body.appendChild(div('azure-accounts-list'));
  document.body.appendChild(div('gcp-accounts-list'));

  // "Add account" buttons
  document.body.appendChild(btn('add-aws-account-btn'));
  document.body.appendChild(btn('add-azure-account-btn'));
  document.body.appendChild(btn('add-gcp-account-btn'));

  // ── Account modal ──────────────────────────────────────────
  const modal = div('account-modal', 'hidden');

  const title = el('h3', {}, 'account-modal-title');
  modal.appendChild(title);

  const form = el('form', {}, 'account-form');

  form.appendChild(input('account-id', 'hidden'));
  form.appendChild(input('account-provider', 'hidden'));
  form.appendChild(input('account-name'));
  const desc = el('textarea', {}, 'account-description') as HTMLTextAreaElement;
  form.appendChild(desc);
  form.appendChild(input('account-contact-email', 'email'));
  form.appendChild(input('account-external-id'));
  const enabledCb = input('account-enabled', 'checkbox');
  enabledCb.checked = true;
  const enabledRow = div('account-enabled-row');
  enabledRow.appendChild(enabledCb);
  form.appendChild(enabledRow);

  // AWS fields
  const awsFields = div('account-aws-fields');
  const authModeSelect = select('account-aws-auth-mode', ['access_keys', 'role_arn', 'bastion']);
  awsFields.appendChild(authModeSelect);

  const keysDiv = div('account-aws-keys-fields');
  keysDiv.appendChild(input('account-aws-access-key-id'));
  keysDiv.appendChild(input('account-aws-secret-access-key', 'password'));
  awsFields.appendChild(keysDiv);

  const roleDiv = div('account-aws-role-fields', 'hidden');
  roleDiv.appendChild(input('account-aws-role-arn'));
  roleDiv.appendChild(input('account-aws-external-id'));
  awsFields.appendChild(roleDiv);

  const bastionDiv = div('account-aws-bastion-fields', 'hidden');
  bastionDiv.appendChild(select('account-aws-bastion-id', ['']));
  bastionDiv.appendChild(input('account-aws-bastion-role-arn'));
  bastionDiv.appendChild(input('account-aws-external-id-bastion'));
  bastionDiv.appendChild(el('pre', {}, 'account-aws-trust-policy-bastion'));
  bastionDiv.appendChild(el('small', {}, 'account-aws-trust-policy-bastion-hint'));
  awsFields.appendChild(bastionDiv);

  const orgRootCb = input('account-aws-is-org-root', 'checkbox');
  awsFields.appendChild(orgRootCb);
  form.appendChild(awsFields);

  // Azure fields
  const azureFields = div('account-azure-fields', 'hidden');
  azureFields.appendChild(input('account-azure-tenant-id'));
  azureFields.appendChild(input('account-azure-client-id'));
  azureFields.appendChild(input('account-azure-client-secret', 'password'));
  form.appendChild(azureFields);

  // GCP fields
  const gcpFields = div('account-gcp-fields', 'hidden');
  const gcpJson = el('textarea', {}, 'account-gcp-service-account-json') as HTMLTextAreaElement;
  gcpFields.appendChild(gcpJson);
  form.appendChild(gcpFields);

  modal.appendChild(form);
  modal.appendChild(btn('close-account-modal-btn'));
  document.body.appendChild(modal);

  // ── Override modal (issue #104, bulk multi-service mode issue #119) ───
  const overrideModal = div('override-modal', 'modal hidden');
  const overrideForm = el('form', {}, 'override-form');
  overrideForm.appendChild(input('override-account-id', 'hidden'));
  overrideForm.appendChild(input('override-provider', 'hidden'));
  // Single-service row (default mode).
  const singleRow = div('override-single-service-row');
  singleRow.appendChild(select('override-service', []));
  overrideForm.appendChild(singleRow);
  // Bulk-mode toggle (issue #119).
  const bulkToggle = input('override-bulk-toggle', 'checkbox');
  overrideForm.appendChild(bulkToggle);
  // Bulk services container (hidden by default).
  const bulkServicesDiv = div('override-bulk-services', 'hidden');
  const bulkServicesList = div('override-bulk-services-list');
  bulkServicesDiv.appendChild(bulkServicesList);
  overrideForm.appendChild(bulkServicesDiv);
  overrideForm.appendChild(select('override-term', ['', '1', '3']));
  overrideForm.appendChild(select('override-payment', ['', 'no-upfront', 'partial-upfront', 'all-upfront']));
  overrideForm.appendChild(input('override-coverage', 'number'));
  const overrideErr = el('p', {}, 'override-form-error');
  overrideForm.appendChild(overrideErr);
  const overrideSubmit = el('button', { type: 'submit' }, 'override-submit-btn') as HTMLButtonElement;
  overrideSubmit.textContent = 'Save override';
  overrideForm.appendChild(overrideSubmit);
  overrideModal.appendChild(overrideForm);
  overrideModal.appendChild(btn('close-override-modal-btn'));
  document.body.appendChild(overrideModal);

  // ── Per-account overrides modal (issue #122) ───────────────
  const accountOverridesModal = div('account-overrides-modal', 'modal hidden');
  const accountOverridesTitle = el('h2', {}, 'account-overrides-modal-title');
  accountOverridesTitle.textContent = 'Service overrides';
  accountOverridesModal.appendChild(accountOverridesTitle);
  accountOverridesModal.appendChild(div('account-overrides-modal-body'));
  accountOverridesModal.appendChild(btn('close-account-overrides-modal-btn'));
  document.body.appendChild(accountOverridesModal);
}
