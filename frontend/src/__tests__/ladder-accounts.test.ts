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
import { listAccountsMinimal } from '../api/accounts';
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
