/**
 * Commitment Laddering settings tests: the registered account selector, account
 * labels and the auto-approve safeguards. Split from ladder.test.ts to stay
 * under the 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, baseConfig, renderSection } from './ladder-harness';
import { saveLadderConfig } from '../ladder';
import type { LadderConfig } from '../api';
import { waitFor } from '@testing-library/dom';
import { listAccountsMinimal, type AccountSummary } from '../api/accounts';
import { canAccess } from '../permissions';
import { confirmDialog } from '../confirmDialog';
import * as api from '../api';

describe('ladder.ts', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    mockShowToast.mockReset();
    (canAccess as jest.Mock).mockReturnValue(true);
    (confirmDialog as jest.Mock).mockResolvedValue(true);
    (listAccountsMinimal as jest.Mock).mockResolvedValue([
      { id: 'acct-1', name: 'AWS account', external_id: 'aws-external', provider: 'aws' },
      { id: 'azure-id', name: 'Azure account', external_id: 'azure-external', provider: 'azure' },
      { id: 'gcp-id', name: 'GCP account', external_id: 'gcp-external', provider: 'gcp' },
    ]);
  });

  describe('registered account selector', () => {
    const account = (): HTMLSelectElement => document.getElementById('ladder-cfg-account') as HTMLSelectElement;
    const provider = (): HTMLSelectElement => document.getElementById('ladder-cfg-provider') as HTMLSelectElement;
    const status = (): HTMLElement => document.getElementById('ladder-cfg-account-status')!;
    const saveButton = (): HTMLElement => document.getElementById('ladder-config-save-btn')!;
    // pendingAccounts lets a test hold the modal's account request open. It is
    // installed after the section renders because the config table also loads
    // account names during init.
    async function open(pendingAccounts?: (mock: jest.Mock) => void): Promise<void> {
      await renderSection();
      pendingAccounts?.(listAccountsMinimal as jest.Mock);
      document.getElementById('ladder-add-config-btn')!.click();
    }
    async function ready(): Promise<void> { await waitFor(() => expect(account()).toBeEnabled()); }

    test('offers only registered accounts for the chosen provider and submits their internal ID', async () => {
      await open();
      await ready();
      expect(account().tagName).toBe('SELECT');
      expect(account()).toHaveValue('');
      expect(Array.from(account().options).map(option => option.value)).toEqual(['', 'acct-1']);
      expect(account().options[1]!.textContent).toBe('AWS account (aws-external)');
      account().value = 'acct-1';
      (document.getElementById('ladder-cfg-max-hourly') as HTMLInputElement).value = '5';
      (api.upsertLadderConfig as jest.Mock).mockImplementation(async config => config);
      await saveLadderConfig();
      expect(api.upsertLadderConfig).toHaveBeenCalledWith(expect.objectContaining({ cloud_account_id: 'acct-1', provider: 'aws' }));
      expect(listAccountsMinimal).toHaveBeenCalledWith();
      expect(api.updateConfig).not.toHaveBeenCalled();
    });

    test('help text says the ladder only plans and nothing is bought automatically', async () => {
      await renderSection();
      const help = document.querySelector('#commitment-laddering-automation-settings .settings-help')!.textContent!;
      expect(help).toMatch(/plans a ramped schedule/);
      expect(help).toMatch(/does not buy anything on\s+its own/);
      expect(help).not.toMatch(/automatically ramps/);
    });

    test('a new config offers AWS only, with a reason on the disabled providers', async () => {
      await open();
      const byValue = (v: string): HTMLOptionElement => provider().querySelector<HTMLOptionElement>(`option[value="${v}"]`)!;
      expect(byValue('aws').disabled).toBe(false);
      for (const v of ['azure', 'gcp']) {
        expect(byValue(v).disabled).toBe(true);
        expect(byValue(v).title).toBe('Not supported yet: the ladder plans AWS commitments only');
        expect(byValue(v).textContent).toMatch(/not supported yet/);
      }
    });

    test('reopening the modal never accumulates the suffix and an edit shows plain labels', async () => {
      await renderSection([baseConfig({ id: 'existing', cloud_account_id: 'saved-id', provider: 'gcp' })]);
      const labels = (): string[] => Array.from(provider().options).map(option => option.textContent!);
      const unsupported = ['AWS', 'Azure (not supported yet)', 'GCP (not supported yet)'];
      document.getElementById('ladder-add-config-btn')!.click();
      expect(labels()).toEqual(unsupported);
      document.getElementById('ladder-modal-cancel-btn')!.click();
      document.getElementById('ladder-add-config-btn')!.click();
      expect(labels()).toEqual(unsupported);
      document.getElementById('ladder-modal-cancel-btn')!.click();
      document.querySelector<HTMLButtonElement>('.ladder-edit-btn')!.click();
      expect(labels()).toEqual(['AWS', 'Azure', 'GCP']);
      document.getElementById('ladder-modal-cancel-btn')!.click();
      document.getElementById('ladder-add-config-btn')!.click();
      expect(labels()).toEqual(unsupported);
    });

    test.each(['azure', 'gcp'] as const)('editing a stored %s row keeps its option selectable and saved', async stored => {
      await renderSection([baseConfig({ id: 'existing', cloud_account_id: 'saved-id', provider: stored })]);
      document.querySelector<HTMLButtonElement>('.ladder-edit-btn')!.click();
      const option = provider().querySelector<HTMLOptionElement>(`option[value="${stored}"]`)!;
      expect(option.disabled).toBe(false);
      expect(option.title).toBe('');
      expect(option.textContent).not.toMatch(/not supported/);
      expect(provider()).toHaveValue(stored);
      (api.upsertLadderConfig as jest.Mock).mockImplementation(async saved => saved);
      await saveLadderConfig();
      expect(api.upsertLadderConfig).toHaveBeenCalledWith(expect.objectContaining({ provider: stored }));
    });

    test.each(['invented-id', 'azure-id'])('rejects an injected option %s for AWS', async id => {
      await open();
      await ready();
      if (account() instanceof HTMLSelectElement) account().add(new Option('Injected', id));
      account().value = id;
      (api.upsertLadderConfig as jest.Mock).mockImplementation(async config => config);
      await saveLadderConfig();
      expect(api.upsertLadderConfig).not.toHaveBeenCalled();
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));
    });

    test('renders hostile names and external IDs as text', async () => {
      const evil = '<img src=x onerror=alert(1)>';
      (listAccountsMinimal as jest.Mock).mockResolvedValue([{ id: 'safe-id', name: evil, external_id: evil, provider: 'aws' }]);
      await open();
      await ready();
      expect(account().options[1]!.textContent).toBe(`${evil} (${evil})`);
      expect(document.querySelector('#ladder-config-form img')).toBeNull();
    });

    test('blocks save while loading and lists only AWS accounts after the response', async () => {
      let resolve!: (accounts: AccountSummary[]) => void;
      await open(mock => mock.mockReturnValue(new Promise<AccountSummary[]>(done => { resolve = done; })));
      expect(account()).toBeDisabled();
      expect(saveButton()).toBeDisabled();
      expect(status()).toHaveTextContent(/loading/i);
      await saveLadderConfig();
      expect(api.upsertLadderConfig).not.toHaveBeenCalled();
      resolve([
        { id: 'gcp-late', name: 'Late', external_id: 'external', provider: 'gcp' },
        { id: 'aws-late', name: 'Late', external_id: 'external', provider: 'aws' },
      ]);
      await ready();
      expect(Array.from(account().options).map(option => option.value)).toEqual(['', 'aws-late']);
    });

    test('a response from a closed modal cannot replace a reopened form', async () => {
      let resolve!: (accounts: AccountSummary[]) => void;
      await open(mock => mock.mockReturnValueOnce(new Promise<AccountSummary[]>(done => { resolve = done; })));
      document.getElementById('ladder-modal-cancel-btn')!.click();
      document.getElementById('ladder-add-config-btn')!.click();
      await ready();
      account().value = 'acct-1';
      resolve([{ id: 'stale-id', name: 'Stale', external_id: 'stale', provider: 'aws' }]);
      await Promise.resolve();
      expect(account()).toHaveValue('acct-1');
      expect(Array.from(account().options).map(option => option.value)).toEqual(['', 'acct-1']);
    });

    test.each(['empty', 'error'])('blocks creation when account list is %s', async state => {
      if (state === 'error') (listAccountsMinimal as jest.Mock).mockRejectedValue(new Error('Forbidden'));
      else (listAccountsMinimal as jest.Mock).mockResolvedValue([]);
      await open();
      await waitFor(() => expect(status()).toHaveTextContent(state === 'error' ? /could not load/i : /no registered/i));
      expect(account()).toBeDisabled();
      expect(saveButton()).toBeDisabled();
      account().add(new Option('Injected', 'invented-id'));
      account().value = 'invented-id';
      await saveLadderConfig();
      expect(api.upsertLadderConfig).not.toHaveBeenCalled();
    });

    test.each(['empty', 'error'])('keeps the captured edit key with a visible warning when list is %s', async state => {
      if (state === 'error') (listAccountsMinimal as jest.Mock).mockRejectedValue(new Error('Forbidden'));
      else (listAccountsMinimal as jest.Mock).mockResolvedValue([]);
      const config = baseConfig({ id: 'existing', cloud_account_id: 'saved-id', provider: 'azure' });
      await renderSection([config]);
      document.querySelector<HTMLButtonElement>('.ladder-edit-btn')!.click();
      await waitFor(() => expect(status()).toHaveTextContent(/identity is unchanged/i));
      expect(account()).toBeDisabled();
      expect(provider()).toBeDisabled();
      account().add(new Option('Injected', 'different-id'));
      account().value = 'different-id';
      provider().value = 'aws';
      (api.upsertLadderConfig as jest.Mock).mockImplementation(async saved => saved);
      await saveLadderConfig();
      expect(api.upsertLadderConfig).toHaveBeenCalledWith(expect.objectContaining({ cloud_account_id: 'saved-id', provider: 'azure' }));
    });

    test('permission loss prevents submitting a selected account', async () => {
      await open();
      await ready();
      account().value = 'acct-1';
      (canAccess as jest.Mock).mockReturnValue(false);
      await saveLadderConfig();
      expect(api.upsertLadderConfig).not.toHaveBeenCalled();
      (listAccountsMinimal as jest.Mock).mockClear();
      await renderSection();
      expect(document.getElementById('ladder-add-config-btn')).toBeNull();
      // Only the table's name lookup runs; no account selector is opened.
      expect(listAccountsMinimal).toHaveBeenCalledTimes(1);
    });
  });


  // Issue #742: readable account labels, auto-approve impact, server errors.
  describe('account labels and auto-approve safeguards', () => {
    const mode = (): HTMLSelectElement => document.getElementById('ladder-cfg-mode') as HTMLSelectElement;
    const warning = (): HTMLElement => document.getElementById('ladder-cfg-mode-warning')!;
    const setMode = (value: string): void => { mode().value = value; mode().dispatchEvent(new Event('change')); };
    async function openNew(): Promise<void> {
      await renderSection();
      document.getElementById('ladder-add-config-btn')!.click();
      await waitFor(() => expect(document.getElementById('ladder-cfg-account')).toBeEnabled());
      (document.getElementById('ladder-cfg-account') as HTMLSelectElement).value = 'acct-1';
      (document.getElementById('ladder-cfg-max-hourly') as HTMLInputElement).value = '5';
      (api.upsertLadderConfig as jest.Mock).mockImplementation(async (cfg: LadderConfig) => cfg);
    }

    test('table shows the registered account name and keeps the ID as the title', async () => {
      await renderSection([baseConfig({ cloud_account_id: 'acct-1' })]);
      const cell = document.querySelector('#ladder-configs-table-container tbody td')!;
      expect(cell).toHaveTextContent('AWS account (aws-external)');
      expect(cell).toHaveAttribute('title', 'acct-1');
      expect(document.querySelector('#ladder-configs-table-container th')).toHaveTextContent('Account');
    });

    test('an uncapped AWS config shows why runs are refused; capped and non-AWS configs do not', async () => {
      await renderSection([
        baseConfig({ cloud_account_id: 'acct-1', max_hourly_commit_per_run: null }),
        baseConfig({ cloud_account_id: 'azure-id', provider: 'azure', max_hourly_commit_per_run: null }),
        baseConfig({ cloud_account_id: 'capped', max_hourly_commit_per_run: 5 }),
      ]);
      const notices = document.querySelectorAll('#ladder-configs-table-container .ladder-uncapped-notice');
      expect(notices).toHaveLength(1);
      expect(notices[0]).toHaveTextContent('AWS account (aws-external)');
      expect(notices[0]).toHaveTextContent('Ladder runs are refused until max_hourly_commit_per_run is set.');
    });

    test('table falls back to the stored ID when the account list fails', async () => {
      (listAccountsMinimal as jest.Mock).mockRejectedValue(new Error('Forbidden'));
      await renderSection([baseConfig({ cloud_account_id: 'acct-1' })]);
      expect(document.querySelector('#ladder-configs-table-container tbody td')).toHaveTextContent('acct-1');
    });

    test('the warning follows the Approval Mode selection', async () => {
      await openNew();
      expect(warning()).not.toBeVisible();
      setMode('auto_approve');
      expect(warning()).toBeVisible();
      expect(warning()).toHaveTextContent(/skips human approval/i);
      setMode('email_approval');
      expect(warning()).not.toBeVisible();
    });

    test('editing an Auto Approve config shows the warning immediately', async () => {
      await renderSection([baseConfig({ id: 'c', cloud_account_id: 'acct-1', mode: 'auto_approve' })]);
      document.querySelector<HTMLButtonElement>('.ladder-edit-btn')!.click();
      expect(warning()).toBeVisible();
    });

    test.each([[true, 1], [false, 0]])('turning on Auto Approve asks first (confirmed=%s)', async (confirmed, saves) => {
      (confirmDialog as jest.Mock).mockResolvedValue(confirmed);
      await openNew();
      setMode('auto_approve');
      (document.getElementById('ladder-cfg-enabled') as HTMLInputElement).checked = true;
      await saveLadderConfig();
      expect(confirmDialog).toHaveBeenCalledWith(expect.objectContaining({ title: 'Enable Auto Approve?', destructive: true }));
      expect(api.upsertLadderConfig).toHaveBeenCalledTimes(saves);
      expect(document.getElementById('ladder-config-modal')!.classList.contains('hidden')).toBe(confirmed);
      expect(document.getElementById('ladder-config-save-btn')).not.toBeDisabled();
    });

    test('no confirmation when Auto Approve is already active or the config stays disabled', async () => {
      await openNew();
      setMode('auto_approve');
      await saveLadderConfig();
      await renderSection([baseConfig({ id: 'c', cloud_account_id: 'acct-1', mode: 'auto_approve' })]);
      document.querySelector<HTMLButtonElement>('.ladder-edit-btn')!.click();
      await saveLadderConfig();
      expect(confirmDialog).not.toHaveBeenCalled();
      expect(api.upsertLadderConfig).toHaveBeenCalledTimes(2);
    });

    test('a failed save shows the server error text', async () => {
      await openNew();
      (api.upsertLadderConfig as jest.Mock).mockRejectedValue(new Error('ramp_schedule fractions must sum to 1.0'));
      await saveLadderConfig();
      expect(mockShowToast).toHaveBeenCalledWith({ message: 'Failed to save ladder config: ramp_schedule fractions must sum to 1.0', kind: 'error' });
    });
  });
});
