/**
 * Settings tests: Global Defaults propagation cancel and display-name popup (#1412).
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
  setupSettingsHandlers,
  isUnsavedChanges,
  openOverrideModal,
} from '../settings';

import * as api from '../api';

describe('Settings Module', () => {
  beforeEach(() => {
    setupSettingsTest();
  });

  // -----------------------------------------------------------------------
  // Cancel on Global Defaults clears dirty state (issue #1412 rows 7.4/7.9)
  // -----------------------------------------------------------------------
  // These tests use loadGlobalSettings() to establish a proper dirty-tracking
  // snapshot (identical to the real app flow where settings are loaded before
  // the user interacts). Without the snapshot every field looks dirty regardless
  // of its value, so the test cannot distinguish "cancel cleared the marker"
  // from "marker was never set".
  describe('cancel on Global Defaults propagation clears dirty state (#1412 7.4/7.9)', () => {
    // Minimal getConfig response that satisfies loadGlobalSettings without errors.
    const baseConfigResponse = {
      global: {
        enabled_providers: ['aws'] as string[],
        notification_email: '',
        auto_collect: true,
        default_term: 3,
        default_payment: 'all-upfront',
        default_coverage: 80,
        notification_days_before: 3,
        recommendations_cache_stale_hours: 24,
        recommendations_lookback_days: 7,
        grace_period_days: { aws: 7, azure: 7, gcp: 7 },
        laddering_enabled: false,
      },
      services: [],
      credentials: {},
    };

    test('cancelling term cascade clears has-unsaved and dirty markers', async () => {
      // loadGlobalSettings populates the form AND calls snapshotAllFields() so
      // dirty tracking has a clean baseline (term=3 in the snapshot).
      (api.getConfig as jest.Mock).mockResolvedValue(baseConfigResponse);
      await loadGlobalSettings();
      setupSettingsHandlers();
      mockConfirmDialog.mockResolvedValueOnce(false);

      // Change term to 1 (differs from snapshot=3) -> confirm appears -> cancel.
      const defaultTerm = document.getElementById('setting-default-term') as HTMLSelectElement;
      defaultTerm.value = '1';
      defaultTerm.dispatchEvent(new Event('change'));
      await new Promise((r) => setTimeout(r, 0));

      // Cancel must restore the previous value.
      expect(defaultTerm.value).toBe('3');

      // With snapshot=3 and restored=3 the diff is clean, so markers must clear.
      const tabBtn = document.getElementById('admin-tab-btn');
      const saveBar = document.querySelector('.settings-buttons');
      expect(tabBtn?.classList.contains('has-unsaved')).toBe(false);
      expect(saveBar?.classList.contains('dirty')).toBe(false);
    });

    test('cancelling payment cascade clears has-unsaved and dirty markers', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue(baseConfigResponse);
      await loadGlobalSettings();
      setupSettingsHandlers();
      mockConfirmDialog.mockResolvedValueOnce(false);

      const defaultPayment = document.getElementById('setting-default-payment') as HTMLSelectElement;
      defaultPayment.value = 'no-upfront';
      defaultPayment.dispatchEvent(new Event('change'));
      await new Promise((r) => setTimeout(r, 0));

      expect(defaultPayment.value).toBe('all-upfront');

      const tabBtn = document.getElementById('admin-tab-btn');
      const saveBar = document.querySelector('.settings-buttons');
      expect(tabBtn?.classList.contains('has-unsaved')).toBe(false);
      expect(saveBar?.classList.contains('dirty')).toBe(false);
    });
  });

  // -----------------------------------------------------------------------
  // Global Defaults popup uses display names (issue #1412 rows 7.3/7.8)
  // -----------------------------------------------------------------------
  describe('Global Defaults popup shows display names not backend IDs (#1412 7.3/7.8)', () => {
    test('confirmAndPropagateTerm passes display names to confirmDialog', async () => {
      setupSettingsHandlers();
      mockConfirmDialog.mockResolvedValueOnce(false);

      const defaultTerm = document.getElementById('setting-default-term') as HTMLSelectElement;
      defaultTerm.dataset['previous'] = '3';
      // Set one service to a different value so the diff is non-empty.
      (document.getElementById('aws-ec2-term') as HTMLSelectElement).value = '3';
      defaultTerm.value = '1';
      defaultTerm.dispatchEvent(new Event('change'));
      await new Promise((r) => setTimeout(r, 0));
      await new Promise((r) => setTimeout(r, 0));

      expect(mockConfirmDialog).toHaveBeenCalledTimes(1);
      const callArg = mockConfirmDialog.mock.calls[0]![0] as { body: Node };
      // body is the HTMLDivElement from buildAffectedList; serialize to check text.
      const bodyText = (callArg.body as HTMLElement).textContent ?? '';
      // Must include display name, not raw backend ID.
      expect(bodyText).toMatch(/EC2 Reserved Instances/);
      expect(bodyText).not.toMatch(/^aws$/m);
    });
  });

  // -----------------------------------------------------------------------
  // isUnsavedChanges -- no false prompt on pristine load or before load
  // (issue #1441)
  // -----------------------------------------------------------------------
  // The navigation guard in navigation.ts calls isUnsavedChanges() whenever
  // the user leaves the Admin tab. Before the fix, savedSnapshot started as
  // an empty object ({}), so comparing any live DOM value against undefined
  // always returned true -- causing the "You have unsaved settings changes.
  // Leave without saving?" modal to fire even without any edits.
  describe('isUnsavedChanges -- navigation guard (issue #1441)', () => {
    const baseConfigResponse = {
      global: {
        enabled_providers: ['aws'] as string[],
        notification_email: '',
        auto_collect: true,
        default_term: 3,
        default_payment: 'all-upfront',
        default_coverage: 80,
        notification_days_before: 3,
        recommendations_cache_stale_hours: 24,
        recommendations_lookback_days: 7,
        grace_period_days: { aws: 7, azure: 7, gcp: 7 },
        laddering_enabled: false,
      },
      services: [],
      credentials: {},
    };

    test('returns false before settings have been loaded (empty snapshot -- regression for #1441)', () => {
      // Use a fresh module instance so savedSnapshot starts as the empty object
      // {} that it is at module initialisation, before any loadGlobalSettings()
      // call has run snapshotAllFields(). This is the exact state that caused
      // the bug: the navigation guard fired on every first Admin-tab visit
      // because `getFieldValue(id) !== undefined` was always true.
      //
      // Pre-fix: TRACKED_FIELDS.some(id => getFieldValue(id) !== savedSnapshot[id])
      //   = TRACKED_FIELDS.some(id => '' !== undefined) = true -> prompt shown (BUG)
      // Post-fix: Object.keys(savedSnapshot).length === 0 -> return false (CORRECT)
      let freshIsUnsavedChanges!: () => boolean;
      jest.isolateModules(() => {
        // eslint-disable-next-line @typescript-eslint/no-require-imports
        const s = require('../settings') as { isUnsavedChanges: () => boolean };
        freshIsUnsavedChanges = s.isUnsavedChanges;
      });
      expect(freshIsUnsavedChanges()).toBe(false);
    });

    test('returns false immediately after settings load without any user edits', async () => {
      // loadGlobalSettings() populates all tracked fields from the API response
      // and then calls snapshotAllFields() to establish the clean baseline.
      // A user who navigates away right after load (no edits) must see no prompt.
      (api.getConfig as jest.Mock).mockResolvedValue(baseConfigResponse);
      await loadGlobalSettings();

      expect(isUnsavedChanges()).toBe(false);
    });

    test('returns true after a tracked field is edited post-load', async () => {
      // After a clean load + snapshot, mutating a tracked field must be detected
      // so the navigation guard can show the prompt for real unsaved changes.
      // Verifies the fix does not suppress legitimate dirty-state detection.
      (api.getConfig as jest.Mock).mockResolvedValue(baseConfigResponse);
      await loadGlobalSettings();

      // Simulate the user changing the notification email -- a tracked field.
      const emailInput = document.getElementById('setting-notification-email') as HTMLInputElement;
      emailInput.value = 'edited@example.com';
      emailInput.dispatchEvent(new Event('input'));

      expect(isUnsavedChanges()).toBe(true);
    });
  });

  // Issue #135: `default_coverage || 80` rewrote a stored 0 to 80, and the
  // next Save persisted 80 without the user touching the field.
  describe('default_coverage 0 round-trips through load and save (issue #135)', () => {
    const configWithCoverage = (global: Record<string, unknown>, services: unknown[] = []) => ({
      global: {
        enabled_providers: ['aws'],
        notification_email: '',
        auto_collect: true,
        default_term: 3,
        default_payment: 'all-upfront',
        notification_days_before: 3,
        ...global,
      },
      services,
      credentials: {},
    });

    const saveUnchanged = async () => {
      (api.updateConfig as jest.Mock).mockResolvedValue({});
      (api.updateServiceConfig as jest.Mock).mockClear().mockResolvedValue(undefined);
      await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);
    };

    test('a stored 0 renders as 0 and an unedited Save sends no global PUT', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue(configWithCoverage({ default_coverage: 0 }));
      await loadGlobalSettings();

      expect((document.getElementById('setting-default-coverage') as HTMLInputElement).value).toBe('0');
      expect((document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value).toBe('0');

      await saveUnchanged();

      expect(api.updateConfig).not.toHaveBeenCalled();
      expect(api.updateServiceConfig).not.toHaveBeenCalled();
    });

    test('an absent default_coverage renders the 80 default and an unedited Save sends no global PUT', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue(configWithCoverage({}));
      await loadGlobalSettings();

      expect((document.getElementById('setting-default-coverage') as HTMLInputElement).value).toBe('80');

      await saveUnchanged();

      expect(api.updateConfig).not.toHaveBeenCalled();
    });

    test('a per-service coverage of 0 round-trips under a non-zero global', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue(configWithCoverage(
        { default_coverage: 50 },
        [{ provider: 'aws', service: 'savings-plans-compute', enabled: true, term: 3, payment: 'all-upfront', coverage: 0 }],
      ));
      await loadGlobalSettings();

      expect((document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value).toBe('0');
      expect((document.getElementById('aws-savings-plans-ec2instance-coverage') as HTMLInputElement).value).toBe('50');

      await saveUnchanged();

      expect(api.updateServiceConfig).not.toHaveBeenCalled();
    });

    test('the override modal inherit label shows the stored 0%', async () => {
      const modal = document.createElement('div');
      modal.id = 'override-modal';
      const covInput = document.createElement('input');
      covInput.id = 'override-coverage';
      modal.appendChild(covInput);
      document.body.appendChild(modal);

      (api.getConfig as jest.Mock).mockResolvedValue(configWithCoverage({ default_coverage: 0 }));
      await loadGlobalSettings();
      openOverrideModal('acc-1', 'aws', [], document.createElement('div'));

      expect(covInput.placeholder).toBe('Inherit (currently: 0%)');
    });
  });
});
