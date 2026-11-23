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
  setupSettingsHandlers,
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

  }); // end saveGlobalSettings
});
