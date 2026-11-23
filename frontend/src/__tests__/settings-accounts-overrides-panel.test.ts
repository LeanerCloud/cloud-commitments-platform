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

// ---------------------------------------------------------------------------
// Inline edit on existing override rows: Term, Coverage, Enabled (issue #110)
// ---------------------------------------------------------------------------

describe('Overrides panel: inline Term/Coverage/Enabled (issue #110)', () => {
  /**
   * Render the AWS accounts list and open the per-account overrides modal,
   * mirroring openOverridesPanel from the Payment-selector describe block.
   * The body element is what loadOverridesPanel renders into.
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
    await new Promise(r => setTimeout(r, 0));
    const panel = document.getElementById('account-overrides-modal-body') as HTMLElement | null;
    expect(panel).not.toBeNull();
    return panel!;
  }

  beforeEach(() => {
    buildAccountsDOM();
    jest.clearAllMocks();
  });

  // ----- Term cell -----

  test('Term: renders <select> with Inherit + 1yr/3yr options for an AWS EC2 row', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2' },
    ]);

    const panel = await openOverridesPanel('acc-1');
    const sel = panel.querySelector('select.override-term-select') as HTMLSelectElement | null;
    expect(sel).not.toBeNull();
    const values = Array.from(sel!.options).map(o => o.value);
    expect(values).toEqual(['', '1', '3']);
    expect(sel!.value).toBe(''); // Inherit by default when no term set
    expect(sel!.options[0]!.disabled).toBe(false);
  });

  test('Term: change from Inherit calls saveAccountServiceOverride with only {term}', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2' },
    ]);
    (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue({
      id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', term: 3,
    });

    const panel = await openOverridesPanel('acc-1');
    const sel = panel.querySelector('select.override-term-select') as HTMLSelectElement;
    sel.value = '3';
    sel.dispatchEvent(new Event('change'));
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).toHaveBeenCalledTimes(1);
    expect(api.saveAccountServiceOverride).toHaveBeenCalledWith(
      'acc-1', 'aws', 'ec2', { term: 3 },
    );
  });

  test('Term: RDS row with payment=no-upfront hides term=3 (commitmentOptions parity)', async () => {
    // RDS has the only AWS hard rule: no 3yr/no-upfront combination. The
    // Term dropdown for an RDS row currently set to no-upfront must omit
    // the 3yr option, same way the inline Payment selector omits no-upfront
    // for RDS term=3 rows.
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'rds', payment: 'no-upfront' },
    ]);

    const panel = await openOverridesPanel('acc-1');
    const sel = panel.querySelector('select.override-term-select') as HTMLSelectElement;
    const values = Array.from(sel.options).map(o => o.value);
    expect(values).toEqual(['', '1']);
    expect(values).not.toContain('3');
  });

  test('Term: Inherit is disabled when term already set (no clear-field channel)', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', term: 1 },
    ]);

    const panel = await openOverridesPanel('acc-1');
    const sel = panel.querySelector('select.override-term-select') as HTMLSelectElement;
    expect(sel.value).toBe('1');
    expect(sel.options[0]!.disabled).toBe(true);
    expect(api.saveAccountServiceOverride).not.toHaveBeenCalled();
  });

  test('Term: save failure reverts the cell and shows an error toast', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2' },
    ]);
    (api.saveAccountServiceOverride as jest.Mock).mockRejectedValueOnce(new Error('boom'));

    const panel = await openOverridesPanel('acc-1');
    const sel = panel.querySelector('select.override-term-select') as HTMLSelectElement;
    sel.value = '3';
    sel.dispatchEvent(new Event('change'));
    await new Promise(r => setTimeout(r, 0));
    await new Promise(r => setTimeout(r, 0));

    expect(sel.value).toBe(''); // reverted to previous
    const toastCalls = mockShowToast.mock.calls.map(c => c[0]) as Array<{ kind?: string; message?: string }>;
    expect(toastCalls.some(t => t.kind === 'error' && t.message?.includes('term'))).toBe(true);
  });

  // ----- Coverage cell -----

  test('Coverage: renders numeric <input> with value=50 for preset, placeholder when absent', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', coverage: 50 },
      { id: 'o2', account_id: 'acc-1', provider: 'aws', service: 'rds' },
    ]);

    const panel = await openOverridesPanel('acc-1');
    const inputs = panel.querySelectorAll<HTMLInputElement>('input.override-coverage-input');
    expect(inputs.length).toBe(2);
    expect(inputs[0]!.type).toBe('number');
    expect(inputs[0]!.value).toBe('50');
    expect(inputs[1]!.value).toBe('');
    expect(inputs[1]!.placeholder).toBe('Inherit');
  });

  test('Coverage: change to 75 calls saveAccountServiceOverride with only {coverage: 75}', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', coverage: 50 },
    ]);
    (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue({
      id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', coverage: 75,
    });

    const panel = await openOverridesPanel('acc-1');
    const inp = panel.querySelector('input.override-coverage-input') as HTMLInputElement;
    inp.value = '75';
    inp.dispatchEvent(new Event('change'));
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).toHaveBeenCalledTimes(1);
    expect(api.saveAccountServiceOverride).toHaveBeenCalledWith(
      'acc-1', 'aws', 'ec2', { coverage: 75 },
    );
  });

  test('Coverage: out-of-range value (150) does NOT call save, reverts, and posts error toast', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', coverage: 50 },
    ]);

    const panel = await openOverridesPanel('acc-1');
    const inp = panel.querySelector('input.override-coverage-input') as HTMLInputElement;
    inp.value = '150';
    inp.dispatchEvent(new Event('change'));
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).not.toHaveBeenCalled();
    expect(inp.value).toBe('50');
    const toastCalls = mockShowToast.mock.calls.map(c => c[0]) as Array<{ kind?: string; message?: string }>;
    expect(toastCalls.some(t => t.kind === 'error' && t.message?.includes('between 0 and 100'))).toBe(true);
  });

  test('Coverage: negative value does NOT call save, reverts, and posts error toast', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', coverage: 50 },
    ]);

    const panel = await openOverridesPanel('acc-1');
    const inp = panel.querySelector('input.override-coverage-input') as HTMLInputElement;
    inp.value = '-5';
    inp.dispatchEvent(new Event('change'));
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).not.toHaveBeenCalled();
    expect(inp.value).toBe('50');
  });

  test('Coverage: clearing a preset value reverts (no clear-field channel) and posts info toast', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', coverage: 50 },
    ]);

    const panel = await openOverridesPanel('acc-1');
    const inp = panel.querySelector('input.override-coverage-input') as HTMLInputElement;
    inp.value = '';
    inp.dispatchEvent(new Event('change'));
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).not.toHaveBeenCalled();
    expect(inp.value).toBe('50');
    const toastCalls = mockShowToast.mock.calls.map(c => c[0]) as Array<{ kind?: string; message?: string }>;
    expect(toastCalls.some(t => t.kind === 'info' && t.message?.includes('Delete'))).toBe(true);
  });

  test('Enabled: checkbox is checked by default for legacy rows (enabled === undefined)', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2' },
    ]);

    const panel = await openOverridesPanel('acc-1');
    const cb = panel.querySelector('input.override-enabled-toggle') as HTMLInputElement;
    expect(cb).not.toBeNull();
    expect(cb.type).toBe('checkbox');
    expect(cb.checked).toBe(true);
    const tr = cb.closest('tr')!;
    expect(tr.classList.contains('override-disabled')).toBe(false);
  });

  test('Enabled: rows with enabled=false render unchecked with .override-disabled dim', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', enabled: false },
    ]);

    const panel = await openOverridesPanel('acc-1');
    const cb = panel.querySelector('input.override-enabled-toggle') as HTMLInputElement;
    expect(cb.checked).toBe(false);
    const tr = cb.closest('tr')!;
    expect(tr.classList.contains('override-disabled')).toBe(true);
  });

  test('Enabled: toggle off calls save with {enabled: false} and dims the row', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2' },
    ]);
    (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue({
      id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', enabled: false,
    });

    const panel = await openOverridesPanel('acc-1');
    const cb = panel.querySelector('input.override-enabled-toggle') as HTMLInputElement;
    const tr = cb.closest('tr')!;
    cb.checked = false;
    cb.dispatchEvent(new Event('change'));
    // After the optimistic visual update, the row should already be dimmed
    // even before the network roundtrip completes.
    expect(tr.classList.contains('override-disabled')).toBe(true);
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).toHaveBeenCalledTimes(1);
    expect(api.saveAccountServiceOverride).toHaveBeenCalledWith(
      'acc-1', 'aws', 'ec2', { enabled: false },
    );
  });

  test('Enabled: toggle on calls save with {enabled: true} and removes dim', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', enabled: false },
    ]);
    (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue({
      id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', enabled: true,
    });

    const panel = await openOverridesPanel('acc-1');
    const cb = panel.querySelector('input.override-enabled-toggle') as HTMLInputElement;
    const tr = cb.closest('tr')!;
    expect(tr.classList.contains('override-disabled')).toBe(true);
    cb.checked = true;
    cb.dispatchEvent(new Event('change'));
    expect(tr.classList.contains('override-disabled')).toBe(false);
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).toHaveBeenCalledTimes(1);
    expect(api.saveAccountServiceOverride).toHaveBeenCalledWith(
      'acc-1', 'aws', 'ec2', { enabled: true },
    );
  });

  // ----- Non-AWS rows -----

  test('Azure rows: term/coverage/enabled are all editable (issue #109)', async () => {
    // Issue #109: Azure override rows now have inline editing on all fields,
    // same as AWS rows after issue #110.
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'azure', service: 'compute', term: 1, coverage: 80 },
    ]);

    const panel = await openOverridesPanel('acc-1');
    // Term select is present and editable.
    const termSel = panel.querySelector('select.override-term-select') as HTMLSelectElement | null;
    expect(termSel).not.toBeNull();
    expect(termSel!.disabled).toBe(false);
    // Coverage input is present.
    const covInput = panel.querySelector('input.override-coverage-input') as HTMLInputElement | null;
    expect(covInput).not.toBeNull();
    // Enabled toggle is present and not disabled.
    const cb = panel.querySelector('input.override-enabled-toggle') as HTMLInputElement;
    expect(cb).not.toBeNull();
    expect(cb.disabled).toBe(false);
  });
});
