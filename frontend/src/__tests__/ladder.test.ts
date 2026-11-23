/**
 * Commitment Laddering settings module tests (issue #1333 phase 3).
 *
 * Covers the two security/correctness-sensitive surfaces:
 *  (a) renderConfigTable escapes attacker-controlled cloud_account_id/provider
 *      so an injected tag never lands raw in innerHTML (XSS).
 *  (b) saveLadderConfig's max_hourly_commit_per_run guard: blank -> null,
 *      positive -> passthrough, non-positive/invalid -> rejected (no store call).
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, baseConfig, renderSection } from './ladder-harness';
import { initLadderingSettings, renderConfigTable, saveLadderConfig } from '../ladder';
import type { LadderConfig } from '../api';
import { waitFor } from '@testing-library/dom';
import { listAccountsMinimal } from '../api/accounts';
import { canAccess } from '../permissions';
import { refreshLadderTimeline } from '../ladder-timeline';
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

  // Issue #186: the per-account table loads behind the shared skeleton, not a
  // plain "Loading..." paragraph, and the skeleton gives way to the table or
  // to the error.
  test('per-account configs show skeleton rows while loading, then the table (issue #186)', async () => {
    document.body.innerHTML = '<div id="commitment-laddering-settings"></div>';
    let resolve: (c: LadderConfig[]) => void = () => undefined;
    (api.getLadderConfigs as jest.Mock).mockReturnValue(new Promise<LadderConfig[]>((r) => { resolve = r; }));
    const done = initLadderingSettings(false);

    const container = document.getElementById('ladder-configs-table-container')!;
    // No visible "Loading…" paragraph; the only text is the sr-only status.
    expect(container.querySelector('p')).toBeNull();
    const rows = container.querySelectorAll('tr.skeleton-row');
    expect(rows).toHaveLength(3);
    expect(rows[0]!.querySelectorAll('td')).toHaveLength(9);
    expect(container.querySelector('.skeleton-status')).toHaveTextContent('Loading…');

    resolve([baseConfig({ id: 'config-1' })]);
    await done;
    expect(container.querySelector('.skeleton')).toBeNull();
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1);
  });

  test('a failed per-account config load replaces the skeleton with the error (issue #186)', async () => {
    document.body.innerHTML = '<div id="commitment-laddering-settings"></div>';
    (api.getLadderConfigs as jest.Mock).mockRejectedValue(new Error('boom'));
    jest.spyOn(console, 'error').mockImplementation(() => undefined);
    await initLadderingSettings(false);

    const container = document.getElementById('ladder-configs-table-container')!;
    expect(container.querySelector('.skeleton')).toBeNull();
    expect(container.hasAttribute('aria-busy')).toBe(false);
    expect(container).toHaveTextContent('Failed to load per-account configurations.');
  });

  test('config modal moves focus inside, traps Tab, closes on Escape and restores focus (#731)', async () => {
    await renderSection([baseConfig({ id: 'config-1' })]);
    const editBtn = document.querySelector<HTMLButtonElement>('.ladder-edit-btn')!;
    editBtn.focus();
    editBtn.click();

    const modal = document.getElementById('ladder-config-modal')!;
    expect(modal).not.toHaveClass('hidden');
    expect(modal).toHaveAttribute('role', 'dialog');
    expect(modal.contains(document.activeElement)).toBe(true);

    // Shift+Tab from the first focusable wraps to the last one inside the modal.
    modal.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true }));
    expect(modal.contains(document.activeElement)).toBe(true);

    modal.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(modal).toHaveClass('hidden');
    expect(document.activeElement).toBe(editBtn);
  });

  test('lookback input advertises the 10-day baseline minimum', async () => {
    await renderSection([baseConfig({ id: 'config-1' })]);
    expect(document.getElementById('ladder-cfg-lookback-days')).toHaveAttribute('min', '10');
    expect(document.getElementById('ladder-cfg-lookback-days')).toHaveAccessibleDescription('Minimum 10');
  });

  test.each([
    ['aws', 'email_approval', 'daily'],
    ['azure', 'auto_approve', 'weekly'],
    ['gcp', 'auto_approve', 'weekly'],
  ] as const)('editing %s preserves the selected values in the submitted config', async (provider, mode, cadence) => {
    const config = baseConfig({ id: 'config-1', provider, mode, cadence, target_coverage: 80 });
    await renderSection([config]);
    (api.upsertLadderConfig as jest.Mock).mockImplementation(async (cfg: LadderConfig) => cfg);

    document.querySelector<HTMLButtonElement>('.ladder-edit-btn')!.click();
    expect(document.getElementById('ladder-cfg-provider')).toHaveValue(provider);
    expect(document.getElementById('ladder-cfg-mode')).toHaveValue(mode);
    expect(document.getElementById('ladder-cfg-cadence')).toHaveValue(cadence);
    expect(document.getElementById('ladder-cfg-provider')).toBeDisabled();
    expect(document.getElementById('ladder-cfg-account')).toBeDisabled();

    (document.getElementById('ladder-cfg-target-coverage') as HTMLInputElement).value = '75';
    document.getElementById('ladder-config-form')!.dispatchEvent(new Event('submit', { cancelable: true }));
    await waitFor(() => expect(document.getElementById('ladder-config-modal')).toHaveClass('hidden'));
    expect(api.upsertLadderConfig).toHaveBeenCalledTimes(1);
    const { updated_at: _updatedAt, ...expected } = config;
    expect(api.upsertLadderConfig).toHaveBeenCalledWith({ ...expected, target_coverage: 75 });
    // Issue #728: the timeline picks up the saved config without a reload.
    expect(refreshLadderTimeline).toHaveBeenCalledWith(expect.arrayContaining([expect.objectContaining({ cloud_account_id: config.cloud_account_id })]));
  });

  describe('renderLadderingSection: toggle label accessibility (issue #1412 row 3.1)', () => {
    // Regression guard: the Enable Commitment Laddering label text must NOT be
    // a <label for="setting-laddering-enabled"> because that makes the entire
    // text string clickable (toggling the checkbox on click). Pattern matches
    // the Auto-collect row fix from issue #464: use a <span> + aria-labelledby
    // instead.
    test('enable-laddering label is a span, not a <label for> element', async () => {
      await renderSection();
      // A <label for="setting-laddering-enabled"> would make the text clickable.
      const badLabel = document.querySelector('label[for="setting-laddering-enabled"]');
      expect(badLabel).toBeNull();
    });

    test('enable-laddering input carries aria-labelledby referencing the span', async () => {
      await renderSection();
      const input = document.getElementById('setting-laddering-enabled') as HTMLInputElement | null;
      expect(input).not.toBeNull();
      const labelledById = input!.getAttribute('aria-labelledby');
      expect(labelledById).toBe('setting-laddering-enabled-label');
      // The referenced element must exist and contain the label text.
      const labelSpan = document.getElementById('setting-laddering-enabled-label');
      expect(labelSpan).not.toBeNull();
      expect(labelSpan!.textContent).toMatch(/Enable Commitment Laddering/);
    });

    test('enable-laddering checkbox is wrapped in a .toggle-label element', async () => {
      await renderSection();
      const input = document.getElementById('setting-laddering-enabled') as HTMLInputElement | null;
      expect(input).not.toBeNull();
      // The input must be inside a .toggle-label so the toggle switch is the
      // only click target (not the surrounding descriptive text).
      expect(input!.closest('.toggle-label')).not.toBeNull();
    });
  });

  describe('kill-switch toggle', () => {
    const flip = (): HTMLInputElement => {
      const toggle = document.getElementById('setting-laddering-enabled') as HTMLInputElement;
      toggle.checked = !toggle.checked;
      toggle.dispatchEvent(new Event('change'));
      return toggle;
    };

    test.each([true, false])('sends exactly the new laddering_enabled value and nothing else (was %s)', async (before) => {
      await renderSection();
      (api.updateConfig as jest.Mock).mockResolvedValue({});
      const toggle = document.getElementById('setting-laddering-enabled') as HTMLInputElement;
      toggle.checked = before;

      flip();
      await waitFor(() => expect(api.updateConfig).toHaveBeenCalledTimes(1));

      expect(api.updateConfig).toHaveBeenCalledWith({ laddering_enabled: !before });
      expect(toggle.checked).toBe(!before);
      await waitFor(() => expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'success' })));
      // Issue #728: the timeline re-evaluates with the new kill-switch state.
      expect(refreshLadderTimeline).toHaveBeenCalledWith(expect.any(Array), !before);
    });

    test('a failed PUT reverts the toggle and shows an error toast', async () => {
      await renderSection();
      (api.updateConfig as jest.Mock).mockRejectedValue(new Error('boom'));
      jest.spyOn(console, 'error').mockImplementation(() => undefined);
      const toggle = document.getElementById('setting-laddering-enabled') as HTMLInputElement;
      const before = toggle.checked;

      flip();

      await waitFor(() => expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' })));
      expect(api.updateConfig).toHaveBeenCalledWith({ laddering_enabled: !before });
      expect(toggle.checked).toBe(before);
    });
  });

  describe('renderConfigTable XSS escaping', () => {
    test('renders a malicious cloud_account_id/provider as inert text, not markup', async () => {
      await renderSection();
      const evil = '<img src=x onerror=alert(1)>';
      renderConfigTable([baseConfig({ cloud_account_id: evil, provider: evil })]);

      const container = document.getElementById('ladder-configs-table-container')!;

      // The injected payload must not create a live element anywhere in the
      // table (DOM-level assertion; reading serialized innerHTML is unreliable
      // because jsdom re-serializes attribute values with raw </> which are
      // inert inside a quoted attribute).
      expect(container.querySelector('img')).toBeNull();

      // The account-id cell holds the payload as TEXT (no child elements =>
      // it was escaped, not parsed as HTML).
      const firstCell = container.querySelector('tbody tr td');
      expect(firstCell?.textContent).toBe(evil);
      expect(firstCell?.children.length).toBe(0);

      // Exactly one edit button exists (no extra nodes injected) and it carries
      // the value as an inert data-* attribute rather than as markup.
      const btns = container.querySelectorAll<HTMLButtonElement>('button.ladder-edit-btn');
      expect(btns.length).toBe(1);
      expect(btns[0]!.dataset['provider']).toBe(evil);
    });
  });

  describe('saveLadderConfig max_hourly guard', () => {
    // Fill the modal form with a valid baseline so save reaches the store,
    // then override max-hourly per case.
    async function primeValidForm(maxHourly: string): Promise<void> {
      await renderSection();
      document.getElementById('ladder-add-config-btn')!.click();
      await waitFor(() => expect(document.getElementById('ladder-cfg-account')).toBeEnabled());
      (document.getElementById('ladder-cfg-account') as HTMLSelectElement).value = 'acct-1';
      (document.getElementById('ladder-cfg-ramp-schedule') as HTMLTextAreaElement).value =
        '{"steps":[{"after_days":0,"fraction":1.0}]}';
      (document.getElementById('ladder-cfg-max-hourly') as HTMLInputElement).value = maxHourly;
    }

    test('blank max-hourly on an AWS config is blocked with an inline error naming the field', async () => {
      await primeValidForm('');

      await saveLadderConfig();

      expect(api.upsertLadderConfig).not.toHaveBeenCalled();
      const field = document.getElementById('ladder-cfg-max-hourly') as HTMLInputElement;
      expect(field).toHaveAttribute('aria-invalid', 'true');
      const noteId = field.dataset['fieldErrorId']!;
      expect(document.getElementById(noteId)).toHaveTextContent('max_hourly_commit_per_run is required');
      expect(document.activeElement).toBe(field);
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));

      field.value = '5';
      field.dispatchEvent(new Event('input', { bubbles: true }));
      expect(document.getElementById(noteId)).toBeNull();
      expect(field).not.toHaveAttribute('aria-invalid');
    });

    test('blank max-hourly on a stored non-AWS config still serializes to null', async () => {
      const stored = baseConfig({ id: 'existing', cloud_account_id: 'gcp-id', provider: 'gcp', max_hourly_commit_per_run: null });
      await renderSection([stored]);
      document.querySelector<HTMLButtonElement>('.ladder-edit-btn')!.click();
      expect(document.getElementById('ladder-cfg-max-hourly')).toHaveValue(null);
      (api.upsertLadderConfig as jest.Mock).mockResolvedValue(stored);

      await saveLadderConfig();

      expect(api.upsertLadderConfig).toHaveBeenCalledTimes(1);
      const sent = (api.upsertLadderConfig as jest.Mock).mock.calls[0][0] as LadderConfig;
      expect(sent.max_hourly_commit_per_run).toBeNull();
      expect(sent.provider).toBe('gcp');
    });

    test('positive max-hourly passes through unchanged', async () => {
      await primeValidForm('5');
      (api.upsertLadderConfig as jest.Mock).mockResolvedValue(baseConfig());

      await saveLadderConfig();

      expect(api.upsertLadderConfig).toHaveBeenCalledTimes(1);
      const sent = (api.upsertLadderConfig as jest.Mock).mock.calls[0][0] as LadderConfig;
      expect(sent.max_hourly_commit_per_run).toBe(5);
    });

    test('non-positive max-hourly is rejected (guarded, no store call)', async () => {
      await primeValidForm('-1');
      await saveLadderConfig();

      expect(api.upsertLadderConfig).not.toHaveBeenCalled();
      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({ kind: 'error' }),
      );
    });
  });

  describe('saveLadderConfig per-sweep ceiling', () => {
    const sweep = (): HTMLInputElement => document.getElementById('ladder-cfg-max-sweep') as HTMLInputElement;
    async function open(existing?: LadderConfig): Promise<void> {
      await renderSection(existing ? [existing] : []);
      if (existing) {
        (document.querySelector('button.ladder-edit-btn') as HTMLButtonElement).click();
        await waitFor(() => expect(document.getElementById('ladder-cfg-account-status')).toHaveTextContent(/\S/));
      } else {
        document.getElementById('ladder-add-config-btn')!.click();
        await waitFor(() => expect(document.getElementById('ladder-cfg-account')).toBeEnabled());
      }
      (document.getElementById('ladder-cfg-ramp-schedule') as HTMLTextAreaElement).value =
        '{"steps":[{"after_days":0,"fraction":1.0}]}';
      (api.upsertLadderConfig as jest.Mock).mockResolvedValue(baseConfig());
    }
    const sent = (): Record<string, unknown> => (api.upsertLadderConfig as jest.Mock).mock.calls[0][0];

    test('untouched field sends no key', async () => {
      await open(baseConfig({ cloud_account_id: 'acct-1', max_hourly_commit_per_sweep: 2.5 }));
      expect(sweep().value).toBe('2.5');
      await saveLadderConfig();
      expect(Object.prototype.hasOwnProperty.call(sent(), 'max_hourly_commit_per_sweep')).toBe(false);
    });

    test('cleared field sends null', async () => {
      await open(baseConfig({ cloud_account_id: 'acct-1', max_hourly_commit_per_sweep: 2.5 }));
      sweep().value = '';
      await saveLadderConfig();
      expect(sent()['max_hourly_commit_per_sweep']).toBeNull();
    });

    test('a changed number is sent', async () => {
      await open(baseConfig({ cloud_account_id: 'acct-1', max_hourly_commit_per_sweep: null }));
      sweep().value = '4.25';
      await saveLadderConfig();
      expect(sent()['max_hourly_commit_per_sweep']).toBe(4.25);
    });

    test('invalid input is blocked with an inline error and no store call', async () => {
      await open(baseConfig({ cloud_account_id: 'acct-1', max_hourly_commit_per_sweep: null }));
      sweep().value = '1e3';
      await saveLadderConfig();
      expect(api.upsertLadderConfig).not.toHaveBeenCalled();
      expect(sweep()).toHaveAttribute('aria-invalid', 'true');
      expect(document.activeElement).toBe(sweep());
    });

    test.each(['1,5', 'abc', '+5', '1e3'])('typo %s on a stored ceiling is blocked, not sent as a clear', async raw => {
      await open(baseConfig({ cloud_account_id: 'acct-1', max_hourly_commit_per_sweep: 2.5 }));
      sweep().value = raw;
      await saveLadderConfig();
      expect(api.upsertLadderConfig).not.toHaveBeenCalled();
      expect(sweep()).toHaveAttribute('aria-invalid', 'true');
      expect(document.activeElement).toBe(sweep());
    });

    test('a server refusal (arming grant, 503) reaches the toast', async () => {
      await open(baseConfig({ cloud_account_id: 'acct-1', max_hourly_commit_per_sweep: null }));
      sweep().value = '4';
      (api.upsertLadderConfig as jest.Mock).mockRejectedValue(new Error('requires the auto-purchase arming grant'));
      await saveLadderConfig();
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
        kind: 'error', message: expect.stringContaining('auto-purchase arming grant'),
      }));
    });

    test('the table formats large ceilings readably', async () => {
      await renderSection([baseConfig({ max_hourly_commit_per_sweep: 1234567.5 })]);
      expect(document.querySelector('#ladder-configs-table-container tbody')).toHaveTextContent('$1,234,567.5/hr');
    });

    test('the table shows the ceiling or Not set', async () => {
      await renderSection([baseConfig({ max_hourly_commit_per_sweep: 2.5 })]);
      expect(document.querySelector('#ladder-configs-table-container tbody')).toHaveTextContent('$2.5/hr');
      await renderSection([baseConfig({ max_hourly_commit_per_sweep: null })]);
      expect(document.querySelector('#ladder-configs-table-container tbody')).toHaveTextContent('Not set');
    });
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
