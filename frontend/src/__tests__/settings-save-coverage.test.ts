/**
 * Settings tests: global coverage propagation (issue #522) and the SP coverage, enabled and cache-staleness saves.
 * Split from settings.test.ts to stay under the 500-line ceiling; the module
 * mocks and the DOM setup live in settings-harness.ts.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  mockShowToast,
  setupSettingsTest,
} from './settings-harness';
import {
  loadGlobalSettings,
  saveGlobalSettings,
  setupSettingsHandlers,
  isUnsavedChanges,
} from '../settings';

import * as api from '../api';

describe('Settings Module', () => {
  beforeEach(() => {
    setupSettingsTest();
  });

  describe('saveGlobalSettings', () => {
    const fillForm = (): void => {
      // Set up form values
      (document.getElementById('provider-aws') as HTMLInputElement).checked = true;
      (document.getElementById('provider-azure') as HTMLInputElement).checked = false;
      (document.getElementById('provider-gcp') as HTMLInputElement).checked = true;
      (document.getElementById('setting-notification-email') as HTMLInputElement).value = 'test@test.com';
      (document.getElementById('setting-auto-collect') as HTMLInputElement).checked = true;
      (document.getElementById('setting-default-term') as HTMLSelectElement).value = '3';
      (document.getElementById('setting-default-payment') as HTMLSelectElement).value = 'all-upfront';
      (document.getElementById('setting-default-coverage') as HTMLInputElement).value = '80';
      (document.getElementById('setting-notification-days') as HTMLInputElement).value = '3';
    };

    beforeEach(fillForm);

    // Load a pristine config so the saved snapshot matches the form, as in the
    // real page; an edit made afterwards is then the only dirty field.
    const loadPristine = async (): Promise<void> => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: ['aws'], default_term: 3, default_payment: 'all-upfront',
          default_coverage: 80, notification_days_before: 3,
        },
        services: [
          { provider: 'aws', service: 'savings-plans-compute', enabled: true, term: 3, payment: 'all-upfront', coverage: 80 },
          { provider: 'aws', service: 'savings-plans-sagemaker', enabled: true, term: 3, payment: 'all-upfront', coverage: 80 },
          { provider: 'aws', service: 'savings-plans-database', enabled: true, term: 3, payment: 'all-upfront', coverage: 80 },
        ],
      });
      await loadGlobalSettings();
    };

    test('saveGlobalSettings sends per-card enabled=false when SP enabled checkbox unchecked (issue #136)', async () => {
      await loadPristine();
      (api.updateConfig as jest.Mock).mockResolvedValue({});
      (api.updateServiceConfig as jest.Mock).mockClear().mockResolvedValue(undefined);
      window.alert = jest.fn();

      // Disable the database SP card.
      (document.getElementById('aws-savings-plans-database-enabled') as HTMLInputElement).checked = false;

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);

      const dbCall = (api.updateServiceConfig as jest.Mock).mock.calls.find(
        ([provider, service]) => provider === 'aws' && service === 'savings-plans-database',
      );
      expect(dbCall).toBeDefined();
      expect(dbCall![2].enabled).toBe(false);

      // Untouched SP cards are not sent at all (#539).
      const computeCall = (api.updateServiceConfig as jest.Mock).mock.calls.find(
        ([provider, service]) => provider === 'aws' && service === 'savings-plans-compute',
      );
      expect(computeCall).toBeUndefined();
    });

    test('loadGlobalSettings populates SP coverage and enabled from service config (issue #136)', async () => {
      // Seed a non-default initial DOM state on the fallback card so the test
      // fails if the fallback assignment is skipped (it would otherwise pass on
      // the HTML defaults, which happen to equal the asserted values).
      (document.getElementById('aws-savings-plans-ec2instance-coverage') as HTMLInputElement).value = '12';
      (document.getElementById('aws-savings-plans-ec2instance-enabled') as HTMLInputElement).checked = false;

      (api.getConfig as jest.Mock).mockResolvedValue({
        // Non-80 global default so the fallback writes an observably different value.
        global: { enabled_providers: ['aws'], default_term: 3, default_payment: 'all-upfront', default_coverage: 67 },
        services: [
          { provider: 'aws', service: 'savings-plans-compute', term: 1, payment: 'no-upfront', coverage: 65, enabled: false },
        ],
      });
      setupSettingsHandlers();
      await loadGlobalSettings();

      const coverageEl = document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement;
      const enabledEl = document.getElementById('aws-savings-plans-compute-enabled') as HTMLInputElement;
      expect(coverageEl.value).toBe('65');
      expect(enabledEl.checked).toBe(false);

      // Cards without an explicit service row fall back to the global default
      // (67, not the HTML default 80) and re-enable from the seeded false state.
      const ec2Coverage = document.getElementById('aws-savings-plans-ec2instance-coverage') as HTMLInputElement;
      const ec2Enabled = document.getElementById('aws-savings-plans-ec2instance-enabled') as HTMLInputElement;
      expect(ec2Coverage.value).toBe('67');
      expect(ec2Enabled.checked).toBe(true);
    });

    test('an unedited form PUTs no service rows, an edit PUTs only its row', async () => {
      await loadPristine();
      (api.updateConfig as jest.Mock).mockResolvedValue({});
      (api.updateServiceConfig as jest.Mock).mockClear().mockResolvedValue(undefined);
      window.alert = jest.fn();

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);
      expect(api.updateServiceConfig).not.toHaveBeenCalled();

      (document.getElementById('aws-savings-plans-sagemaker-term') as HTMLSelectElement).value = '1';
      await saveGlobalSettings(event);
      expect(api.updateServiceConfig).toHaveBeenCalledTimes(1);
    });

    // Issue #301: configurable recommendations cache-staleness threshold + lookback
    test('sends recommendations_cache_stale_hours and recommendations_lookback_days', async () => {
      (api.updateConfig as jest.Mock).mockResolvedValue({});
      (document.getElementById('setting-recs-stale-hours') as HTMLInputElement).value = '48';
      (document.getElementById('setting-recs-lookback-days') as HTMLSelectElement).value = '30';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);

      const call = (api.updateConfig as jest.Mock).mock.calls[0][0];
      expect(call.recommendations_cache_stale_hours).toBe(48);
      expect(call.recommendations_lookback_days).toBe(30);
    });

    test('rejects out-of-range recommendations_cache_stale_hours and does not call updateConfig', async () => {
      (api.updateConfig as jest.Mock).mockResolvedValue({});
      (document.getElementById('setting-recs-stale-hours') as HTMLInputElement).value = '9999';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);

      expect(api.updateConfig).not.toHaveBeenCalled();
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));
    });

    test('rejects fractional recommendations_cache_stale_hours and does not call updateConfig', async () => {
      // Pin the Number.isInteger guard so a future refactor can't silently
      // truncate fractional input via parseInt and accept it as valid.
      (api.updateConfig as jest.Mock).mockResolvedValue({});
      (document.getElementById('setting-recs-stale-hours') as HTMLInputElement).value = '1.5';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);

      expect(api.updateConfig).not.toHaveBeenCalled();
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));
    });

    test('accepts 0 for recommendations_cache_stale_hours (disable sentinel)', async () => {
      // Preserve the documented "0 = disable automatic background refresh"
      // semantic. The validator must NOT reject 0; updateConfig must be
      // called with the literal 0 so the persisted GlobalConfig disables
      // the stale-while-revalidate background refresh.
      (api.updateConfig as jest.Mock).mockResolvedValue({});
      (document.getElementById('setting-recs-stale-hours') as HTMLInputElement).value = '0';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);

      expect(api.updateConfig).toHaveBeenCalled();
      const call = (api.updateConfig as jest.Mock).mock.calls[0][0];
      expect(call.recommendations_cache_stale_hours).toBe(0);
    });

    describe('global coverage propagation (issue #522)', () => {
      const loadWithCoverage50 = async (): Promise<void> => {
        (api.getConfig as jest.Mock).mockResolvedValue({
          global: { enabled_providers: ['aws'], default_term: 3, default_payment: 'all-upfront', default_coverage: 50 },
          services: [
            { provider: 'aws', service: 'ec2', enabled: true, term: 3, payment: 'all-upfront', coverage: 50 },
            { provider: 'aws', service: 'savings-plans-compute', enabled: true, term: 3, payment: 'all-upfront', coverage: 50 },
          ],
        });
        (api.updateConfig as jest.Mock).mockResolvedValue({});
        (api.updateServiceConfig as jest.Mock).mockClear().mockResolvedValue(undefined);
        await loadGlobalSettings();
      };
      const coverageSentFor = (service: string): number | undefined => {
        const call = (api.updateServiceConfig as jest.Mock).mock.calls.find(
          ([provider, svc]) => provider === 'aws' && svc === service,
        );
        return call?.[2].coverage;
      };

      test('an SP card coverage typed during its pending PUT is kept and sent by the next save (#576)', async () => {
        await loadWithCoverage50();
        const card = document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement;
        card.value = '60';
        let resolvePut!: () => void;
        (api.updateServiceConfig as jest.Mock).mockImplementation((provider: string, svc: string) =>
          provider === 'aws' && svc === 'savings-plans-compute'
            ? new Promise<void>(resolve => { resolvePut = resolve; })
            : Promise.resolve());

        const firstSave = saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);
        await new Promise(r => setTimeout(r, 0));
        card.value = '77';
        resolvePut();
        await firstSave;

        expect(coverageSentFor('savings-plans-compute')).toBe(60);
        expect(card.value).toBe('77');
        expect(isUnsavedChanges()).toBe(true);

        (api.updateServiceConfig as jest.Mock).mockClear().mockResolvedValue(undefined);
        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(coverageSentFor('savings-plans-compute')).toBe(77);
        expect(isUnsavedChanges()).toBe(false);
      });

      test.each(['12.7', '-40', '500'])('an SP card coverage of %s aborts the save before any PUT (#318)', async (value) => {
        await loadWithCoverage50();
        mockShowToast.mockClear();
        (document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value = value;

        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(api.updateConfig).not.toHaveBeenCalled();
        expect(api.updateServiceConfig).not.toHaveBeenCalled();
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
          kind: 'error',
          message: expect.stringContaining('whole number between 0 and 100 (savings-plans-compute)'),
        }));
        expect(isUnsavedChanges()).toBe(true);
      });

      test.each([['0', 0], ['100', 100]])('an SP card coverage of %s is within range and saved (#318)', async (value, sent) => {
        await loadWithCoverage50();
        (document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value = value;

        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(coverageSentFor('savings-plans-compute')).toBe(sent);
      });

      test('a saved service payment edit leaves nothing unsaved (#576)', async () => {
        await loadWithCoverage50();
        (document.getElementById('aws-ec2-payment') as HTMLSelectElement).value = 'partial-upfront';
        expect(isUnsavedChanges()).toBe(true);

        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(isUnsavedChanges()).toBe(false);
      });

      test('an empty global coverage field is rejected instead of saving 0', async () => {
        await loadWithCoverage50();
        (document.getElementById('setting-default-coverage') as HTMLInputElement).value = '';

        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(api.updateConfig).not.toHaveBeenCalled();
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
          message: 'Dashboard coverage target must be a whole number between 0 and 100',
          kind: 'error',
        }));
      });
    });
  }); // end saveGlobalSettings
});
