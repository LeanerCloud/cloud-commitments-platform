/**
 * Commitment Laddering settings tests: the ramp schedule field error, the required
 * numeric fields and the target coverage guard. Split from ladder.test.ts to stay
 * under the 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, baseConfig, renderSection } from './ladder-harness';
import { renderConfigTable, saveLadderConfig } from '../ladder';
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

  describe('ramp schedule field error across modal reopen', () => {
    const ramp = (): HTMLTextAreaElement =>
      document.getElementById('ladder-cfg-ramp-schedule') as HTMLTextAreaElement;

    async function openAdd(): Promise<void> {
      document.getElementById('ladder-add-config-btn')!.click();
      await waitFor(() => expect(document.getElementById('ladder-cfg-account')).toBeEnabled());
      (document.getElementById('ladder-cfg-account') as HTMLSelectElement).value = 'acct-1';
    }

    test('initRampEditor resets the JSON error a cancelled save left in the cloned form, and the field still clears on edit', async () => {
      await renderSection();
      await openAdd();
      ramp().value = '{ not json';
      await saveLadderConfig();
      expect(document.querySelector('.field-error:not([hidden])')).not.toBeNull();
      expect(ramp()).toHaveAttribute('aria-invalid', 'true');

      document.getElementById('ladder-modal-cancel-btn')!.click();
      await openAdd();

      expect(document.querySelector('.field-error:not([hidden])')).toBeNull();
      expect(document.getElementById('ladder-cfg-ramp-schedule-error')!.textContent).toBe('');
      expect(ramp()).not.toHaveAttribute('aria-invalid');
      expect(ramp().getAttribute('aria-describedby')).toBe('ladder-cfg-ramp-schedule-hint ladder-cfg-ramp-schedule-error');

      // A second failed save on the reopened form must still clear on edit.
      ramp().value = '{ still not json';
      await saveLadderConfig();
      expect(document.querySelector('.field-error:not([hidden])')).not.toBeNull();
      ramp().value = '{}';
      ramp().dispatchEvent(new Event('input', { bubbles: true }));
      expect(document.querySelector('.field-error:not([hidden])')).toBeNull();
      expect(ramp()).not.toHaveAttribute('aria-invalid');
    });
  });

  describe('saveLadderConfig required numeric fields (#134)', () => {
    async function primeValidForm(): Promise<void> {
      await renderSection();
      document.getElementById('ladder-add-config-btn')!.click();
      await waitFor(() => expect(document.getElementById('ladder-cfg-account')).toBeEnabled());
      (document.getElementById('ladder-cfg-account') as HTMLSelectElement).value = 'acct-1';
      (document.getElementById('ladder-cfg-ramp-schedule') as HTMLTextAreaElement).value =
        '{"steps":[{"after_days":0,"fraction":1.0}]}';
      (document.getElementById('ladder-cfg-max-hourly') as HTMLInputElement).value = '5';
    }

    test('a cleared Buffer Fraction blocks the save instead of sending 0 (no buffer)', async () => {
      await primeValidForm();
      const buffer = document.getElementById('ladder-cfg-buffer-fraction') as HTMLInputElement;
      buffer.value = '';

      await saveLadderConfig();

      expect(api.upsertLadderConfig).not.toHaveBeenCalled();
      expect(mockShowToast).toHaveBeenCalledWith({
        message: 'Buffer fraction is required. Enter a number before saving.',
        kind: 'error',
      });
      expect(document.activeElement).toBe(buffer);
    });

    test.each([
      ['ladder-cfg-target-coverage', 'Target coverage'],
      ['ladder-cfg-baseline-percentile', 'Baseline percentile'],
      ['ladder-cfg-lookback-days', 'Lookback days'],
      ['ladder-cfg-buf-util-threshold', 'Buffer utilization threshold'],
      ['ladder-cfg-max-actions', 'Max actions per run'],
    ])('a cleared %s names the field instead of a generic save failure', async (id, label) => {
      await primeValidForm();
      (document.getElementById(id) as HTMLInputElement).value = '  ';

      await saveLadderConfig();

      expect(api.upsertLadderConfig).not.toHaveBeenCalled();
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringContaining(label) }));
    });

    test('an explicit 0 Buffer Fraction is still a valid "no buffer" choice', async () => {
      await primeValidForm();
      (document.getElementById('ladder-cfg-buffer-fraction') as HTMLInputElement).value = '0';
      (api.upsertLadderConfig as jest.Mock).mockResolvedValue(baseConfig({ buffer_fraction: 0 }));

      await saveLadderConfig();

      const sent = (api.upsertLadderConfig as jest.Mock).mock.calls[0][0] as LadderConfig;
      expect(sent.buffer_fraction).toBe(0);
      expect(sent.target_coverage).toBe(100);
      expect(sent.lookback_days).toBe(30);
      expect(sent.max_actions_per_run).toBe(10);
    });
  });

  describe('renderConfigTable target_coverage guard (#134)', () => {
    test('a row without target_coverage renders N/A instead of blanking the table', async () => {
      await renderSection();
      renderConfigTable([
        baseConfig({ target_coverage: null as unknown as number }),
        baseConfig({ cloud_account_id: 'second', target_coverage: 85 }),
      ]);

      const rows = document.querySelectorAll('#ladder-configs-table-container tbody tr');
      expect(rows).toHaveLength(2);
      expect(rows[0]!.children[5]!.textContent).toBe('N/A');
      expect(rows[1]!.children[5]!.textContent).toBe('85.0%');
    });
  });
});
