/**
 * Settings tests: the global PUT carries only changed fields (issue #539).
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

    describe('global PUT carries only changed fields (issue #539)', () => {
      const loadStaleTab = async (): Promise<void> => {
        (api.getConfig as jest.Mock).mockResolvedValue({
          global: {
            enabled_providers: ['aws'],
            notification_email: 'old@test.com',
            default_term: 1,
            default_payment: 'no-upfront',
            default_coverage: 50,
            notification_days_before: 3,
          },
          services: [
            { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'no-upfront', coverage: 50 },
            { provider: 'aws', service: 'savings-plans-compute', enabled: true, term: 1, payment: 'no-upfront', coverage: 50 },
          ],
        });
        (api.updateConfig as jest.Mock).mockClear().mockResolvedValue({});
        (api.updateServiceConfig as jest.Mock).mockClear().mockResolvedValue(undefined);
        await loadGlobalSettings();
      };
      const save = (): Promise<void> => saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

      test('saving an unrelated field does not send the cached term, payment or coverage', async () => {
        await loadStaleTab();
        (document.getElementById('setting-notification-email') as HTMLInputElement).value = 'new@test.com';

        await save();

        expect(api.updateConfig).toHaveBeenCalledTimes(1);
        expect(api.updateConfig).toHaveBeenCalledWith({ notification_email: 'new@test.com' });
      });

      test('changing only the term sends only the term', async () => {
        await loadStaleTab();
        (document.getElementById('setting-default-term') as HTMLSelectElement).value = '3';

        await save();

        expect(api.updateConfig).toHaveBeenCalledWith({ default_term: 3 });
      });

      test('changing the coverage sends only the coverage and no service PUTs', async () => {
        await loadStaleTab();
        (document.getElementById('setting-default-coverage') as HTMLInputElement).value = '70';

        await save();

        expect(api.updateConfig).toHaveBeenCalledWith({ default_coverage: 70 });
        expect(api.updateServiceConfig).not.toHaveBeenCalled();
      });

      test('a second save does not resend a field the first save persisted', async () => {
        await loadStaleTab();
        (document.getElementById('setting-notification-email') as HTMLInputElement).value = 'new@test.com';
        await save();
        (api.updateConfig as jest.Mock).mockClear();

        await save();

        expect(api.updateConfig).not.toHaveBeenCalled();
      });

      test('a rejected global PUT leaves the field dirty, so the next save resends it', async () => {
        await loadStaleTab();
        (document.getElementById('setting-notification-email') as HTMLInputElement).value = 'new@test.com';
        (api.updateConfig as jest.Mock).mockRejectedValueOnce(new Error('boom'));

        await save();
        expect(api.updateConfig).toHaveBeenCalledTimes(1);
        (api.updateConfig as jest.Mock).mockClear().mockResolvedValue({});

        await save();

        expect(api.updateConfig).toHaveBeenCalledTimes(1);
        expect(api.updateConfig).toHaveBeenCalledWith({ notification_email: 'new@test.com' });
      });

      test('a field edited while the global PUT is in flight is still sent by the next save', async () => {
        await loadStaleTab();
        (document.getElementById('setting-notification-email') as HTMLInputElement).value = 'new@test.com';
        let resolvePut!: () => void;
        (api.updateConfig as jest.Mock).mockImplementationOnce(
          () => new Promise<void>(resolve => { resolvePut = resolve; }),
        );

        const firstSave = save();
        (document.getElementById('setting-default-term') as HTMLSelectElement).value = '3';
        resolvePut();
        await firstSave;
        (api.updateConfig as jest.Mock).mockClear().mockResolvedValue({});

        await save();

        expect(api.updateConfig).toHaveBeenCalledWith({ default_term: 3 });
      });

      test('a sent field edited again while its PUT is in flight is still sent by the next save', async () => {
        await loadStaleTab();
        const email = document.getElementById('setting-notification-email') as HTMLInputElement;
        email.value = 'first@test.com';
        let resolvePut!: () => void;
        (api.updateConfig as jest.Mock).mockImplementationOnce(
          () => new Promise<void>(resolve => { resolvePut = resolve; }),
        );

        const firstSave = save();
        email.value = 'second@test.com';
        resolvePut();
        await firstSave;
        expect(api.updateConfig).toHaveBeenCalledWith({ notification_email: 'first@test.com' });
        (api.updateConfig as jest.Mock).mockClear().mockResolvedValue({});

        await save();

        expect(api.updateConfig).toHaveBeenCalledWith({ notification_email: 'second@test.com' });
      });

      test('an unchanged form sends no global PUT and no service PUTs', async () => {
        await loadStaleTab();

        await save();

        expect(api.updateConfig).not.toHaveBeenCalled();
        expect(api.updateServiceConfig).not.toHaveBeenCalled();
      });

      test('editing one service card PUTs only that row and only the edited field', async () => {
        await loadStaleTab();
        (document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value = '65';

        await save();

        expect(api.updateConfig).not.toHaveBeenCalled();
        expect(api.updateServiceConfig).toHaveBeenCalledTimes(1);
        expect(api.updateServiceConfig).toHaveBeenCalledWith('aws', 'savings-plans-compute', expect.objectContaining({ coverage: 65 }));
        const body = (api.updateServiceConfig as jest.Mock).mock.calls[0][2];
        expect(Object.keys(body).sort()).toEqual(['coverage', 'provider', 'service']);
      });

      test('editing only a recommendation filter sends only that filter', async () => {
        await loadStaleTab();
        const regions = document.createElement('input');
        regions.id = 'aws-ec2-include-regions';
        regions.value = 'us-east-1, eu-west-1';
        document.getElementById('global-settings-form')!.appendChild(regions);

        await save();

        expect(api.updateServiceConfig).toHaveBeenCalledTimes(1);
        expect(api.updateServiceConfig).toHaveBeenCalledWith('aws', 'ec2', {
          provider: 'aws', service: 'ec2', include_regions: ['us-east-1', 'eu-west-1'],
        });
      });

      test('a filter saved by one Save is not resent by the next', async () => {
        await loadStaleTab();
        const regions = document.createElement('input');
        regions.id = 'aws-ec2-include-regions';
        regions.value = 'us-east-1';
        document.getElementById('global-settings-form')!.appendChild(regions);
        await save();
        expect(api.updateServiceConfig).toHaveBeenCalledTimes(1);
        (api.updateServiceConfig as jest.Mock).mockClear();

        await save();

        expect(api.updateServiceConfig).not.toHaveBeenCalled();
      });

      test('an edited SP coverage cleared to empty blocks the save and stays dirty', async () => {
        await loadStaleTab();
        const card = document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement;
        card.value = '';
        mockShowToast.mockClear();

        await save();

        expect(api.updateConfig).not.toHaveBeenCalled();
        expect(api.updateServiceConfig).not.toHaveBeenCalled();
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
          message: expect.stringContaining('coverage must be a whole number between 0 and 100 (savings-plans-compute)'),
          kind: 'error',
        }));
        expect(isUnsavedChanges()).toBe(true);

        card.value = '65';
        await save();
        expect(api.updateServiceConfig).toHaveBeenCalledWith('aws', 'savings-plans-compute', expect.objectContaining({ coverage: 65 }));
      });

      test('an unedited SP card is never flagged, whatever its stored value', async () => {
        await loadStaleTab();
        (api.getConfig as jest.Mock).mockResolvedValue({
          global: { enabled_providers: ['aws'], default_term: 1, default_payment: 'no-upfront', default_coverage: 50, notification_days_before: 3 },
          services: [{ provider: 'aws', service: 'savings-plans-compute', enabled: true, term: 1, payment: 'no-upfront', coverage: 150 }],
        });
        await loadGlobalSettings();
        (document.getElementById('setting-notification-email') as HTMLInputElement).value = 'new@test.com';
        mockShowToast.mockClear();

        await save();

        expect(api.updateConfig).toHaveBeenCalledWith({ notification_email: 'new@test.com' });
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'success' }));
      });

      test('an emptied cache stale threshold is refused, an explicit 0 is saved', async () => {
        await loadStaleTab();
        const hours = document.getElementById('setting-recs-stale-hours') as HTMLInputElement;
        hours.value = '';
        mockShowToast.mockClear();

        await save();

        expect(api.updateConfig).not.toHaveBeenCalled();
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
          message: expect.stringContaining('Cache stale threshold must be a whole number'),
          kind: 'error',
        }));

        hours.value = '0';
        await save();
        expect(api.updateConfig).toHaveBeenCalledWith({ recommendations_cache_stale_hours: 0 });
      });

      test('a second save after a service PUT succeeded resends nothing', async () => {
        await loadStaleTab();
        (document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value = '65';
        await save();
        (api.updateServiceConfig as jest.Mock).mockClear();

        await save();

        expect(api.updateServiceConfig).not.toHaveBeenCalled();
      });
    });
  }); // end saveGlobalSettings
});
