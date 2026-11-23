/**
 * Shared setup for the settings External ID suite: the module mocks, the account
 * modal DOM builder and the Map-backed sessionStorage shim. Import this module
 * FIRST so the mocks register before the suite loads ../settings. This file is
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
}));

jest.mock('../toast', () => ({
  showToast: () => ({ dismiss: () => undefined }),
}));

jest.mock('../confirmDialog', () => ({
  confirmDialog: jest.fn(),
}));

// ---------------------------------------------------------------------------
// DOM helpers (subset of settings-accounts.test.ts — kept private so the
// two suites stay independent).
// ---------------------------------------------------------------------------

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs: Record<string, string> = {},
  id?: string,
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

export function buildAccountsDOM(): void {
  document.body.replaceChildren();

  document.body.appendChild(div('aws-accounts-list'));
  document.body.appendChild(div('azure-accounts-list'));
  document.body.appendChild(div('gcp-accounts-list'));
  document.body.appendChild(btn('add-aws-account-btn'));
  document.body.appendChild(btn('add-azure-account-btn'));
  document.body.appendChild(btn('add-gcp-account-btn'));

  const modal = div('account-modal', 'hidden');
  modal.appendChild(el('h3', {}, 'account-modal-title'));

  const form = el('form', {}, 'account-form');
  form.appendChild(input('account-id', 'hidden'));
  form.appendChild(input('account-provider', 'hidden'));
  form.appendChild(input('account-name'));
  form.appendChild(el('textarea', {}, 'account-description') as HTMLTextAreaElement);
  form.appendChild(input('account-contact-email', 'email'));
  form.appendChild(input('account-external-id'));
  const enabledCb = input('account-enabled', 'checkbox');
  enabledCb.checked = true;
  form.appendChild(enabledCb);

  const awsFields = div('account-aws-fields');
  awsFields.appendChild(select('account-aws-auth-mode', ['access_keys', 'role_arn', 'bastion', 'workload_identity_federation']));

  const keysDiv = div('account-aws-keys-fields');
  keysDiv.appendChild(input('account-aws-access-key-id'));
  keysDiv.appendChild(input('account-aws-secret-access-key', 'password'));
  awsFields.appendChild(keysDiv);

  const roleDiv = div('account-aws-role-fields', 'hidden');
  roleDiv.appendChild(input('account-aws-role-arn'));
  roleDiv.appendChild(input('account-aws-external-id'));
  roleDiv.appendChild(el('pre', {}, 'account-aws-trust-policy'));
  roleDiv.appendChild(el('small', {}, 'account-aws-trust-policy-hint'));
  awsFields.appendChild(roleDiv);

  const bastionDiv = div('account-aws-bastion-fields', 'hidden');
  bastionDiv.appendChild(select('account-aws-bastion-id', ['']));
  bastionDiv.appendChild(input('account-aws-bastion-role-arn'));
  bastionDiv.appendChild(input('account-aws-external-id-bastion'));
  bastionDiv.appendChild(el('pre', {}, 'account-aws-trust-policy-bastion'));
  bastionDiv.appendChild(el('small', {}, 'account-aws-trust-policy-bastion-hint'));
  awsFields.appendChild(bastionDiv);

  const wifDiv = div('account-aws-wif-fields', 'hidden');
  wifDiv.appendChild(input('account-aws-wif-role-arn'));
  wifDiv.appendChild(input('account-aws-wif-token-file'));
  awsFields.appendChild(wifDiv);

  awsFields.appendChild(input('account-aws-is-org-root', 'checkbox'));
  form.appendChild(awsFields);

  const azureFields = div('account-azure-fields', 'hidden');
  form.appendChild(azureFields);
  const gcpFields = div('account-gcp-fields', 'hidden');
  form.appendChild(gcpFields);

  modal.appendChild(form);
  modal.appendChild(btn('close-account-modal-btn'));
  document.body.appendChild(modal);
}

// installRealSessionStorage swaps the global jest mock (setup.ts) for a
// Map-backed shim that actually persists writes across calls within a
// single test. The setup mock is intentionally inert (every getItem
// returns null) so it doesn't satisfy the issue #126 stable-draft
// invariant — these tests need a real-ish backing store.
export function installRealSessionStorage(): void {
  const store = new Map<string, string>();
  Object.defineProperty(global, 'sessionStorage', {
    value: {
      getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
      setItem: (k: string, v: string) => { store.set(k, String(v)); },
      removeItem: (k: string) => { store.delete(k); },
      clear: () => { store.clear(); },
      get length() { return store.size; },
      key: (i: number) => Array.from(store.keys())[i] ?? null,
    },
    writable: true,
    configurable: true,
  });
}
