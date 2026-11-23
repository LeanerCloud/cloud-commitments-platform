/**
 * Settings tests: saveGlobalSettings form collection and validation, resetSettings and the default payment fallback.
 * Split from settings.test.ts to stay under the 500-line ceiling; the module
 * mocks and the DOM setup live in settings-harness.ts.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  mockConfirmDialog,
  setupSettingsTest,
} from './settings-harness';
import {
  loadGlobalSettings,
  saveGlobalSettings,
  resetSettings,
} from '../settings';

import * as api from '../api';

describe('Settings Module', () => {
  beforeEach(() => {
    setupSettingsTest();
  });

  describe('resetSettings', () => {
    beforeEach(() => {
      // Set non-default values
      (document.getElementById('provider-aws') as HTMLInputElement).checked = false;
      (document.getElementById('provider-azure') as HTMLInputElement).checked = true;
      (document.getElementById('provider-gcp') as HTMLInputElement).checked = true;
      (document.getElementById('setting-notification-email') as HTMLInputElement).value = 'custom@example.com';
      (document.getElementById('setting-auto-collect') as HTMLInputElement).checked = false;
      (document.getElementById('setting-default-term') as HTMLSelectElement).value = '1';
      (document.getElementById('setting-default-payment') as HTMLSelectElement).value = 'all-upfront';
      (document.getElementById('setting-default-coverage') as HTMLInputElement).value = '50';
      (document.getElementById('setting-notification-days') as HTMLInputElement).value = '7';
    });

    test('does nothing if user cancels confirmation', async () => {
      mockConfirmDialog.mockResolvedValueOnce(false);

      await resetSettings();

      // Values should not change
      expect((document.getElementById('provider-azure') as HTMLInputElement).checked).toBe(true);
    });

    test('resets all fields to defaults on confirmation', async () => {
      mockConfirmDialog.mockResolvedValueOnce(true);

      await resetSettings();

      expect((document.getElementById('provider-aws') as HTMLInputElement).checked).toBe(true);
      expect((document.getElementById('provider-azure') as HTMLInputElement).checked).toBe(false);
      expect((document.getElementById('provider-gcp') as HTMLInputElement).checked).toBe(false);
      expect((document.getElementById('setting-notification-email') as HTMLInputElement).value).toBe('');
      expect((document.getElementById('setting-auto-collect') as HTMLInputElement).checked).toBe(true);
      expect((document.getElementById('setting-default-term') as HTMLSelectElement).value).toBe('3');
      expect((document.getElementById('setting-default-payment') as HTMLSelectElement).value).toBe('no-upfront');
      expect((document.getElementById('setting-default-coverage') as HTMLInputElement).value).toBe('80');
      expect((document.getElementById('setting-notification-days') as HTMLInputElement).value).toBe('3');
    });

    // Issue #301: recommendations cycle params reset to defaults
    test('resets recommendations_cache_stale_hours to 24 and lookback to 7', async () => {
      mockConfirmDialog.mockResolvedValueOnce(true);
      (document.getElementById('setting-recs-stale-hours') as HTMLInputElement).value = '48';
      (document.getElementById('setting-recs-lookback-days') as HTMLSelectElement).value = '60';

      await resetSettings();

      expect((document.getElementById('setting-recs-stale-hours') as HTMLInputElement).value).toBe('24');
      expect((document.getElementById('setting-recs-lookback-days') as HTMLSelectElement).value).toBe('7');
    });

    // Issue #466: Reset to Defaults must expand the Collection Schedule
    // section (auto-collect re-flips ON, so the dependent controls need to
    // be visible) AND mark the settings form as dirty so the user sees
    // the "Unsaved changes" affordance and remembers to click Save.
    test('expands the Collection Schedule row when re-enabling Auto-collect (#466)', async () => {
      mockConfirmDialog.mockResolvedValueOnce(true);
      // Pre-reset state: auto-collect off, schedule row hidden.
      (document.getElementById('setting-auto-collect') as HTMLInputElement).checked = false;
      const scheduleRow = document.getElementById('collection-schedule-row');
      scheduleRow?.classList.add('hidden');

      await resetSettings();

      expect((document.getElementById('setting-auto-collect') as HTMLInputElement).checked).toBe(true);
      expect(scheduleRow?.classList.contains('hidden')).toBe(false);
    });

    test('surfaces "Unsaved changes" indicator after reset (#466)', async () => {
      mockConfirmDialog.mockResolvedValueOnce(true);
      // Clear any prior dirty flag from a previous test.
      const tabBtn = document.getElementById('admin-tab-btn');
      tabBtn?.classList.remove('has-unsaved');
      document.querySelectorAll('.settings-buttons').forEach(el => el.classList.remove('dirty'));

      await resetSettings();

      // reflectDirtyState toggles .has-unsaved on #admin-tab-btn and .dirty
      // on .settings-buttons whenever any tracked field differs from the
      // saved snapshot. After Reset, every field has been overwritten
      // relative to the (still-empty) module-level snapshot, so the
      // indicator must be active.
      expect(tabBtn?.classList.contains('has-unsaved')).toBe(true);
      const saveBar = document.querySelector('.settings-buttons') as HTMLElement | null;
      expect(saveBar?.classList.contains('dirty')).toBe(true);
    });
  });

  describe('default payment falls back to no-upfront (#1758)', () => {
    const paymentSelect = (): HTMLSelectElement =>
      document.getElementById('setting-default-payment') as HTMLSelectElement;

    test('loadGlobalSettings uses no-upfront when the config has no default_payment', async () => {
      paymentSelect().value = 'all-upfront';
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: { enabled_providers: ['aws'], default_term: 3, default_coverage: 80, notification_days_before: 3 },
        services: [],
      });

      await loadGlobalSettings();

      expect(paymentSelect().value).toBe('no-upfront');
    });

    test('saveGlobalSettings sends no-upfront when the select has no value', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: ['aws'], default_term: 3, default_payment: 'all-upfront',
          default_coverage: 80, notification_days_before: 3,
        },
        services: [],
      });
      await loadGlobalSettings();
      paymentSelect().value = '';
      (api.updateConfig as jest.Mock).mockClear().mockResolvedValue({});
      window.alert = jest.fn();

      await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

      expect(api.updateConfig).toHaveBeenCalledWith(
        expect.objectContaining({ default_payment: 'no-upfront' }),
      );
    });
  });
});
