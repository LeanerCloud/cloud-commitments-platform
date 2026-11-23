/**
 * Settings tests: global PUT against the real index.html (issue #539).
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
} from '../settings';

import * as api from '../api';

describe('Settings Module', () => {
  beforeEach(() => {
    setupSettingsTest();
  });

  describe('global PUT against the real index.html (issue #539)', () => {
    const loadRealPage = async (providers: string[] = ['aws']): Promise<void> => {
      // eslint-disable-next-line @typescript-eslint/no-require-imports
      const fs = require('fs') as typeof import('fs');
      // eslint-disable-next-line @typescript-eslint/no-require-imports
      const path = require('path') as typeof import('path');
      const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8');
      document.body.innerHTML = html
        .replace(/^[\s\S]*<body[^>]*>/i, '')
        .replace(/<\/body>[\s\S]*$/i, '')
        .replace(/<script[\s\S]*?<\/script>/gi, '');
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: providers,
          notification_email: 'old@test.com',
          auto_collect: true,
          collection_schedule: 'daily',
          default_term: 3,
          default_payment: 'all-upfront',
          default_coverage: 80,
          notification_days_before: 3,
          grace_period_days: { aws: 7, azure: 7, gcp: 7 },
          recommendations_cache_stale_hours: 24,
          recommendations_lookback_days: 7,
          offering_class: 'convertible',
          require_different_approver: false,
          laddering_enabled: true,
        },
        services: [],
      });
      (api.updateConfig as jest.Mock).mockClear().mockResolvedValue({});
      (api.updateServiceConfig as jest.Mock).mockClear().mockResolvedValue(undefined);
      await loadGlobalSettings();
      await new Promise(resolve => setTimeout(resolve, 0));
    };
    const save = (): Promise<void> => saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);
    const globalBody = (): unknown => (api.updateConfig as jest.Mock).mock.calls[0]?.[0];

    test('the laddering toggle is rendered and an unedited Save sends no global PUT', async () => {
      await loadRealPage();
      expect(document.getElementById('setting-laddering-enabled')).not.toBeNull();

      await save();

      expect(api.updateConfig).not.toHaveBeenCalled();
    });

    test('editing only the notification email sends only the email', async () => {
      await loadRealPage();
      (document.getElementById('setting-notification-email') as HTMLInputElement).value = 'new@test.com';

      await save();

      expect(globalBody()).toEqual({ notification_email: 'new@test.com' });
    });

    test('editing one grace input saves grace_period_days', async () => {
      await loadRealPage();
      (document.getElementById('setting-grace-azure') as HTMLInputElement).value = '9';

      await save();

      expect(globalBody()).toEqual({ grace_period_days: { aws: 7, azure: 9, gcp: 7 } });
    });

    test('editing only a provider toggle saves enabled_providers', async () => {
      await loadRealPage();
      (document.getElementById('provider-gcp') as HTMLInputElement).checked = true;

      await save();

      expect(globalBody()).toEqual({ enabled_providers: ['aws', 'gcp'] });
    });

    describe('refuses a save that leaves no provider enabled (issue #1531)', () => {
      const serverProviders = (providers: string[]): void => {
        (api.getConfig as jest.Mock).mockResolvedValue({
          global: { enabled_providers: providers, default_term: 3, default_payment: 'all-upfront', default_coverage: 80 },
          services: [],
        });
      };
      const uncheckAws = (): void => {
        (document.getElementById('provider-aws') as HTMLInputElement).checked = false;
      };

      test('unchecking the only enabled provider is refused with a toast and nothing sent', async () => {
        await loadRealPage(['aws']);
        mockShowToast.mockClear();
        uncheckAws();

        await save();

        expect(mockShowToast).toHaveBeenCalledWith({ message: 'Enable at least one provider before saving.', kind: 'error' });
        expect(api.updateConfig).not.toHaveBeenCalled();
        expect(api.updateServiceConfig).not.toHaveBeenCalled();
      });

      test('unchecking one of two providers saves the remaining one', async () => {
        await loadRealPage(['aws', 'gcp']);
        uncheckAws();

        await save();

        expect(globalBody()).toEqual({ enabled_providers: ['gcp'] });
      });

      test('a stored empty list does not block editing an unrelated field', async () => {
        await loadRealPage([]);
        (document.getElementById('setting-notification-email') as HTMLInputElement).value = 'new@test.com';

        await save();

        expect(globalBody()).toEqual({ notification_email: 'new@test.com' });
      });

      test('another admin enabling gcp makes the merged list non-empty, so the save is allowed', async () => {
        await loadRealPage(['aws']);
        serverProviders(['gcp']);
        uncheckAws();

        await save();

        expect(globalBody()).toEqual({ enabled_providers: ['gcp'] });
      });
    });

    test('editing only the collection schedule saves it', async () => {
      await loadRealPage();
      (document.getElementById('setting-collection-schedule') as HTMLSelectElement).value = 'weekly';

      await save();

      expect(globalBody()).toEqual({ collection_schedule: 'weekly' });
    });

    test('editing only the approver toggle saves it', async () => {
      await loadRealPage();
      (document.getElementById('setting-require-different-approver') as HTMLInputElement).checked = true;

      await save();

      expect(globalBody()).toEqual({ require_different_approver: true });
    });

    test('editing only the experimental GCP plan-term toggle saves it', async () => {
      await loadRealPage();
      const box = document.getElementById('setting-experimental-gcp-plan-term') as HTMLInputElement;
      expect(box.checked).toBe(false);
      box.checked = true;

      await save();

      expect(globalBody()).toEqual({ experimental_gcp_plan_term: true });
    });

    test('editing only the offering class saves it', async () => {
      await loadRealPage();
      (document.getElementById('setting-ec2-offering-class') as HTMLSelectElement).value = 'standard';

      await save();

      expect(globalBody()).toEqual({ offering_class: 'standard' });
    });

    test('a global field persisted before a filter validation failure is not treated as unsaved', async () => {
      await loadRealPage();
      const minCount = document.getElementById('aws-ec2-min-count') as HTMLInputElement;
      expect(minCount).not.toBeNull();
      (document.getElementById('setting-notification-email') as HTMLInputElement).value = 'new@test.com';
      minCount.value = '-1';

      await save();
      expect(api.updateConfig).toHaveBeenCalledTimes(1);
      expect(api.updateConfig).toHaveBeenLastCalledWith({ notification_email: 'new@test.com' });

      // The user undoes the edit and fixes the filter. The stored email is
      // new@test.com, so reverting the field must be sent.
      (document.getElementById('setting-notification-email') as HTMLInputElement).value = 'old@test.com';
      minCount.value = '0';
      await save();

      expect(api.updateConfig).toHaveBeenCalledTimes(2);
      expect(api.updateConfig).toHaveBeenLastCalledWith({ notification_email: 'old@test.com' });
    });
  });
});
