/**
 * Settings account tests: the create-override modal and the per-account overrides modal. Split from
 * settings-accounts.test.ts to stay under the 500-line ceiling; the module
 * mocks and DOM helpers live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  buildAccountsDOM,
} from './settings-accounts-harness';
import {
  loadAccountsForProvider,
  setupSettingsHandlers,
} from '../settings';

import * as api from '../api';

// ---------------------------------------------------------------------------
// Override creation modal — issue #104
// ---------------------------------------------------------------------------

describe('Create-override modal', () => {
  /**
   * Click the "Service overrides" expander button on an AWS account row to
   * trigger loadOverridesPanel, then return the panel + override-modal DOM
   * for further interaction.
   */
  async function expandOverridesPanel(accountId = 'acc-1'): Promise<{ panel: HTMLElement; modal: HTMLElement }> {
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
    // Issue #122: panel is now inside the per-account overrides modal.
    const panel = document.getElementById('account-overrides-modal-body') as HTMLElement | null;
    expect(panel).not.toBeNull();
    const modal = document.getElementById('override-modal') as HTMLElement | null;
    expect(modal).not.toBeNull();
    return { panel: panel!, modal: modal! };
  }

  beforeEach(() => {
    buildAccountsDOM();
    setupSettingsHandlers();
    jest.clearAllMocks();
  });

  test('empty-state auto-opens the override modal', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);

    const { modal } = await expandOverridesPanel('acc-1');

    expect(modal.classList.contains('hidden')).toBe(false);
    // Service dropdown should be populated with all 9 AWS service options
    // (5 RIs + 4 per-plan-type SP slugs after the issue #22 follow-up).
    const svcSelect = document.getElementById('override-service') as HTMLSelectElement;
    const values = Array.from(svcSelect.options).map(o => o.value);
    expect(values).toEqual([
      'ec2', 'rds', 'elasticache', 'opensearch', 'redshift',
      'savings-plans-compute', 'savings-plans-ec2instance',
      'savings-plans-sagemaker', 'savings-plans-database',
    ]);
  });

  test('populated panel shows an Add override button that opens the modal', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', term: 1 },
    ]);

    const { panel, modal } = await expandOverridesPanel('acc-1');
    // The auto-open path must NOT fire when overrides already exist.
    expect(modal.classList.contains('hidden')).toBe(true);

    const addBtn = Array.from(panel.querySelectorAll('button')).find(b => b.textContent === 'Add override');
    expect(addBtn).toBeDefined();
    addBtn!.click();
    expect(modal.classList.contains('hidden')).toBe(false);

    // ec2 already has an override; the dropdown excludes it.
    const svcSelect = document.getElementById('override-service') as HTMLSelectElement;
    const values = Array.from(svcSelect.options).map(o => o.value);
    expect(values).not.toContain('ec2');
    expect(values).toContain('rds');
  });

  test('submit sends only the fields the user filled in (sparse PUT)', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);
    (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue({
      id: 'o-new', account_id: 'acc-1', provider: 'aws', service: 'rds', payment: 'no-upfront',
    });

    await expandOverridesPanel('acc-1');

    (document.getElementById('override-service') as HTMLSelectElement).value = 'rds';
    (document.getElementById('override-payment') as HTMLSelectElement).value = 'no-upfront';
    // term + coverage left blank → omitted from the request

    const form = document.getElementById('override-form') as HTMLFormElement;
    form.dispatchEvent(new Event('submit', { cancelable: true }));
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).toHaveBeenCalledTimes(1);
    expect(api.saveAccountServiceOverride).toHaveBeenCalledWith(
      'acc-1', 'aws', 'rds', { payment: 'no-upfront' },
    );
  });

  test('coercion: term parses to int, coverage parses to number', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);
    (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue({});

    await expandOverridesPanel('acc-1');
    (document.getElementById('override-service') as HTMLSelectElement).value = 'ec2';
    (document.getElementById('override-term') as HTMLSelectElement).value = '3';
    (document.getElementById('override-payment') as HTMLSelectElement).value = 'all-upfront';
    (document.getElementById('override-coverage') as HTMLInputElement).value = '85';

    (document.getElementById('override-form') as HTMLFormElement).dispatchEvent(
      new Event('submit', { cancelable: true }),
    );
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).toHaveBeenCalledWith(
      'acc-1', 'aws', 'ec2',
      { term: 3, payment: 'all-upfront', coverage: 85 },
    );
  });

  test('blocks submit when no field is set (would be a no-op override)', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);

    await expandOverridesPanel('acc-1');
    (document.getElementById('override-service') as HTMLSelectElement).value = 'ec2';
    // All three override fields left blank.

    (document.getElementById('override-form') as HTMLFormElement).dispatchEvent(
      new Event('submit', { cancelable: true }),
    );
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).not.toHaveBeenCalled();
    const errEl = document.getElementById('override-form-error');
    expect(errEl?.textContent).toContain('Set at least one');
  });

  test('blocks submit when coverage is out of range', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);

    await expandOverridesPanel('acc-1');
    (document.getElementById('override-service') as HTMLSelectElement).value = 'ec2';
    (document.getElementById('override-coverage') as HTMLInputElement).value = '150';

    (document.getElementById('override-form') as HTMLFormElement).dispatchEvent(
      new Event('submit', { cancelable: true }),
    );
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).not.toHaveBeenCalled();
    const errEl = document.getElementById('override-form-error');
    expect(errEl?.textContent).toContain('Dashboard coverage must be a whole number between 0 and 100');
  });

  test('blocks submit when coverage is fractional (#136 A11-017)', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);

    await expandOverridesPanel('acc-1');
    (document.getElementById('override-service') as HTMLSelectElement).value = 'ec2';
    (document.getElementById('override-coverage') as HTMLInputElement).value = '50.5';

    (document.getElementById('override-form') as HTMLFormElement).dispatchEvent(
      new Event('submit', { cancelable: true }),
    );
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).not.toHaveBeenCalled();
    expect(document.getElementById('override-form-error')?.textContent).toContain('whole number');
  });

  test('save success closes the modal and reloads the panel', async () => {
    (api.listAccountServiceOverrides as jest.Mock)
      .mockResolvedValueOnce([])  // initial empty
      .mockResolvedValueOnce([    // after save reload
        { id: 'o-new', account_id: 'acc-1', provider: 'aws', service: 'rds', payment: 'all-upfront' },
      ]);
    (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue({});

    const { modal } = await expandOverridesPanel('acc-1');
    (document.getElementById('override-service') as HTMLSelectElement).value = 'rds';
    (document.getElementById('override-payment') as HTMLSelectElement).value = 'all-upfront';

    (document.getElementById('override-form') as HTMLFormElement).dispatchEvent(
      new Event('submit', { cancelable: true }),
    );
    await new Promise(r => setTimeout(r, 0));

    expect(modal.classList.contains('hidden')).toBe(true);
    expect(api.listAccountServiceOverrides).toHaveBeenCalledTimes(2);
  });

  test('cancel button closes the modal without calling the API', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);

    const { modal } = await expandOverridesPanel('acc-1');
    expect(modal.classList.contains('hidden')).toBe(false);

    (document.getElementById('close-override-modal-btn') as HTMLButtonElement).click();
    expect(modal.classList.contains('hidden')).toBe(true);
    expect(api.saveAccountServiceOverride).not.toHaveBeenCalled();
  });

  test('Azure account: empty state auto-opens the create modal with Azure service list (issue #109)', async () => {
    // Issue #109: Azure accounts now get the same "Add override" flow as AWS.
    // The empty-state auto-opens the create modal and the service dropdown
    // is populated with Azure services (vm, sql, cosmosdb, redis, search).
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'az-1', name: 'AzureProd', provider: 'azure', external_id: 'sub-x', enabled: true },
    ]);
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);

    await loadAccountsForProvider('azure');
    const overridesBtn = document.querySelector(
      `button[aria-label="Service overrides for AzureProd (sub-x)"]`,
    ) as HTMLButtonElement;
    overridesBtn.click();
    await new Promise(r => setTimeout(r, 0));

    const panel = document.getElementById('account-overrides-modal-body') as HTMLElement;
    const modal = document.getElementById('override-modal') as HTMLElement;

    // The inner create modal auto-opens (same as AWS empty state).
    expect(modal.classList.contains('hidden')).toBe(false);

    // Add override button is present.
    const addBtn = Array.from(panel.querySelectorAll('button')).find(b => b.textContent === 'Add override');
    expect(addBtn).toBeDefined();

    // Service dropdown lists Azure services, not AWS services.
    const svcSel = document.getElementById('override-service') as HTMLSelectElement;
    const svcValues = Array.from(svcSel.options).map(o => o.value);
    expect(svcValues).toContain('compute');
    expect(svcValues).toContain('relational-db');
    expect(svcValues).toContain('nosql');
    expect(svcValues).not.toContain('ec2');
    expect(svcValues).not.toContain('rds');
  });

  test('all services already overridden disables submit', async () => {
    // Mirrors AWS_OVERRIDE_SERVICES post-issue-#22-follow-up: 5 RI services
    // plus the four per-plan-type SP slugs.
    const all = [
      'ec2', 'rds', 'elasticache', 'opensearch', 'redshift',
      'savings-plans-compute', 'savings-plans-ec2instance',
      'savings-plans-sagemaker', 'savings-plans-database',
    ];
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue(
      all.map((service, i) => ({
        id: `o-${i}`, account_id: 'acc-1', provider: 'aws', service, term: 1,
      })),
    );

    const { panel, modal } = await expandOverridesPanel('acc-1');
    const addBtn = Array.from(panel.querySelectorAll('button')).find(b => b.textContent === 'Add override');
    addBtn!.click();
    expect(modal.classList.contains('hidden')).toBe(false);

    const submitBtn = modal.querySelector('button[type="submit"]') as HTMLButtonElement;
    expect(submitBtn.disabled).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Per-account overrides modal — issue #122
// ---------------------------------------------------------------------------

describe('Account overrides modal', () => {
  beforeEach(() => {
    buildAccountsDOM();
    setupSettingsHandlers();
    jest.clearAllMocks();
  });

  test('Overrides button opens an account-scoped modal whose title binds to the account', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-1', name: 'AWS Prod', provider: 'aws', external_id: '123456789012', enabled: true },
    ]);
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', payment: 'all-upfront' },
    ]);

    await loadAccountsForProvider('aws');
    const btn = document.querySelector(
      `button[aria-label="Service overrides for AWS Prod (123456789012)"]`,
    ) as HTMLButtonElement;
    btn.click();
    await new Promise(r => setTimeout(r, 0));

    const modal = document.getElementById('account-overrides-modal') as HTMLElement;
    expect(modal.classList.contains('hidden')).toBe(false);
    const title = document.getElementById('account-overrides-modal-title') as HTMLElement;
    expect(title.textContent).toBe('Service overrides for AWS Prod (123456789012)');
  });

  test('switching accounts swaps the modal title; only one account context is active at a time', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-a', name: 'AWS Prod',  provider: 'aws', external_id: '123456789012', enabled: true },
      { id: 'acc-b', name: 'CUDly host', provider: 'aws', external_id: '444444444444', enabled: true },
    ]);
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);

    await loadAccountsForProvider('aws');
    const titleEl = document.getElementById('account-overrides-modal-title') as HTMLElement;

    // Click Overrides on Account A.
    (document.querySelector(
      `button[aria-label="Service overrides for AWS Prod (123456789012)"]`,
    ) as HTMLButtonElement).click();
    await new Promise(r => setTimeout(r, 0));
    expect(titleEl.textContent).toContain('AWS Prod');

    // Click Overrides on Account B without closing the modal first.
    (document.querySelector(
      `button[aria-label="Service overrides for CUDly host (444444444444)"]`,
    ) as HTMLButtonElement).click();
    await new Promise(r => setTimeout(r, 0));

    // Title now reflects Account B; only ONE modal exists in the DOM.
    expect(titleEl.textContent).toContain('CUDly host');
    expect(titleEl.textContent).not.toContain('AWS Prod');
    expect(document.querySelectorAll('#account-overrides-modal').length).toBe(1);
  });

  test('Close button hides the modal and clears the body to avoid stale flash on next open', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-1', name: 'Prod', provider: 'aws', external_id: '111', enabled: true },
    ]);
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', payment: 'all-upfront' },
    ]);

    await loadAccountsForProvider('aws');
    (document.querySelector(
      `button[aria-label="Service overrides for Prod (111)"]`,
    ) as HTMLButtonElement).click();
    await new Promise(r => setTimeout(r, 0));

    const modal = document.getElementById('account-overrides-modal') as HTMLElement;
    const body = document.getElementById('account-overrides-modal-body') as HTMLElement;
    expect(modal.classList.contains('hidden')).toBe(false);
    expect(body.querySelector('table.overrides-table')).not.toBeNull();

    (document.getElementById('close-account-overrides-modal-btn') as HTMLButtonElement).click();
    expect(modal.classList.contains('hidden')).toBe(true);
    // Body cleared so the next open doesn't flash stale rows for ~1ms.
    expect(body.children.length).toBe(0);
  });

  test('focus moves into the modal, Escape closes it, clears the body and restores focus (#731)', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-1', name: 'Prod', provider: 'aws', external_id: '111', enabled: true },
    ]);
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', payment: 'all-upfront' },
    ]);

    await loadAccountsForProvider('aws');
    const trigger = document.querySelector(
      `button[aria-label="Service overrides for Prod (111)"]`,
    ) as HTMLButtonElement;
    trigger.focus();
    trigger.click();
    await new Promise(r => setTimeout(r, 0));

    const modal = document.getElementById('account-overrides-modal') as HTMLElement;
    const body = document.getElementById('account-overrides-modal-body') as HTMLElement;
    expect(modal.contains(document.activeElement)).toBe(true);

    modal.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(modal.classList.contains('hidden')).toBe(true);
    expect(body.children.length).toBe(0);
    expect(document.activeElement).toBe(trigger);
  });

  test('Escape in the auto-opened create modal closes only that modal and returns focus to the outer one (#731)', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-1', name: 'Prod', provider: 'aws', external_id: '111', enabled: true },
    ]);
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);

    await loadAccountsForProvider('aws');
    (document.querySelector(
      `button[aria-label="Service overrides for Prod (111)"]`,
    ) as HTMLButtonElement).click();
    await new Promise(r => setTimeout(r, 0));

    const outer = document.getElementById('account-overrides-modal') as HTMLElement;
    const inner = document.getElementById('override-modal') as HTMLElement;
    expect(inner.classList.contains('hidden')).toBe(false);
    expect(inner.contains(document.activeElement)).toBe(true);

    inner.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(inner.classList.contains('hidden')).toBe(true);
    expect(outer.classList.contains('hidden')).toBe(false);
    expect(outer.contains(document.activeElement)).toBe(true);

    // The Escape path ran the same cleanup as the Close button: a stray
    // submit no longer has an account context to save against.
    (document.getElementById('override-form') as HTMLFormElement)
      .dispatchEvent(new Event('submit', { cancelable: true }));
    await new Promise(r => setTimeout(r, 0));
    expect(api.saveAccountServiceOverride).not.toHaveBeenCalled();
  });

  test('no inline panel renders below the accounts table (issue #122 regression guard)', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-1', name: 'Prod', provider: 'aws', external_id: '111', enabled: true },
    ]);

    await loadAccountsForProvider('aws');

    // The inline expandable panel was the cause of the panel-stacking bug.
    // Verify it is gone — the only override container is the modal body.
    expect(document.querySelector('.account-overrides-panel')).toBeNull();
  });
});
