/**
 * Settings tests: copyToClipboard, Azure payment defaults and per-service term/payment constraints.
 * Split from settings.test.ts to stay under the 500-line ceiling; the module
 * mocks and the DOM setup live in settings-harness.ts.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  setupSettingsTest,
} from './settings-harness';
import {
  loadGlobalSettings,
  saveGlobalSettings,
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
});
