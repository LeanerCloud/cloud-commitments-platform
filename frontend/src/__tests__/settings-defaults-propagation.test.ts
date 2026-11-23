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
  setupSettingsHandlers,
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
});
