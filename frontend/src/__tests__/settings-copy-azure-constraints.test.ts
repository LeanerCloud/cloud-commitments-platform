/**
 * Settings tests: copyToClipboard, Azure payment defaults and per-service term/payment constraints.
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
  copyToClipboard,
} from '../settings';

import * as api from '../api';

describe('Settings Module', () => {
  beforeEach(() => {
    setupSettingsTest();
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
});
