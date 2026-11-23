/**
 * Settings module tests
 */

// The harness registers the module mocks, so it must be imported first.
import {
  mockConfirmDialog,
  mockShowToast,
  setupSettingsTest,
} from './settings-harness';
import {
  loadGlobalSettings,
  saveGlobalSettings,
  resetSettings,
  setupSettingsHandlers,
  copyToClipboard,
  isUnsavedChanges,
} from '../settings';

import * as api from '../api';

describe('Settings Module', () => {
  beforeEach(() => {
    setupSettingsTest();
  });

  describe('setupSettingsHandlers', () => {
    test('sets up provider checkbox event handlers', () => {
      setupSettingsHandlers();

      const awsCheck = document.getElementById('provider-aws') as HTMLInputElement;
      const awsSettings = document.getElementById('aws-settings');

      // Trigger change event
      awsCheck.checked = true;
      awsCheck.dispatchEvent(new Event('change'));

      expect(awsSettings?.classList.contains('hidden')).toBe(false);
    });

    test('sets up azure checkbox event handler', () => {
      setupSettingsHandlers();

      const azureCheck = document.getElementById('provider-azure') as HTMLInputElement;
      const azureSettings = document.getElementById('azure-settings');

      azureCheck.checked = true;
      azureCheck.dispatchEvent(new Event('change'));

      expect(azureSettings?.classList.contains('hidden')).toBe(false);
    });

    test('sets up gcp checkbox event handler', () => {
      setupSettingsHandlers();

      const gcpCheck = document.getElementById('provider-gcp') as HTMLInputElement;
      const gcpSettings = document.getElementById('gcp-settings');

      gcpCheck.checked = true;
      gcpCheck.dispatchEvent(new Event('change'));

      expect(gcpSettings?.classList.contains('hidden')).toBe(false);
    });

    test('sets up auto-collect checkbox to toggle schedule visibility', () => {
      setupSettingsHandlers();

      const autoCollect = document.getElementById('setting-auto-collect') as HTMLInputElement;
      const scheduleRow = document.getElementById('collection-schedule-row');

      autoCollect.checked = true;
      autoCollect.dispatchEvent(new Event('change'));

      expect(scheduleRow?.classList.contains('hidden')).toBe(false);

      autoCollect.checked = false;
      autoCollect.dispatchEvent(new Event('change'));

      expect(scheduleRow?.classList.contains('hidden')).toBe(true);
    });

    test('sets up default term to propagate to services (after confirm)', async () => {
      setupSettingsHandlers();
      mockConfirmDialog.mockResolvedValueOnce(true);

      // Baseline is "3" via the parent beforeEach; flip to "1" so the
      // cascade has real work to do.
      const defaultTerm = document.getElementById('setting-default-term') as HTMLSelectElement;
      defaultTerm.value = '1';
      defaultTerm.dispatchEvent(new Event('change'));
      await new Promise((r) => setTimeout(r, 0));

      expect(mockConfirmDialog).toHaveBeenCalledTimes(1);
      expect((document.getElementById('aws-ec2-term') as HTMLSelectElement).value).toBe('1');
      expect((document.getElementById('aws-rds-term') as HTMLSelectElement).value).toBe('1');
      expect((document.getElementById('azure-vm-term') as HTMLSelectElement).value).toBe('1');
      expect((document.getElementById('gcp-compute-term') as HTMLSelectElement).value).toBe('1');
    });

    test('sets up default payment to propagate to AWS services (after confirm), clamping where per-service constraints reject the value', async () => {
      setupSettingsHandlers();
      mockConfirmDialog.mockResolvedValueOnce(true);

      const defaultPayment = document.getElementById('setting-default-payment') as HTMLSelectElement;
      defaultPayment.value = 'no-upfront';
      defaultPayment.dispatchEvent(new Event('change'));
      await new Promise((r) => setTimeout(r, 0));

      expect(mockConfirmDialog).toHaveBeenCalledTimes(1);
      // EC2 accepts no-upfront at both terms — propagation lands as-is.
      expect((document.getElementById('aws-ec2-payment') as HTMLSelectElement).value).toBe('no-upfront');
      // RDS 3yr rejects no-upfront (parent beforeEach seeds all service
      // terms at "3"), so the constraint sync clamps RDS back to the
      // first valid payment option instead of persisting an invalid
      // combination the provider will refuse.
      const rdsPayment = (document.getElementById('aws-rds-payment') as HTMLSelectElement).value;
      expect(rdsPayment).not.toBe('no-upfront');
      expect(['partial-upfront', 'all-upfront']).toContain(rdsPayment);
    });

    test('cancelling the cascade restores the default term to its prior value', async () => {
      setupSettingsHandlers();
      mockConfirmDialog.mockResolvedValueOnce(false);

      const defaultTerm = document.getElementById('setting-default-term') as HTMLSelectElement;
      // Baseline: all services at "3" (seeded in beforeEach). Attempt to switch to "1".
      defaultTerm.value = '1';
      defaultTerm.dispatchEvent(new Event('change'));
      await new Promise((r) => setTimeout(r, 0));

      expect(defaultTerm.value).toBe('3');
      expect((document.getElementById('aws-ec2-term') as HTMLSelectElement).value).toBe('3');
    });

    test('cascade confirm skipped when no services would change', async () => {
      setupSettingsHandlers();
      // All services and default already at "3" (via parent beforeEach).
      // Flip default to "3" (self) and assert no prompt fires.
      const defaultTerm = document.getElementById('setting-default-term') as HTMLSelectElement;
      defaultTerm.value = '3';
      defaultTerm.dispatchEvent(new Event('change'));
      await new Promise((r) => setTimeout(r, 0));

      expect(mockConfirmDialog).not.toHaveBeenCalled();
    });

  });

  describe('loadGlobalSettings', () => {
    test('shows loading state and hides form initially', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: ['aws'],
          notification_email: 'test@example.com',
          auto_collect: true,
          default_term: 3,
          default_payment: 'all-upfront',
          default_coverage: 80,
          notification_days_before: 3
        },
        credentials: {}
      });

      const loadingEl = document.getElementById('settings-loading');
      const formEl = document.getElementById('global-settings-form');

      const promise = loadGlobalSettings();

      // After starting, loading should be shown
      expect(loadingEl?.classList.contains('hidden')).toBe(false);
      expect(formEl?.classList.contains('hidden')).toBe(true);

      await promise;
    });

    test('populates form with config data', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: ['aws', 'azure'],
          notification_email: 'admin@example.com',
          auto_collect: true,
          default_term: 1,
          default_payment: 'partial-upfront',
          default_coverage: 90,
          notification_days_before: 5
        },
        credentials: {}
      });

      await loadGlobalSettings();

      expect((document.getElementById('provider-aws') as HTMLInputElement).checked).toBe(true);
      expect((document.getElementById('provider-azure') as HTMLInputElement).checked).toBe(true);
      expect((document.getElementById('provider-gcp') as HTMLInputElement).checked).toBe(false);
      expect((document.getElementById('setting-notification-email') as HTMLInputElement).value).toBe('admin@example.com');
      expect((document.getElementById('setting-auto-collect') as HTMLInputElement).checked).toBe(true);
      expect((document.getElementById('setting-default-term') as HTMLSelectElement).value).toBe('1');
      expect((document.getElementById('setting-default-payment') as HTMLSelectElement).value).toBe('partial-upfront');
      expect((document.getElementById('setting-default-coverage') as HTMLInputElement).value).toBe('90');
      expect((document.getElementById('setting-notification-days') as HTMLInputElement).value).toBe('5');
    });

    test('populates 4-eyes checkbox from config.global.require_different_approver (issue #1005)', async () => {
      const checkbox = document.createElement('input');
      checkbox.type = 'checkbox';
      checkbox.id = 'setting-require-different-approver';
      document.getElementById('global-settings-form')?.appendChild(checkbox);

      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: [],
          default_term: 3,
          default_payment: 'all-upfront',
          default_coverage: 80,
          notification_days_before: 3,
          require_different_approver: true
        },
        credentials: {}
      });

      await loadGlobalSettings();

      expect(checkbox.checked).toBe(true);
    });

    test('populates the experimental GCP plan-term checkbox: off when absent, on when true (issue #858)', async () => {
      const checkbox = document.createElement('input');
      checkbox.type = 'checkbox';
      checkbox.id = 'setting-experimental-gcp-plan-term';
      checkbox.checked = true;
      document.getElementById('global-settings-form')?.appendChild(checkbox);

      const global = {
        enabled_providers: [],
        default_term: 3,
        default_payment: 'all-upfront',
        default_coverage: 80,
        notification_days_before: 3,
      };
      (api.getConfig as jest.Mock).mockResolvedValue({ global, credentials: {} });
      await loadGlobalSettings();
      expect(checkbox.checked).toBe(false);

      (api.getConfig as jest.Mock).mockResolvedValue({
        global: { ...global, experimental_gcp_plan_term: true },
        credentials: {},
      });
      await loadGlobalSettings();
      expect(checkbox.checked).toBe(true);
    });

    test('populates collection schedule from config', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: [],
          collection_schedule: 'weekly',
          default_term: 3,
          default_payment: 'all-upfront',
          default_coverage: 80,
          notification_days_before: 3
        },
        credentials: {}
      });

      await loadGlobalSettings();

      expect((document.getElementById('setting-collection-schedule') as HTMLSelectElement).value).toBe('weekly');
    });

    test('handles auto_collect being false explicitly', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: [],
          auto_collect: false,
          default_term: 3,
          default_payment: 'all-upfront',
          default_coverage: 80,
          notification_days_before: 3
        },
        credentials: {}
      });

      await loadGlobalSettings();

      expect((document.getElementById('setting-auto-collect') as HTMLInputElement).checked).toBe(false);
    });

    test('shows error on API failure', async () => {
      (api.getConfig as jest.Mock).mockRejectedValue(new Error('API Error'));

      await loadGlobalSettings();

      const errorEl = document.getElementById('settings-error');
      expect(errorEl?.classList.contains('hidden')).toBe(false);
      expect(errorEl?.textContent).toContain('Failed to load settings');
    });

    test('hides loading and shows form on success', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: [],
          default_term: 3,
          default_payment: 'all-upfront',
          default_coverage: 80,
          notification_days_before: 3
        },
        credentials: {}
      });

      await loadGlobalSettings();

      const loadingEl = document.getElementById('settings-loading');
      const formEl = document.getElementById('global-settings-form');

      expect(loadingEl?.classList.contains('hidden')).toBe(true);
      expect(formEl?.classList.contains('hidden')).toBe(false);
    });

    test('handles missing global config gracefully', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        credentials: {}
      });

      await loadGlobalSettings();

      // Should not throw and form should be visible
      const formEl = document.getElementById('global-settings-form');
      expect(formEl?.classList.contains('hidden')).toBe(false);
    });

    // Issue #301: recommendations cycle params populated from API
    test('populates recommendations_cache_stale_hours and recommendations_lookback_days', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: ['aws'],
          default_term: 3,
          default_payment: 'all-upfront',
          default_coverage: 80,
          notification_days_before: 3,
          recommendations_cache_stale_hours: 48,
          recommendations_lookback_days: 30,
        },
      });

      await loadGlobalSettings();

      expect((document.getElementById('setting-recs-stale-hours') as HTMLInputElement).value).toBe('48');
      expect((document.getElementById('setting-recs-lookback-days') as HTMLSelectElement).value).toBe('30');
    });

    test('populates recommendations fields with defaults when absent from API response', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: ['aws'],
          default_term: 3,
          default_payment: 'all-upfront',
          default_coverage: 80,
          notification_days_before: 3,
          // recommendations_cache_stale_hours and recommendations_lookback_days absent
        },
      });

      await loadGlobalSettings();

      expect((document.getElementById('setting-recs-stale-hours') as HTMLInputElement).value).toBe('24');
      expect((document.getElementById('setting-recs-lookback-days') as HTMLSelectElement).value).toBe('7');
    });

    test('handles missing credentials config gracefully', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: ['aws'],
          default_term: 3,
          default_payment: 'all-upfront',
          default_coverage: 80,
          notification_days_before: 3
        }
      });

      await loadGlobalSettings();

      // Should not throw
      const formEl = document.getElementById('global-settings-form');
      expect(formEl?.classList.contains('hidden')).toBe(false);
    });

    test('populates service card selects from data.services', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: ['aws'],
          default_term: 3,
          default_payment: 'all-upfront',
          default_coverage: 80,
          notification_days_before: 3
        },
        services: [
          { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'no-upfront', coverage: 70 },
          { provider: 'aws', service: 'rds', enabled: true, term: 3, payment: 'all-upfront', coverage: 80 }
        ]
      });

      await loadGlobalSettings();

      expect((document.getElementById('aws-ec2-term') as HTMLSelectElement).value).toBe('1');
      expect((document.getElementById('aws-ec2-payment') as HTMLSelectElement).value).toBe('no-upfront');
      expect((document.getElementById('aws-rds-term') as HTMLSelectElement).value).toBe('3');
    });
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

    test('prevents default form submission', async () => {
      (api.updateConfig as jest.Mock).mockResolvedValue({});

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);

      expect(event.preventDefault).toHaveBeenCalled();
    });

    test('collects form data and calls updateConfig', async () => {
      // Load a config that differs in every field, then fill the form, so each
      // field is dirty and the PUT carries all of them.
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: ['azure'],
          notification_email: 'old@test.com',
          auto_collect: false,
          default_term: 1,
          default_payment: 'no-upfront',
          default_coverage: 10,
          notification_days_before: 5,
          grace_period_days: { aws: 1, azure: 2, gcp: 3 },
          recommendations_cache_stale_hours: 12,
          recommendations_lookback_days: 30,
        },
        services: [],
      });
      await loadGlobalSettings();
      fillForm();
      (api.updateConfig as jest.Mock).mockClear().mockResolvedValue({});
      window.alert = jest.fn();

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);

      // The harness has no schedule, grace, stale-hours, lookback, offering-class
      // or approver elements, so those read the same before and after and are
      // not sent.
      expect(api.updateConfig).toHaveBeenCalledWith({
        enabled_providers: ['aws', 'gcp'],
        notification_email: 'test@test.com',
        auto_collect: true,
        default_term: 3,
        default_payment: 'all-upfront',
        default_coverage: 80,
        notification_days_before: 3,
      });
    });

    test('shows success alert on save', async () => {
      (api.updateConfig as jest.Mock).mockResolvedValue({});
      window.alert = jest.fn();

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);

      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
        message: 'Settings saved successfully',
        kind: 'success',
      }));
    });

    test('sends require_different_approver: true when the 4-eyes checkbox is checked (issue #1005)', async () => {
      const checkbox = document.createElement('input');
      checkbox.type = 'checkbox';
      checkbox.id = 'setting-require-different-approver';
      checkbox.checked = true;
      document.getElementById('global-settings-form')?.appendChild(checkbox);

      (api.updateConfig as jest.Mock).mockResolvedValue({});
      window.alert = jest.fn();

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);

      expect(api.updateConfig).toHaveBeenCalledWith(expect.objectContaining({
        require_different_approver: true,
      }));
    });

    test('sends experimental_gcp_plan_term from the checkbox (issue #858)', async () => {
      const checkbox = document.createElement('input');
      checkbox.type = 'checkbox';
      checkbox.id = 'setting-experimental-gcp-plan-term';
      checkbox.checked = true;
      document.getElementById('global-settings-form')?.appendChild(checkbox);

      (api.updateConfig as jest.Mock).mockResolvedValue({});
      window.alert = jest.fn();

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);

      expect(api.updateConfig).toHaveBeenCalledWith(expect.objectContaining({
        experimental_gcp_plan_term: true,
      }));
    });

    test('shows error alert on failure', async () => {
      (api.updateConfig as jest.Mock).mockRejectedValue(new Error('Save failed'));
      window.alert = jest.fn();

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);

      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
        message: 'Failed to save settings: Save failed',
        kind: 'error',
      }));
    });

    test('handles missing form elements gracefully', async () => {
      // Remove all form elements
      document.body.innerHTML = '';
      (api.updateConfig as jest.Mock).mockResolvedValue({});
      window.alert = jest.fn();

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await expect(saveGlobalSettings(event)).resolves.toBeUndefined();

      // No provider checkbox exists, so the empty list is refused (#1531)
      // instead of being written.
      expect(api.updateConfig).not.toHaveBeenCalled();
    });

    // Issue #22 follow-up: AWS Savings Plans split into four per-plan-type
    // cards (Compute / EC2 Instance / SageMaker / Database). Each gets
    // a dedicated card in index.html so users can pin term/payment per
    // workload independently. Lambda is intentionally absent — it has no
    // standalone SP product (Lambda commitments roll into Compute SP).
    test('four per-plan-type SP cards present in index.html (issue #22)', () => {
      const fs = require('fs') as typeof import('fs');
      const path = require('path') as typeof import('path');
      const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf-8');
      expect(html).toMatch(/<h5>\s*Compute Savings Plans\s*<\/h5>/);
      expect(html).toMatch(/<h5>\s*EC2 Instance Savings Plans\s*<\/h5>/);
      expect(html).toMatch(/<h5>\s*SageMaker Savings Plans\s*<\/h5>/);
      expect(html).toMatch(/<h5>\s*Database Savings Plans\s*<\/h5>/);
      for (const planType of ['compute', 'ec2instance', 'sagemaker', 'database']) {
        expect(html).toMatch(new RegExp(`id="aws-savings-plans-${planType}-term"`));
        expect(html).toMatch(new RegExp(`id="aws-savings-plans-${planType}-payment"`));
        // Issue #136: coverage and enabled controls must be present on each card.
        expect(html).toMatch(new RegExp(`id="aws-savings-plans-${planType}-coverage"`));
        expect(html).toMatch(new RegExp(`id="aws-savings-plans-${planType}-enabled"`));
      }
    });

    test('no Lambda card in index.html — Lambda has no standalone SP, only Compute SP (issue #22)', () => {
      const fs = require('fs') as typeof import('fs');
      const path = require('path') as typeof import('path');
      const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf-8');
      // Guard against accidental re-introduction of a Lambda-specific card.
      expect(html).not.toMatch(/<h5>\s*Lambda Savings Plans\s*<\/h5>/);
      expect(html).not.toMatch(/id="aws-lambda-term"/);
      expect(html).not.toMatch(/id="aws-lambda-payment"/);
    });

    test('saveGlobalSettings sends per-plan-type SP term and payment (issue #22)', async () => {
      (api.updateConfig as jest.Mock).mockResolvedValue({});
      (api.updateServiceConfig as jest.Mock).mockClear().mockResolvedValue(undefined);
      window.alert = jest.fn();

      // User pins SageMaker SP to 1yr / no-upfront while leaving Compute
      // SP at the global default (3yr / all-upfront, matching the seeded
      // baseline from beforeEach).
      (document.getElementById('aws-savings-plans-sagemaker-term') as HTMLSelectElement).value = '1';
      (document.getElementById('aws-savings-plans-sagemaker-payment') as HTMLSelectElement).value = 'no-upfront';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);

      const call = (api.updateServiceConfig as jest.Mock).mock.calls.find(
        ([provider, service]) => provider === 'aws' && service === 'savings-plans-sagemaker',
      );
      expect(call).toBeDefined();
      const cfg = call![2];
      expect(cfg.term).toBe(1);
      expect(cfg.payment).toBe('no-upfront');
    });

    test('saveGlobalSettings sends per-card coverage from SP coverage input (issue #136)', async () => {
      await loadPristine();
      (api.updateConfig as jest.Mock).mockResolvedValue({});
      (api.updateServiceConfig as jest.Mock).mockClear().mockResolvedValue(undefined);
      window.alert = jest.fn();

      // Pin compute SP to 60% coverage, leave others at default 80%.
      (document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value = '60';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await saveGlobalSettings(event);

      const computeCall = (api.updateServiceConfig as jest.Mock).mock.calls.find(
        ([provider, service]) => provider === 'aws' && service === 'savings-plans-compute',
      );
      expect(computeCall).toBeDefined();
      expect(computeCall![2].coverage).toBe(60);

      // Other SP cards were not edited, so they are not sent (#539).
      const ec2Call = (api.updateServiceConfig as jest.Mock).mock.calls.find(
        ([provider, service]) => provider === 'aws' && service === 'savings-plans-ec2instance',
      );
      expect(ec2Call).toBeUndefined();
    });

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

      test('a changed global coverage sends no service PUTs and shows the propagated value on the SP cards', async () => {
        await loadWithCoverage50();
        (document.getElementById('setting-default-coverage') as HTMLInputElement).value = '0';

        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(api.updateConfig).toHaveBeenCalledWith({ default_coverage: 0 });
        expect(api.updateServiceConfig).not.toHaveBeenCalled();
        expect((document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value).toBe('0');
        expect(isUnsavedChanges()).toBe(false);
      });

      test('an SP card coverage edited in the same save wins over the new default and is the only service PUT', async () => {
        await loadWithCoverage50();
        (document.getElementById('setting-default-coverage') as HTMLInputElement).value = '70';
        (document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value = '60';

        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(coverageSentFor('ec2')).toBeUndefined();
        expect(coverageSentFor('savings-plans-compute')).toBe(60);
        expect(api.updateServiceConfig).toHaveBeenCalledTimes(1);
        expect((document.getElementById('aws-savings-plans-ec2instance-coverage') as HTMLInputElement).value).toBe('70');
      });

      test('a second save without reload resends nothing', async () => {
        await loadWithCoverage50();
        (document.getElementById('setting-default-coverage') as HTMLInputElement).value = '70';
        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);
        (api.updateConfig as jest.Mock).mockClear();
        (api.updateServiceConfig as jest.Mock).mockClear();

        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(api.updateConfig).not.toHaveBeenCalled();
        expect(api.updateServiceConfig).not.toHaveBeenCalled();
      });

      test('an SP card edit saved after a global change is not resent by a further save', async () => {
        await loadWithCoverage50();
        (document.getElementById('setting-default-coverage') as HTMLInputElement).value = '0';
        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);
        (document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value = '70';
        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);
        expect(coverageSentFor('savings-plans-compute')).toBe(70);
        (api.updateServiceConfig as jest.Mock).mockClear();

        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(api.updateServiceConfig).not.toHaveBeenCalled();
        expect((document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value).toBe('70');
      });

      test('a saved SP card edit does not shield the card from a later global change', async () => {
        await loadWithCoverage50();
        (document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value = '70';
        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);
        (document.getElementById('setting-default-coverage') as HTMLInputElement).value = '20';
        (api.updateServiceConfig as jest.Mock).mockClear();

        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(api.updateServiceConfig).not.toHaveBeenCalled();
        expect((document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value).toBe('20');
      });

      test('after a partial failure the retry resends only the failed SP card coverage', async () => {
        await loadWithCoverage50();
        (document.getElementById('setting-default-coverage') as HTMLInputElement).value = '0';
        (document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value = '70';
        (document.getElementById('aws-savings-plans-ec2instance-coverage') as HTMLInputElement).value = '30';
        (api.updateServiceConfig as jest.Mock).mockImplementation(async (provider: string, svc: string) => {
          if (provider === 'aws' && svc === 'savings-plans-ec2instance') throw new Error('sp failed');
        });

        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);
        expect(coverageSentFor('savings-plans-compute')).toBe(70);

        (api.updateServiceConfig as jest.Mock).mockClear().mockResolvedValue(undefined);
        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(coverageSentFor('savings-plans-compute')).toBeUndefined();
        expect(coverageSentFor('savings-plans-ec2instance')).toBe(30);
      });

      test('after a failed service PUT a further global change still reaches an unedited SP card', async () => {
        await loadWithCoverage50();
        (document.getElementById('aws-savings-plans-ec2instance-coverage') as HTMLInputElement).value = '30';
        (api.updateServiceConfig as jest.Mock).mockImplementation(async (provider: string, svc: string) => {
          if (provider === 'aws' && svc === 'savings-plans-ec2instance') throw new Error('sp failed');
        });
        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        (document.getElementById('setting-default-coverage') as HTMLInputElement).value = '20';
        (api.updateServiceConfig as jest.Mock).mockClear().mockResolvedValue(undefined);
        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(coverageSentFor('savings-plans-ec2instance')).toBe(30);
        expect((document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value).toBe('20');
      });

      test('a rejected global PUT leaves the coverage dirty and sends no service PUTs', async () => {
        await loadWithCoverage50();
        (document.getElementById('setting-default-coverage') as HTMLInputElement).value = '70';
        (api.updateConfig as jest.Mock).mockRejectedValueOnce(new Error('boom'));

        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(api.updateServiceConfig).not.toHaveBeenCalled();
        expect(isUnsavedChanges()).toBe(true);
      });

      test('an SP card coverage whose service PUT failed stays dirty and is resent', async () => {
        await loadWithCoverage50();
        (document.getElementById('aws-savings-plans-compute-coverage') as HTMLInputElement).value = '70';
        (api.updateServiceConfig as jest.Mock).mockImplementation(async (provider: string, svc: string) => {
          if (provider === 'aws' && svc === 'savings-plans-compute') throw new Error('sp failed');
        });

        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);
        expect(isUnsavedChanges()).toBe(true);

        (api.updateServiceConfig as jest.Mock).mockClear().mockResolvedValue(undefined);
        await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

        expect(coverageSentFor('savings-plans-compute')).toBe(70);
        expect(isUnsavedChanges()).toBe(false);
      });

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
  }); // end saveGlobalSettings

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

  describe('copyToClipboard', () => {
    let originalClipboard: Clipboard;

    beforeEach(() => {
      originalClipboard = navigator.clipboard;
    });

    afterEach(() => {
      Object.defineProperty(navigator, 'clipboard', {
        value: originalClipboard,
        writable: true
      });
    });

    test('copies text from element to clipboard', async () => {
      const writeTextMock = jest.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, 'clipboard', {
        value: { writeText: writeTextMock },
        writable: true
      });

      copyToClipboard('test-copy-element');

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(writeTextMock).toHaveBeenCalledWith('test-value');
    });

    test('shows copied feedback on button', async () => {
      jest.useFakeTimers();

      const writeTextMock = jest.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, 'clipboard', {
        value: { writeText: writeTextMock },
        writable: true
      });

      copyToClipboard('test-copy-element');

      // Flush microtasks for the promise to resolve
      await Promise.resolve();
      jest.runAllTicks();

      const btn = document.querySelector('.copy-btn') as HTMLButtonElement;
      // The innerHTML will contain the actual checkmark character, not the HTML entity
      expect(btn.classList.contains('copied')).toBe(true);

      // Fast-forward timer
      jest.advanceTimersByTime(2000);

      expect(btn.innerHTML).toContain('Copy');
      expect(btn.classList.contains('copied')).toBe(false);

      jest.useRealTimers();
    });

    test('handles non-existent element gracefully', () => {
      const writeTextMock = jest.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, 'clipboard', {
        value: { writeText: writeTextMock },
        writable: true
      });

      // Should not throw
      expect(() => copyToClipboard('non-existent-element')).not.toThrow();
      expect(writeTextMock).not.toHaveBeenCalled();
    });

    test('handles clipboard error gracefully', async () => {
      jest.useFakeTimers();

      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      const writeTextMock = jest.fn().mockRejectedValue(new Error('Clipboard error'));
      Object.defineProperty(navigator, 'clipboard', {
        value: { writeText: writeTextMock },
        writable: true
      });

      copyToClipboard('test-copy-element');

      // Flush all promises
      await Promise.resolve();
      jest.runAllTicks();
      await Promise.resolve();

      expect(consoleError).toHaveBeenCalledWith('Failed to copy:', expect.any(Error));
      consoleError.mockRestore();
      jest.useRealTimers();
    });
  });

  describe('Azure payment defaults (issue #12)', () => {
    // Shared fixture covers term selects only; inject the four Azure payment
    // selects this issue introduces so loadGlobalSettings / saveGlobalSettings
    // have DOM nodes to read / write.
    const injectAzurePaymentSelects = () => {
      const form = document.getElementById('global-settings-form');
      if (!form) throw new Error('global-settings-form fixture missing');
      for (const svc of ['vm', 'sql', 'cosmosdb', 'redis']) {
        if (document.getElementById(`azure-${svc}-payment`)) continue;
        const select = document.createElement('select');
        select.id = `azure-${svc}-payment`;
        for (const [value, label] of [['upfront', 'Upfront'], ['monthly', 'Monthly']] as const) {
          const opt = document.createElement('option');
          opt.value = value;
          opt.textContent = label;
          if (value === 'upfront') opt.selected = true;
          select.appendChild(opt);
        }
        form.appendChild(select);
      }
    };

    test('loadGlobalSettings applies persisted azure-vm payment', async () => {
      injectAzurePaymentSelects();
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: {
          enabled_providers: ['aws', 'azure'],
          default_term: 3,
          default_payment: 'all-upfront',
          default_coverage: 80,
        },
        services: [
          { provider: 'azure', service: 'vm', term: 3, payment: 'monthly', enabled: true, coverage: 80 },
        ],
      });

      await loadGlobalSettings();

      const vmPayment = document.getElementById('azure-vm-payment') as HTMLSelectElement;
      expect(vmPayment.value).toBe('monthly');
    });

    test('saveGlobalSettings sends the per-service Azure payment, not the global default', async () => {
      injectAzurePaymentSelects();
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: { enabled_providers: ['aws', 'azure'], default_term: 3, default_payment: 'all-upfront', default_coverage: 80 },
        services: [],
      });
      (api.updateConfig as jest.Mock).mockResolvedValue({});
      (api.updateServiceConfig as jest.Mock).mockClear().mockResolvedValue(undefined);

      await loadGlobalSettings();
      // User flips Azure VM to Monthly while global default stays all-upfront.
      (document.getElementById('azure-vm-payment') as HTMLSelectElement).value = 'monthly';
      (document.getElementById('setting-default-payment') as HTMLSelectElement).value = 'all-upfront';

      await saveGlobalSettings({ preventDefault: jest.fn() } as unknown as Event);

      const azureVmCall = (api.updateServiceConfig as jest.Mock).mock.calls.find(
        ([provider, service]) => provider === 'azure' && service === 'compute',
      );
      expect(azureVmCall).toBeDefined();
      const cfg = azureVmCall![2];
      expect(cfg.payment).toBe('monthly');
    });

    test('help text describes the correct per-provider payment semantics', () => {
      // The help copy lives in index.html (loaded into the test fixture
      // verbatim at test startup). Guard against regressions in the
      // wording — the old copy falsely claimed Azure was always upfront.
      const fs = require('fs') as typeof import('fs');
      const path = require('path') as typeof import('path');
      const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf-8');
      expect(html).toMatch(/Azure reservations support upfront or monthly/);
      expect(html).not.toMatch(/Azure and GCP reservations are always paid upfront/);
    });
  });

  // Guard against the RDS 3yr + no-upfront regression from follow-up to
  // issue #12. The backend rejects that combination (and EC/OpenSearch/
  // Redshift 3yr no-upfront), so the Settings form must not allow it.
  // Rules live in commitmentOptions.ts; these tests exercise the wiring
  // that applies them to the per-service dropdowns.
  describe('per-service term/payment combination constraints', () => {
    const optVisible = (sel: HTMLSelectElement, value: string): boolean => {
      const opt = Array.from(sel.options).find(o => o.value === value);
      if (!opt) return false;
      return !opt.hidden && !opt.disabled;
    };

    test('RDS 3yr hides "no-upfront" and keeps partial/all upfront selectable', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: { enabled_providers: ['aws'], default_term: 3, default_payment: 'all-upfront', default_coverage: 80 },
        services: [{ provider: 'aws', service: 'rds', term: 3, payment: 'all-upfront' }],
      });
      setupSettingsHandlers();
      await loadGlobalSettings();

      const rdsPayment = document.getElementById('aws-rds-payment') as HTMLSelectElement;
      expect(optVisible(rdsPayment, 'no-upfront')).toBe(false);
      expect(optVisible(rdsPayment, 'partial-upfront')).toBe(true);
      expect(optVisible(rdsPayment, 'all-upfront')).toBe(true);
    });

    test('RDS 1yr keeps all three payment options visible', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: { enabled_providers: ['aws'], default_term: 1, default_payment: 'no-upfront', default_coverage: 80 },
        services: [{ provider: 'aws', service: 'rds', term: 1, payment: 'no-upfront' }],
      });
      setupSettingsHandlers();
      await loadGlobalSettings();

      const rdsPayment = document.getElementById('aws-rds-payment') as HTMLSelectElement;
      expect(optVisible(rdsPayment, 'no-upfront')).toBe(true);
      expect(optVisible(rdsPayment, 'partial-upfront')).toBe(true);
      expect(optVisible(rdsPayment, 'all-upfront')).toBe(true);
    });

    test('switching RDS term 1yr → 3yr while "no-upfront" is selected auto-clamps payment', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: { enabled_providers: ['aws'], default_term: 1, default_payment: 'no-upfront', default_coverage: 80 },
        services: [{ provider: 'aws', service: 'rds', term: 1, payment: 'no-upfront' }],
      });
      setupSettingsHandlers();
      await loadGlobalSettings();

      const rdsTerm = document.getElementById('aws-rds-term') as HTMLSelectElement;
      const rdsPayment = document.getElementById('aws-rds-payment') as HTMLSelectElement;
      expect(rdsPayment.value).toBe('no-upfront');

      rdsTerm.value = '3';
      rdsTerm.dispatchEvent(new Event('change'));

      // no-upfront is now invalid; payment should snap to first valid option
      expect(rdsPayment.value).not.toBe('no-upfront');
      expect(['partial-upfront', 'all-upfront']).toContain(rdsPayment.value);
      expect(optVisible(rdsPayment, 'no-upfront')).toBe(false);
    });

    test('legacy-persisted invalid combo (RDS 3yr + no-upfront) is clamped on load', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: { enabled_providers: ['aws'], default_term: 3, default_payment: 'all-upfront', default_coverage: 80 },
        // Simulate a config stored before this guardrail existed.
        services: [{ provider: 'aws', service: 'rds', term: 3, payment: 'no-upfront' }],
      });
      setupSettingsHandlers();
      await loadGlobalSettings();

      const rdsPayment = document.getElementById('aws-rds-payment') as HTMLSelectElement;
      expect(rdsPayment.value).not.toBe('no-upfront');
    });

    test('EC2 3yr keeps all three payment options visible (no service-level restriction)', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: { enabled_providers: ['aws'], default_term: 3, default_payment: 'no-upfront', default_coverage: 80 },
        services: [{ provider: 'aws', service: 'ec2', term: 3, payment: 'no-upfront' }],
      });
      setupSettingsHandlers();
      await loadGlobalSettings();

      const ec2Payment = document.getElementById('aws-ec2-payment') as HTMLSelectElement;
      expect(optVisible(ec2Payment, 'no-upfront')).toBe(true);
      expect(optVisible(ec2Payment, 'partial-upfront')).toBe(true);
      expect(optVisible(ec2Payment, 'all-upfront')).toBe(true);
      expect(ec2Payment.value).toBe('no-upfront');
    });

    test.each(['elasticache', 'opensearch', 'redshift'])(
      '%s 3yr keeps "no-upfront" visible (AWS only restricts RDS)',
      async (service) => {
        (api.getConfig as jest.Mock).mockResolvedValue({
          global: { enabled_providers: ['aws'], default_term: 3, default_payment: 'no-upfront', default_coverage: 80 },
          services: [{ provider: 'aws', service, term: 3, payment: 'no-upfront' }],
        });
        setupSettingsHandlers();
        await loadGlobalSettings();

        const payment = document.getElementById(`aws-${service}-payment`) as HTMLSelectElement;
        expect(optVisible(payment, 'no-upfront')).toBe(true);
        // And the selected value round-trips cleanly — the backend persists
        // this service with no-upfront, and the UI should not clamp it.
        expect(payment.value).toBe('no-upfront');
      },
    );

    // Empty-state regression: on a fresh install where no per-product
    // service_configs rows exist (e.g., right after migration 000040 ran
    // but before any user save), loadGlobalSettings must not blow up,
    // and the four SP card selects must remain present + interactable.
    // This protects against a regression where the load path tried to
    // touch a missing service entry and threw, leaving the cards in a
    // broken state. The selects keep their HTML-default values (3 /
    // all-upfront) per the existing per-service load semantics — the
    // wider "apply globalCfg.default_term / default_payment to empty
    // selects" cascade is OUT OF SCOPE for this PR (see plan §6.5).
    test('per-plan-type SP cards remain interactable when no service rows exist (issue #22 follow-up)', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: { enabled_providers: ['aws'], default_term: 1, default_payment: 'no-upfront', default_coverage: 80 },
        services: [], // no per-service overrides
      });
      setupSettingsHandlers();
      await loadGlobalSettings();

      for (const planType of ['compute', 'ec2instance', 'sagemaker', 'database']) {
        const term = document.getElementById(`aws-savings-plans-${planType}-term`) as HTMLSelectElement;
        const payment = document.getElementById(`aws-savings-plans-${planType}-payment`) as HTMLSelectElement;
        expect(term).not.toBeNull();
        expect(payment).not.toBeNull();
        // Cards keep the HTML-default 3yr/all-upfront baseline (matching
        // the existing umbrella card's pre-rev-2 behaviour).
        expect(term.value).toBe('3');
        expect(payment.value).toBe('all-upfront');
        // All 6 (term × payment) combos remain selectable on the new
        // SP keys via the _default fallback in commitmentOptions.
        expect(optVisible(payment, 'no-upfront')).toBe(true);
        expect(optVisible(payment, 'partial-upfront')).toBe(true);
        expect(optVisible(payment, 'all-upfront')).toBe(true);
      }
    });

    test('SageMaker 3yr keeps "no-upfront" visible (issue #22 — no service-level restriction)', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: { enabled_providers: ['aws'], default_term: 3, default_payment: 'no-upfront', default_coverage: 80 },
        services: [{ provider: 'aws', service: 'savings-plans-sagemaker', term: 3, payment: 'no-upfront' }],
      });
      setupSettingsHandlers();
      await loadGlobalSettings();

      const payment = document.getElementById('aws-savings-plans-sagemaker-payment') as HTMLSelectElement;
      expect(optVisible(payment, 'no-upfront')).toBe(true);
      expect(optVisible(payment, 'partial-upfront')).toBe(true);
      expect(optVisible(payment, 'all-upfront')).toBe(true);
      expect(payment.value).toBe('no-upfront');
    });

    test('propagating global "no-upfront" to all services while term=3 clamps restricted services', async () => {
      (api.getConfig as jest.Mock).mockResolvedValue({
        global: { enabled_providers: ['aws'], default_term: 3, default_payment: 'all-upfront', default_coverage: 80 },
        services: [
          { provider: 'aws', service: 'ec2', term: 3, payment: 'all-upfront' },
          { provider: 'aws', service: 'rds', term: 3, payment: 'all-upfront' },
        ],
      });
      setupSettingsHandlers();
      await loadGlobalSettings();

      // User changes the global default to no-upfront and confirms the propagation.
      mockConfirmDialog.mockResolvedValue(true);
      const defaultPayment = document.getElementById('setting-default-payment') as HTMLSelectElement;
      defaultPayment.dataset['previous'] = 'all-upfront';
      defaultPayment.value = 'no-upfront';
      defaultPayment.dispatchEvent(new Event('change'));

      // Allow the async confirmDialog promise to resolve.
      await Promise.resolve();
      await Promise.resolve();

      const ec2Payment = document.getElementById('aws-ec2-payment') as HTMLSelectElement;
      const rdsPayment = document.getElementById('aws-rds-payment') as HTMLSelectElement;
      // EC2 accepts the propagated no-upfront (no restriction).
      expect(ec2Payment.value).toBe('no-upfront');
      // RDS 3yr rejects no-upfront, so it clamps back to the first valid option.
      expect(rdsPayment.value).not.toBe('no-upfront');
    });
  });

  // -----------------------------------------------------------------------
  // Inline range validation — top-level numeric settings (issue #1411)
  // -----------------------------------------------------------------------
  // These inputs have min/max in the test DOM (added alongside the fix) so
  // wireInlineRangeValidation creates the error element and wires the
  // 'input'/'blur' events. Tests confirm the regression is gone: inline
  // error appears immediately as the user types, not only after Save.
  describe('inline range validation on top-level numeric settings (#1411)', () => {
    function fire(el: HTMLInputElement, type: 'input' | 'blur'): void {
      el.dispatchEvent(new Event(type));
    }

    beforeEach(() => {
      setupSettingsHandlers();
    });

    it.each([
      ['setting-notification-days', '0',     'Must be a whole number between 1 and 30'],
      ['setting-notification-days', '31',    'Must be a whole number between 1 and 30'],
      ['setting-recs-stale-hours',  '-1',    'Must be a whole number between 0 and 8760'],
      ['setting-default-coverage',  '101',   'Must be a whole number between 0 and 100'],
    ])('typing %s=%s shows inline error with message %s', (id, val, msg) => {
      const input = document.getElementById(id) as HTMLInputElement;
      input.value = val;
      fire(input, 'input');

      expect(input.getAttribute('aria-invalid')).toBe('true');
      const errEl = document.getElementById(`${id}-range-error`);
      expect(errEl).not.toBeNull();
      expect(errEl!.classList.contains('hidden')).toBe(false);
      expect(errEl!.textContent).toBe(msg);
    });

    it('clearing the default coverage field shows the inline error instead of passing as 0 (#522)', () => {
      const input = document.getElementById('setting-default-coverage') as HTMLInputElement;
      input.value = '';
      fire(input, 'input');

      expect(input.getAttribute('aria-invalid')).toBe('true');
      const errEl = document.getElementById('setting-default-coverage-range-error');
      expect(errEl!.classList.contains('hidden')).toBe(false);
      expect(errEl!.textContent).toBe('Must be a whole number between 0 and 100');
    });

    it('clearing an optional numeric field stays without an inline error', () => {
      const input = document.getElementById('setting-notification-days') as HTMLInputElement;
      input.value = '';
      fire(input, 'input');

      expect(input.getAttribute('aria-invalid')).toBeNull();
    });

    it('typing a fractional value into notification-days is rejected inline', () => {
      const input = document.getElementById('setting-notification-days') as HTMLInputElement;
      input.value = '1.5';
      fire(input, 'input');

      expect(input.getAttribute('aria-invalid')).toBe('true');
      const errEl = document.getElementById('setting-notification-days-range-error');
      expect(errEl!.classList.contains('hidden')).toBe(false);
    });

    it('restoring a valid value clears the inline error', () => {
      const input = document.getElementById('setting-notification-days') as HTMLInputElement;
      input.value = '0';
      fire(input, 'input');
      expect(input.getAttribute('aria-invalid')).toBe('true');

      input.value = '5';
      fire(input, 'input');
      expect(input.getAttribute('aria-invalid')).toBeNull();
      const errEl = document.getElementById('setting-notification-days-range-error');
      expect(errEl!.classList.contains('hidden')).toBe(true);
    });
  });

  // -----------------------------------------------------------------------
  // Inline range validation — SP coverage inputs (issue #1411 row 7.13)
  // -----------------------------------------------------------------------
  describe('inline range validation wired for SP coverage inputs (#1411 row 7.13)', () => {
    function fire(el: HTMLInputElement, type: 'input' | 'blur'): void {
      el.dispatchEvent(new Event(type));
    }

    beforeEach(() => {
      setupSettingsHandlers();
    });

    it.each([
      'aws-savings-plans-compute-coverage',
      'aws-savings-plans-ec2instance-coverage',
      'aws-savings-plans-sagemaker-coverage',
      'aws-savings-plans-database-coverage',
    ])('typing 101 into %s shows inline error', (id) => {
      const input = document.getElementById(id) as HTMLInputElement;
      input.value = '101';
      fire(input, 'input');

      expect(input.getAttribute('aria-invalid')).toBe('true');
      const errEl = document.getElementById(`${id}-range-error`);
      expect(errEl).not.toBeNull();
      expect(errEl!.classList.contains('hidden')).toBe(false);
      expect(errEl!.textContent).toBe('Must be a whole number between 0 and 100');
    });

    it('valid SP coverage value clears any prior error', () => {
      const id = 'aws-savings-plans-compute-coverage';
      const input = document.getElementById(id) as HTMLInputElement;
      input.value = '200';
      fire(input, 'input');
      expect(input.getAttribute('aria-invalid')).toBe('true');

      input.value = '80';
      fire(input, 'input');
      expect(input.getAttribute('aria-invalid')).toBeNull();
      const errEl = document.getElementById(`${id}-range-error`);
      expect(errEl!.classList.contains('hidden')).toBe(true);
    });
  });
});
