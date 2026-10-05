/**
 * Settings Save: every per-service PUT carries a payment token the backend
 * accepts for that service's provider (issue #545).
 *
 * The DOM is the real frontend/src/index.html and the accepted tokens are
 * read from internal/config/validation.go, so a card option or a backend
 * token change that makes them disagree fails here.
 */

import * as fs from 'fs';
import * as path from 'path';
import { loadGlobalSettings, saveGlobalSettings, setupSettingsHandlers } from '../settings';

jest.mock('../api', () => ({
  getConfig: jest.fn(),
  updateConfig: jest.fn(),
  updateServiceConfig: jest.fn(),
  getLadderConfigs: jest.fn().mockResolvedValue([]),
}));
jest.mock('../federation', () => ({
  initFederationPanel: jest.fn().mockResolvedValue(undefined),
}));
jest.mock('../confirmDialog', () => ({
  confirmDialog: jest.fn(() => Promise.resolve(true)),
}));
const mockShowToast = jest.fn<{ dismiss: () => void }, [unknown]>(() => ({ dismiss: jest.fn() }));
jest.mock('../toast', () => ({
  showToast: (opts: unknown) => mockShowToast(opts),
}));

import * as api from '../api';

function validPaymentsByProvider(): Record<string, string[]> {
  const src = fs.readFileSync(path.join(__dirname, '..', '..', '..', 'internal', 'config', 'validation.go'), 'utf-8');
  const block = /var ValidPaymentOptionsByProvider = map\[string\]\[\]string\{([\s\S]*?)\n\}/.exec(src);
  if (!block) throw new Error('ValidPaymentOptionsByProvider not found in validation.go');
  const out: Record<string, string[]> = {};
  for (const line of block[1]!.split('\n')) {
    const m = /"(\w+)":\s*\{([^}]*)\}/.exec(line);
    if (m) out[m[1]!] = [...m[2]!.matchAll(/"([\w-]+)"/g)].map(t => t[1]!);
  }
  if (Object.keys(out).length !== 3) throw new Error(`expected 3 providers in validation.go, parsed ${JSON.stringify(out)}`);
  return out;
}

function mountRealIndexHtml(): void {
  const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf-8');
  document.body.innerHTML = new DOMParser().parseFromString(html, 'text/html').body.innerHTML;
}

type ServiceCall = { provider: string; service: string; payment: string };

function serviceCalls(): ServiceCall[] {
  return (api.updateServiceConfig as jest.Mock).mock.calls.map(
    ([provider, service, cfg]) => ({ provider, service, payment: cfg.payment }),
  );
}

describe('Settings Save payment tokens (issue #545)', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    mountRealIndexHtml();
    (api.getConfig as jest.Mock).mockResolvedValue({
      global: {
        enabled_providers: ['aws', 'azure', 'gcp'],
        default_term: 3,
        default_payment: 'all-upfront',
        default_coverage: 80,
        notification_days_before: 3,
      },
      services: [],
    });
    (api.updateConfig as jest.Mock).mockResolvedValue(undefined);
    (api.updateServiceConfig as jest.Mock).mockResolvedValue(undefined);
  });

  async function save(): Promise<void> {
    await loadGlobalSettings();
    await saveGlobalSettings(new Event('submit'));
  }

  it('sends only backend-valid payments for the untouched card defaults', async () => {
    await save();

    const valid = validPaymentsByProvider();
    const calls = serviceCalls();
    expect(calls.filter(c => c.provider === 'azure')).toHaveLength(5);
    expect(calls.filter(c => c.provider === 'gcp')).toHaveLength(4);
    for (const c of calls) {
      expect({ ...c, valid: valid[c.provider]!.includes(c.payment) }).toEqual({ ...c, valid: true });
    }
  });

  it('sends the Azure option the user picked, and GCP monthly whatever the global default is', async () => {
    await loadGlobalSettings();
    (document.getElementById('azure-vm-payment') as HTMLSelectElement).value = 'monthly';
    (document.getElementById('setting-default-payment') as HTMLSelectElement).value = 'partial-upfront';
    await saveGlobalSettings(new Event('submit'));

    const calls = serviceCalls();
    expect(calls.find(c => c.provider === 'azure' && c.service === 'vm')!.payment).toBe('monthly');
    expect(calls.find(c => c.provider === 'azure' && c.service === 'sql')!.payment).toBe('upfront');
    for (const c of calls.filter(x => x.provider === 'gcp')) expect(c.payment).toBe('monthly');
  });

  it('does not push the AWS default payment into Azure cards', async () => {
    await loadGlobalSettings();
    setupSettingsHandlers();
    const def = document.getElementById('setting-default-payment') as HTMLSelectElement;
    def.value = 'partial-upfront';
    def.dispatchEvent(new Event('change'));
    await new Promise(resolve => setTimeout(resolve, 0));

    expect((document.getElementById('aws-ec2-payment') as HTMLSelectElement).value).toBe('partial-upfront');
    expect((document.getElementById('azure-vm-payment') as HTMLSelectElement).value).toBe('upfront');
  });

  it('loads a persisted Azure payment into its card', async () => {
    (api.getConfig as jest.Mock).mockResolvedValue({
      global: { enabled_providers: ['azure'], default_term: 3, default_payment: 'all-upfront', default_coverage: 80 },
      services: [{ provider: 'azure', service: 'vm', term: 3, payment: 'monthly', enabled: true, coverage: 80 }],
    });
    await loadGlobalSettings();
    expect((document.getElementById('azure-vm-payment') as HTMLSelectElement).value).toBe('monthly');
  });

  it('one failed service PUT reports that service, still saves the rest and keeps only its fields dirty', async () => {
    const consoleError = jest.spyOn(console, 'error').mockImplementation(() => undefined);
    (api.updateServiceConfig as jest.Mock).mockImplementation(async (provider: string, service: string) => {
      if (provider === 'azure' && service === 'vm') throw new Error('invalid payment option');
    });
    await loadGlobalSettings();
    const azurePayment = document.getElementById('azure-vm-payment') as HTMLSelectElement;
    azurePayment.value = 'monthly';
    const awsPayment = document.getElementById('aws-ec2-payment') as HTMLSelectElement;
    awsPayment.value = 'no-upfront';

    await saveGlobalSettings(new Event('submit'));

    expect(api.updateServiceConfig).toHaveBeenCalledTimes(18);
    expect(mockShowToast).toHaveBeenCalledTimes(1);
    expect(mockShowToast).toHaveBeenCalledWith({
      message: 'Settings saved, but 1 service failed: azure/vm: invalid payment option',
      kind: 'error',
    });
    expect(azurePayment.classList.contains('dirty')).toBe(true);
    expect(awsPayment.classList.contains('dirty')).toBe(false);
    consoleError.mockRestore();
  });
});
