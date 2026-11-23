/**
 * Settings account tests: the bulk multi-service override modal. Split from
 * settings-accounts.test.ts to stay under the 500-line ceiling; the module
 * mocks and DOM helpers live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  mockShowToast,
  buildAccountsDOM,
} from './settings-accounts-harness';
import {
  loadAccountsForProvider,
  setupSettingsHandlers,
} from '../settings';

import * as api from '../api';

// ---------------------------------------------------------------------------
// Bulk multi-service override modal — issue #119
// ---------------------------------------------------------------------------

describe('Bulk override modal (issue #119)', () => {
  /**
   * Open the override modal in bulk mode with no pre-existing overrides,
   * toggle the bulk switch, and return the DOM elements needed by tests.
   */
  async function openBulkModal(accountId = 'acc-1'): Promise<{
    modal: HTMLElement;
    bulkToggle: HTMLInputElement;
    bulkList: HTMLElement;
    submitBtn: HTMLButtonElement;
    form: HTMLFormElement;
  }> {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: accountId, name: 'Prod', provider: 'aws', external_id: '111', enabled: true },
    ]);
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);
    await loadAccountsForProvider('aws');

    const overridesBtn = document.querySelector(
      `button[aria-label="Service overrides for Prod (111)"]`,
    ) as HTMLButtonElement;
    expect(overridesBtn).not.toBeNull();
    overridesBtn.click();
    await new Promise(r => setTimeout(r, 0));

    const modal = document.getElementById('override-modal') as HTMLElement;
    const bulkToggle = document.getElementById('override-bulk-toggle') as HTMLInputElement;
    const bulkList = document.getElementById('override-bulk-services-list') as HTMLElement;
    const submitBtn = document.getElementById('override-submit-btn') as HTMLButtonElement;
    const form = document.getElementById('override-form') as HTMLFormElement;

    // Flip to bulk mode.
    bulkToggle.checked = true;
    bulkToggle.dispatchEvent(new Event('change'));

    return { modal, bulkToggle, bulkList, submitBtn, form };
  }

  beforeEach(() => {
    buildAccountsDOM();
    setupSettingsHandlers();
    jest.clearAllMocks();
  });

  test('bulk toggle hides single-service row and shows checkbox list', async () => {
    const { bulkList } = await openBulkModal();

    const singleRow = document.getElementById('override-single-service-row') as HTMLElement;
    const bulkDiv = document.getElementById('override-bulk-services') as HTMLElement;

    expect(singleRow.classList.contains('hidden')).toBe(true);
    expect(bulkDiv.classList.contains('hidden')).toBe(false);

    // All 9 AWS services should be rendered as checkboxes.
    const checkboxes = bulkList.querySelectorAll('input[type="checkbox"]');
    expect(checkboxes).toHaveLength(9);
    const values = Array.from(checkboxes).map(cb => (cb as HTMLInputElement).value);
    expect(values).toContain('ec2');
    expect(values).toContain('rds');
    expect(values).toContain('elasticache');
  });

  test('submit is disabled until at least one checkbox is checked', async () => {
    const { submitBtn, bulkList } = await openBulkModal();

    // No boxes checked yet.
    expect(submitBtn.disabled).toBe(true);

    // Check one box.
    const firstCb = bulkList.querySelector('input[type="checkbox"]') as HTMLInputElement;
    firstCb.checked = true;
    firstCb.dispatchEvent(new Event('change'));
    expect(submitBtn.disabled).toBe(false);
  });

  test('all 3 saves succeed: aggregated success toast, modal closes, panel reloads', async () => {
    (api.listAccountServiceOverrides as jest.Mock)
      .mockResolvedValueOnce([])  // initial: empty -> auto-opens modal
      .mockResolvedValueOnce([   // reload after save: returns new rows so modal stays closed
        { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'rds',         term: 1 },
        { id: 'o2', account_id: 'acc-1', provider: 'aws', service: 'elasticache', term: 1 },
        { id: 'o3', account_id: 'acc-1', provider: 'aws', service: 'opensearch',  term: 1 },
      ]);
    (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue({});

    const { form, bulkList } = await openBulkModal();

    // Select rds, elasticache, opensearch.
    const targets = ['rds', 'elasticache', 'opensearch'];
    for (const svc of targets) {
      const cb = bulkList.querySelector(`input[value="${svc}"]`) as HTMLInputElement;
      cb.checked = true;
      cb.dispatchEvent(new Event('change'));
    }
    (document.getElementById('override-payment') as HTMLSelectElement).value = 'all-upfront';

    form.dispatchEvent(new Event('submit', { cancelable: true }));
    // Flush all async chains: allSettled + loadOverridesPanel + recommendations.
    for (let i = 0; i < 10; i++) await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).toHaveBeenCalledTimes(3);
    const toastArg = mockShowToast.mock.calls[0]?.[0] as { message: string; kind: string };
    expect(toastArg.kind).toBe('success');
    expect(toastArg.message).toMatch(/Created 3 overrides/);

    // Modal should be hidden after success.
    const modal = document.getElementById('override-modal') as HTMLElement;
    expect(modal.classList.contains('hidden')).toBe(true);
  });

  test('2 successes / 1 failure: warning toast names the failed service', async () => {
    (api.listAccountServiceOverrides as jest.Mock)
      .mockResolvedValueOnce([])  // initial: empty -> auto-opens modal
      .mockResolvedValueOnce([   // reload after partial save: non-empty keeps modal closed
        { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'rds',        term: 1 },
        { id: 'o2', account_id: 'acc-1', provider: 'aws', service: 'opensearch', term: 1 },
      ]);
    (api.saveAccountServiceOverride as jest.Mock)
      .mockResolvedValueOnce({})  // rds OK
      .mockRejectedValueOnce(new Error('server error'))  // elasticache fails
      .mockResolvedValueOnce({});  // opensearch OK

    const { form, bulkList } = await openBulkModal();

    const targets = ['rds', 'elasticache', 'opensearch'];
    for (const svc of targets) {
      const cb = bulkList.querySelector(`input[value="${svc}"]`) as HTMLInputElement;
      cb.checked = true;
      cb.dispatchEvent(new Event('change'));
    }
    (document.getElementById('override-payment') as HTMLSelectElement).value = 'no-upfront';

    form.dispatchEvent(new Event('submit', { cancelable: true }));
    // Flush all async chains: allSettled + loadOverridesPanel + recommendations.
    for (let i = 0; i < 10; i++) await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).toHaveBeenCalledTimes(3);
    const toastArg = mockShowToast.mock.calls[0]?.[0] as { message: string; kind: string };
    expect(toastArg.kind).toBe('warning');
    expect(toastArg.message).toMatch(/Created 2 overrides/);
    expect(toastArg.message).toContain('elasticache');

    // Modal closes even on partial success.
    const modal = document.getElementById('override-modal') as HTMLElement;
    expect(modal.classList.contains('hidden')).toBe(true);
  });

  test('all saves fail: error shown in form, modal stays open', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);
    (api.saveAccountServiceOverride as jest.Mock).mockRejectedValue(new Error('coverage must be between 0 and 100'));

    const { form, bulkList } = await openBulkModal();

    const targets = ['rds', 'elasticache'];
    for (const svc of targets) {
      const cb = bulkList.querySelector(`input[value="${svc}"]`) as HTMLInputElement;
      cb.checked = true;
      cb.dispatchEvent(new Event('change'));
    }
    (document.getElementById('override-payment') as HTMLSelectElement).value = 'no-upfront';

    form.dispatchEvent(new Event('submit', { cancelable: true }));
    await new Promise(r => setTimeout(r, 0));

    // No toast on total failure.
    expect(mockShowToast).not.toHaveBeenCalled();

    const errEl = document.getElementById('override-form-error') as HTMLElement;
    // Issue #748: the server's reason, not a blanket "Check your connection".
    expect(errEl.textContent).toBe('All 2 saves failed: coverage must be between 0 and 100');

    // Modal stays open so user can retry.
    const modal = document.getElementById('override-modal') as HTMLElement;
    expect(modal.classList.contains('hidden')).toBe(false);
  });
});
