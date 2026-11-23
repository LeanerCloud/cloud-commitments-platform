/**
 * Settings account tests: the per-account overrides panel (payment selector and
 * inline Term/Coverage/Enabled edits). Split from settings-accounts.test.ts to
 * stay under the 500-line ceiling; the module mocks and DOM helpers live in the
 * harness.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  mockShowToast,
  mockLoadRecommendations,
  buildAccountsDOM,
} from './settings-accounts-harness';
import {
  loadAccountsForProvider,
} from '../settings';

import * as api from '../api';

// ---------------------------------------------------------------------------
// Account overrides panel — payment option selector (issue #23)
// ---------------------------------------------------------------------------

describe('Overrides panel — AWS payment selector', () => {
  /**
   * Render the AWS accounts list and expand the first account's overrides
   * panel so the panel DOM is populated with whatever
   * listAccountServiceOverrides has been mocked to return.
   */
  async function openOverridesPanel(accountId = 'acc-1'): Promise<HTMLElement> {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: accountId, name: 'Prod', provider: 'aws', external_id: '111', enabled: true },
    ]);
    await loadAccountsForProvider('aws');
    const overridesBtn = document.querySelector(
      `button[aria-label="Service overrides for Prod (111)"]`,
    ) as HTMLButtonElement | null;
    expect(overridesBtn).not.toBeNull();
    overridesBtn!.click();
    // loadOverridesPanel is async; let microtasks flush.
    await new Promise(r => setTimeout(r, 0));
    // Issue #122: the inline expandable panel was replaced by a per-account
    // modal. The body element is what loadOverridesPanel renders into.
    const panel = document.getElementById('account-overrides-modal-body') as HTMLElement | null;
    expect(panel).not.toBeNull();
    return panel!;
  }

  beforeEach(() => {
    buildAccountsDOM();
    jest.clearAllMocks();
  });

  test('renders payment <select> with Inherit + 3 AWS options for AWS overrides', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2' },
    ]);

    const panel = await openOverridesPanel('acc-1');
    const select = panel.querySelector('select.override-payment-select') as HTMLSelectElement | null;
    expect(select).not.toBeNull();
    const values = Array.from(select!.options).map(o => o.value);
    expect(values).toEqual(['', 'no-upfront', 'partial-upfront', 'all-upfront']);
    expect(select!.value).toBe(''); // Inherit by default when no payment set
    expect(select!.options[0]!.disabled).toBe(false);
  });

  test('changing payment from Inherit calls saveAccountServiceOverride with only {payment}', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'rds', term: 1 },
    ]);
    (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue({
      id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'rds', term: 1, payment: 'all-upfront',
    });

    const panel = await openOverridesPanel('acc-1');
    const select = panel.querySelector('select.override-payment-select') as HTMLSelectElement;
    select.value = 'all-upfront';
    select.dispatchEvent(new Event('change'));
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).toHaveBeenCalledTimes(1);
    expect(api.saveAccountServiceOverride).toHaveBeenCalledWith(
      'acc-1', 'aws', 'rds', { payment: 'all-upfront' },
    );
  });

  // Issue #196 — once the read path consults per-account overrides, the
  // recs list must refresh after a mutation or the user keeps seeing
  // stale data until the next page navigation.
  test('inline payment change triggers a recommendations refresh (issue #196)', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'rds', term: 1 },
    ]);
    (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue({
      id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'rds', term: 1, payment: 'all-upfront',
    });

    const panel = await openOverridesPanel('acc-1');
    const select = panel.querySelector('select.override-payment-select') as HTMLSelectElement;
    select.value = 'all-upfront';
    select.dispatchEvent(new Event('change'));
    await new Promise(r => setTimeout(r, 0));

    expect(mockLoadRecommendations).toHaveBeenCalledTimes(1);
  });

  // The refresh is best-effort: a failure to refresh must not surface to
  // the user as an error toast, and must not block the override mutation
  // from completing successfully.
  test('refresh failure after override save is swallowed (issue #196)', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'rds', term: 1 },
    ]);
    (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue({
      id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'rds', term: 1, payment: 'all-upfront',
    });
    mockLoadRecommendations.mockRejectedValueOnce(new Error('network blip'));
    const consoleWarnSpy = jest.spyOn(console, 'warn').mockImplementation(() => {});

    try {
      const panel = await openOverridesPanel('acc-1');
      const select = panel.querySelector('select.override-payment-select') as HTMLSelectElement;
      select.value = 'all-upfront';
      select.dispatchEvent(new Event('change'));
      await new Promise(r => setTimeout(r, 0));

      expect(api.saveAccountServiceOverride).toHaveBeenCalledTimes(1);
      expect(consoleWarnSpy).toHaveBeenCalled();
      // No error toast should have been shown for the refresh failure: the
      // success toast from the save path is what the user sees.
      const toastCalls = mockShowToast.mock.calls.map(c => c[0]);
      const errorToasts = toastCalls.filter(t => (t as { kind?: string }).kind === 'error');
      expect(errorToasts).toHaveLength(0);
    } finally {
      consoleWarnSpy.mockRestore();
    }
  });

  test('Inherit is disabled when override already has a payment set (no clear-field channel)', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', payment: 'partial-upfront' },
    ]);

    const panel = await openOverridesPanel('acc-1');
    const select = panel.querySelector('select.override-payment-select') as HTMLSelectElement;
    expect(select.value).toBe('partial-upfront');
    expect(select.options[0]!.disabled).toBe(true);
    // The selector still does NOT call save on initial render (no change yet).
    expect(api.saveAccountServiceOverride).not.toHaveBeenCalled();
  });

  test('Azure rows render an editable payment select with provider-appropriate options (issue #109)', async () => {
    // Issue #109: Azure override rows now get inline payment editing.
    // Azure uses upfront/monthly (not the AWS no-upfront/partial/all-upfront set).
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'azure', service: 'compute', term: 1, payment: 'upfront' },
    ]);

    const panel = await openOverridesPanel('acc-1');
    const sel = panel.querySelector('select.override-payment-select') as HTMLSelectElement | null;
    expect(sel).not.toBeNull();
    const options = Array.from(sel!.options).map(o => o.value);
    // Azure has upfront + monthly (not the AWS no-upfront/partial/all-upfront set).
    expect(options).toContain('upfront');
    expect(options).toContain('monthly');
    expect(options).not.toContain('no-upfront');
    expect(options).not.toContain('all-upfront');
  });
});
