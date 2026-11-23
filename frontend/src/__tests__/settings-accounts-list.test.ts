/**
 * Settings account tests: account list, account modal and account form submit.
 * Split from settings-accounts.test.ts to stay under the 500-line ceiling; the
 * module mocks and DOM helpers live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  mockShowToast,
  buildAccountsDOM,
} from './settings-accounts-harness';
import {
  loadAccountsForProvider,
  openAccountModal,
  setupSettingsHandlers,
} from '../settings';

import * as api from '../api';

// ---------------------------------------------------------------------------
// loadAccountsForProvider
// ---------------------------------------------------------------------------

describe('loadAccountsForProvider', () => {
  beforeEach(() => {
    buildAccountsDOM();
    jest.clearAllMocks();
  });

  test('renders account rows when API returns accounts', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'id1', name: 'Prod',    external_id: '111111111111', enabled: true },
      { id: 'id2', name: 'Dev',     external_id: '222222222222', enabled: false }
    ]);

    await loadAccountsForProvider('aws');

    const container = document.getElementById('aws-accounts-list')!;
    // P3 replaced inline .account-row divs with an accounts-table.
    const table = container.querySelector('table.accounts-table');
    expect(table).not.toBeNull();
    expect(table?.querySelectorAll('tbody tr')).toHaveLength(2);
    expect(container.textContent).toContain('Prod');
    expect(container.textContent).toContain('Disabled');
  });

  test('action labels do not repeat the ID when the name already contains it (#1691)', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'id1', name: 'CUDly host (111111111111)', external_id: '111111111111', enabled: true },
      { id: 'id2', name: 'Dev', external_id: '222222222222', enabled: true }
    ]);

    await loadAccountsForProvider('aws');

    const labels = Array.from(
      document.getElementById('aws-accounts-list')!.querySelectorAll('tbody button[aria-label]')
    ).map(b => b.getAttribute('aria-label')!);
    expect(labels).toContain('Edit CUDly host (111111111111)');
    expect(labels).toContain('Delete CUDly host (111111111111)');
    expect(labels.some(l => l.includes('(111111111111) (111111111111)'))).toBe(false);
    expect(labels).toContain('Edit Dev (222222222222)');
  });

  test('renders "No accounts configured." for empty list', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([]);

    await loadAccountsForProvider('aws');

    // P3 wraps the empty copy in a dedicated <p.accounts-empty>.
    const container = document.getElementById('aws-accounts-list')!;
    expect(container.querySelector('.accounts-empty')?.textContent).toBe('No accounts configured.');
  });

  test('renders error message on API failure', async () => {
    (api.listAccounts as jest.Mock).mockRejectedValue(new Error('Network error'));

    await loadAccountsForProvider('azure');

    expect(document.getElementById('azure-accounts-list')!.textContent).toBe('Failed to load accounts.');
  });

  test('does nothing when container element is missing', async () => {
    document.body.replaceChildren();
    (api.listAccounts as jest.Mock).mockResolvedValue([]);

    await expect(loadAccountsForProvider('aws')).resolves.toBeUndefined();
    expect(api.listAccounts).not.toHaveBeenCalled();
  });

  test('passes provider filter to listAccounts', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([]);

    await loadAccountsForProvider('gcp');

    expect(api.listAccounts).toHaveBeenCalledWith({ provider: 'gcp' });
  });

  test('renders status-chip row with counts and All active by default', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: '1', name: 'Prod', provider: 'aws', external_id: '111', enabled: true },
      { id: '2', name: 'Stage', provider: 'aws', external_id: '222', enabled: false },
      { id: '3', name: 'Dev', provider: 'aws', external_id: '333', enabled: true },
    ]);

    await loadAccountsForProvider('aws');

    const container = document.getElementById('aws-accounts-list')!;
    const chips = Array.from(container.querySelectorAll('.status-chip'));
    expect(chips).toHaveLength(3);
    expect(chips[0]!.textContent).toBe('All (3)');
    expect(chips[1]!.textContent).toBe('Active (2)');
    expect(chips[2]!.textContent).toBe('Disabled (1)');
    expect(chips[0]!.classList.contains('active')).toBe(true);
    expect(chips[1]!.classList.contains('active')).toBe(false);
    expect(container.querySelectorAll('tbody tr')).toHaveLength(3);
  });

  test('clicking Active chip filters out disabled rows', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: '1', name: 'Prod', provider: 'aws', external_id: '111', enabled: true },
      { id: '2', name: 'Stage', provider: 'aws', external_id: '222', enabled: false },
    ]);

    await loadAccountsForProvider('aws');
    const container = document.getElementById('aws-accounts-list')!;
    const chips = Array.from(container.querySelectorAll('.status-chip')) as HTMLButtonElement[];
    chips[1]!.click();

    const rows = Array.from(container.querySelectorAll('tbody tr'));
    expect(rows).toHaveLength(1);
    expect(rows[0]!.textContent).toContain('Prod');
    expect(rows[0]!.textContent).not.toContain('Stage');
    const afterChips = Array.from(container.querySelectorAll('.status-chip')) as HTMLButtonElement[];
    expect(afterChips[1]!.classList.contains('active')).toBe(true);
  });

  test('clicking All restores full row set after a filter', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: '1', name: 'Prod', provider: 'aws', external_id: '111', enabled: true },
      { id: '2', name: 'Stage', provider: 'aws', external_id: '222', enabled: false },
    ]);

    await loadAccountsForProvider('aws');
    const container = document.getElementById('aws-accounts-list')!;
    const chips = () => Array.from(container.querySelectorAll('.status-chip')) as HTMLButtonElement[];
    chips()[2]!.click();
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1);
    chips()[0]!.click();
    expect(container.querySelectorAll('tbody tr')).toHaveLength(2);
  });
});

// ---------------------------------------------------------------------------
// Account modal (opened via setupSettingsHandlers)
// ---------------------------------------------------------------------------

describe('Account modal via setupSettingsHandlers', () => {
  beforeEach(() => {
    buildAccountsDOM();
    jest.clearAllMocks();
    (api.listAccounts as jest.Mock).mockResolvedValue([]);
    setupSettingsHandlers();
  });

  test('add-aws-account-btn opens modal as "Add Account"', () => {
    document.getElementById('add-aws-account-btn')!.click();

    expect(document.getElementById('account-modal')!.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('account-modal-title')!.textContent).toBe('Add account');
  });

  test('add-azure-account-btn shows azure fields, hides aws fields', () => {
    document.getElementById('add-azure-account-btn')!.click();

    expect(document.getElementById('account-modal')!.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('account-azure-fields')!.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('account-aws-fields')!.classList.contains('hidden')).toBe(true);
  });

  test('add-gcp-account-btn shows gcp fields, hides aws fields', () => {
    document.getElementById('add-gcp-account-btn')!.click();

    expect(document.getElementById('account-gcp-fields')!.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('account-aws-fields')!.classList.contains('hidden')).toBe(true);
  });

  test('approve-registration modal (onSave) hides the Enabled toggle; add and edit keep it (#1776)', () => {
    const row = document.getElementById('account-enabled-row')!;
    openAccountModal('aws', undefined, { onSave: async () => {} });
    expect(row.classList.contains('hidden')).toBe(true);
    openAccountModal('aws');
    expect(row.classList.contains('hidden')).toBe(false);
  });

  test('close button hides account modal', () => {
    document.getElementById('add-aws-account-btn')!.click();
    document.getElementById('close-account-modal-btn')!.click();
    expect(document.getElementById('account-modal')!.classList.contains('hidden')).toBe(true);
  });

  test('clicking modal backdrop closes it', () => {
    document.getElementById('add-aws-account-btn')!.click();
    const modal = document.getElementById('account-modal')!;

    const ev = new MouseEvent('click', { bubbles: true });
    Object.defineProperty(ev, 'target', { value: modal });
    modal.dispatchEvent(ev);

    expect(modal.classList.contains('hidden')).toBe(true);
  });

  test('switching auth mode to role_arn shows role fields, hides key fields', () => {
    document.getElementById('add-aws-account-btn')!.click();

    const authMode = document.getElementById('account-aws-auth-mode') as HTMLSelectElement;
    authMode.value = 'role_arn';
    authMode.dispatchEvent(new Event('change'));

    expect(document.getElementById('account-aws-keys-fields')!.classList.contains('hidden')).toBe(true);
    expect(document.getElementById('account-aws-role-fields')!.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('account-aws-bastion-fields')!.classList.contains('hidden')).toBe(true);
  });

  test('switching auth mode back to access_keys shows key fields', () => {
    document.getElementById('add-aws-account-btn')!.click();

    const authMode = document.getElementById('account-aws-auth-mode') as HTMLSelectElement;
    authMode.value = 'role_arn';
    authMode.dispatchEvent(new Event('change'));

    authMode.value = 'access_keys';
    authMode.dispatchEvent(new Event('change'));

    expect(document.getElementById('account-aws-keys-fields')!.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('account-aws-role-fields')!.classList.contains('hidden')).toBe(true);
  });

  test('add-aws-account-btn auto-generates the External ID (issue #18)', () => {
    document.getElementById('add-aws-account-btn')!.click();

    const extID = document.getElementById('account-aws-external-id') as HTMLInputElement;
    // UUIDs are 36 chars with dashes (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx);
    // the test fixture doesn't set a readonly attribute (the production
    // index.html does), so we only assert on the generated value here.
    expect(extID.value).not.toBe('');
    expect(extID.value.length).toBeGreaterThanOrEqual(16);
  });
});

// ---------------------------------------------------------------------------
// Account form submit
// ---------------------------------------------------------------------------

describe('Account form submit', () => {
  beforeEach(() => {
    buildAccountsDOM();
    jest.clearAllMocks();
    (api.listAccounts as jest.Mock).mockResolvedValue([]);
    setupSettingsHandlers();
  });

  test('creates new account when account-id is empty', async () => {
    (api.createAccount as jest.Mock).mockResolvedValue({
      id: 'new-id', name: 'Acct', provider: 'aws', external_id: '123456789012', enabled: true
    });

    document.getElementById('add-aws-account-btn')!.click();
    (document.getElementById('account-name') as HTMLInputElement).value = 'Acct';
    (document.getElementById('account-external-id') as HTMLInputElement).value = '123456789012';

    document.getElementById('account-form')!.dispatchEvent(new Event('submit'));
    await new Promise(r => setTimeout(r, 0));

    expect(api.createAccount).toHaveBeenCalled();
    expect(api.updateAccount).not.toHaveBeenCalled();
    expect(api.listAccounts).toHaveBeenCalled(); // list refreshed
  });

  test('disables the Save button while the account request is in flight (issue #735)', async () => {
    const btn = document.createElement('button');
    btn.type = 'submit';
    document.getElementById('account-form')!.appendChild(btn);
    let resolve!: (v: unknown) => void;
    (api.createAccount as jest.Mock).mockReturnValue(new Promise((r) => { resolve = r; }));

    document.getElementById('add-aws-account-btn')!.click();
    (document.getElementById('account-name') as HTMLInputElement).value = 'Acct';
    (document.getElementById('account-external-id') as HTMLInputElement).value = '123456789012';
    document.getElementById('account-form')!.dispatchEvent(new Event('submit'));
    await new Promise(r => setTimeout(r, 0));
    expect(btn.disabled).toBe(true);

    resolve({ id: 'new-id', name: 'Acct', provider: 'aws', external_id: '123456789012', enabled: true });
    await new Promise(r => setTimeout(r, 0));
    expect(btn.disabled).toBe(false);
  });

  test('updates existing account when account-id is set', async () => {
    (api.updateAccount as jest.Mock).mockResolvedValue({
      id: 'existing-id', name: 'Updated', provider: 'aws', external_id: '123456789012', enabled: true
    });

    document.getElementById('add-aws-account-btn')!.click();
    (document.getElementById('account-id') as HTMLInputElement).value = 'existing-id';
    (document.getElementById('account-name') as HTMLInputElement).value = 'Updated';
    (document.getElementById('account-external-id') as HTMLInputElement).value = '123456789012';

    document.getElementById('account-form')!.dispatchEvent(new Event('submit'));
    await new Promise(r => setTimeout(r, 0));

    expect(api.updateAccount).toHaveBeenCalledWith('existing-id', expect.any(Object));
    expect(api.createAccount).not.toHaveBeenCalled();
  });

  test('saves AWS access_key credentials when key fields are filled', async () => {
    (api.createAccount as jest.Mock).mockResolvedValue({
      id: 'new-id', name: 'Acct', provider: 'aws', external_id: '123456789012', enabled: true
    });
    (api.saveAccountCredentials as jest.Mock).mockResolvedValue(undefined);

    document.getElementById('add-aws-account-btn')!.click();
    (document.getElementById('account-name') as HTMLInputElement).value = 'Acct';
    (document.getElementById('account-external-id') as HTMLInputElement).value = '123456789012';
    (document.getElementById('account-aws-auth-mode') as HTMLSelectElement).value = 'access_keys';
    (document.getElementById('account-aws-access-key-id') as HTMLInputElement).value = 'AKID';
    (document.getElementById('account-aws-secret-access-key') as HTMLInputElement).value = 'SECRET';

    document.getElementById('account-form')!.dispatchEvent(new Event('submit'));
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountCredentials).toHaveBeenCalledWith('new-id', {
      credential_type: 'aws_access_keys',
      payload: { access_key_id: 'AKID', secret_access_key: 'SECRET' }
    });
  });

  test('skips credential save when key fields are empty', async () => {
    (api.createAccount as jest.Mock).mockResolvedValue({
      id: 'new-id', name: 'Acct', provider: 'aws', external_id: '123456789012', enabled: true
    });

    document.getElementById('add-aws-account-btn')!.click();
    (document.getElementById('account-name') as HTMLInputElement).value = 'Acct';
    (document.getElementById('account-external-id') as HTMLInputElement).value = '123456789012';
    (document.getElementById('account-aws-auth-mode') as HTMLSelectElement).value = 'access_keys';
    // leave key fields empty

    document.getElementById('account-form')!.dispatchEvent(new Event('submit'));
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountCredentials).not.toHaveBeenCalled();
  });

  test('shows alert on create failure', async () => {
    (api.createAccount as jest.Mock).mockRejectedValue(new Error('Server error'));
    mockShowToast.mockClear();

    document.getElementById('add-aws-account-btn')!.click();
    (document.getElementById('account-name') as HTMLInputElement).value = 'Acct';
    (document.getElementById('account-external-id') as HTMLInputElement).value = '123456789012';

    document.getElementById('account-form')!.dispatchEvent(new Event('submit'));
    await new Promise(r => setTimeout(r, 0));

    expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
      message: expect.stringContaining('Failed to save account'),
      kind: 'error'
    }));
  });

  test('saves azure credentials when secret field is filled', async () => {
    (api.createAccount as jest.Mock).mockResolvedValue({
      id: 'az-id', name: 'AzureAcct', provider: 'azure', external_id: 'sub-id', enabled: true
    });
    (api.saveAccountCredentials as jest.Mock).mockResolvedValue(undefined);

    document.getElementById('add-azure-account-btn')!.click();
    (document.getElementById('account-name') as HTMLInputElement).value = 'AzureAcct';
    (document.getElementById('account-external-id') as HTMLInputElement).value = 'sub-id';
    (document.getElementById('account-azure-client-secret') as HTMLInputElement).value = 'mysecret';

    document.getElementById('account-form')!.dispatchEvent(new Event('submit'));
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountCredentials).toHaveBeenCalledWith('az-id', {
      credential_type: 'azure_client_secret',
      payload: { client_secret: 'mysecret' }
    });
  });
});
