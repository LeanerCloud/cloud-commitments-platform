/**
 * Settings account tests: account deletion confirms and the override modal Inherit labels. Split from
 * settings-accounts.test.ts to stay under the 500-line ceiling; the module
 * mocks and DOM helpers live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  mockShowToast,
  mockConfirmDialog,
  buildAccountsDOM,
} from './settings-accounts-harness';
import {
  loadAccountsForProvider,
  openOverrideModal,
  setGlobalDefaultsForTest,
} from '../settings';

import * as api from '../api';

// ---------------------------------------------------------------------------
// Issue #734 — the delete confirms name the account and list the cascade
// ---------------------------------------------------------------------------

describe('deleteAccount — named target and listed cascade (issue #734)', () => {
  beforeEach(() => {
    buildAccountsDOM();
    jest.clearAllMocks();
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-1', name: 'Prod', external_id: '111111111111', enabled: true, provider: 'aws' },
    ]);
  });

  function pendingError(ids: string[]): Error & { status: number; details: Record<string, unknown> } {
    const err = new Error('cannot delete account') as Error & { status: number; details: Record<string, unknown> };
    err.status = 409;
    err.details = { pending_count: ids.length, pending_execution_ids: ids, reason: 'pending_executions' };
    return err;
  }

  async function clickDelete(): Promise<void> {
    const buttons = Array.from(document.getElementById('aws-accounts-list')!.querySelectorAll('button'));
    buttons.find(b => b.textContent === 'Delete')!.click();
    for (let i = 0; i < 10; i++) await new Promise(r => setTimeout(r, 0));
  }

  function confirmBody(call: number): HTMLElement {
    return (mockConfirmDialog.mock.calls[call]![0] as { body: HTMLElement }).body;
  }

  test('first confirm names the account by name and external ID', async () => {
    mockConfirmDialog.mockResolvedValueOnce(false);
    await loadAccountsForProvider('aws');
    await clickDelete();

    const opts = mockConfirmDialog.mock.calls[0]![0] as { title: string; body: string };
    expect(opts.title).toBe('Delete account "Prod" (111111111111)?');
    expect(opts.body).toContain('"Prod" (111111111111)');
    expect(api.deleteAccount).not.toHaveBeenCalled();
  });

  test('a plain delete shows a success toast naming the account', async () => {
    mockConfirmDialog.mockResolvedValueOnce(true);
    (api.deleteAccount as jest.Mock).mockResolvedValueOnce(undefined);
    await loadAccountsForProvider('aws');
    await clickDelete();

    expect(mockShowToast).toHaveBeenCalledWith({ message: 'Deleted account "Prod" (111111111111).', kind: 'success' });
  });

  test('cascade confirm lists each pending purchase with date and upfront cost, plus the total', async () => {
    mockConfirmDialog.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    (api.deleteAccount as jest.Mock).mockRejectedValueOnce(pendingError(['exec-1', 'exec-2']));
    (api.getPurchaseDetails as jest.Mock).mockImplementation((id: string) => Promise.resolve(
      id === 'exec-1'
        ? { execution_id: id, status: 'pending', plan_name: 'Weekly EC2', step_number: 2, scheduled_date: '2026-11-02', total_upfront_cost: 1200, estimated_savings: 0, recommendations: [{}, {}] }
        : { execution_id: id, status: 'notified', scheduled_date: '2026-11-09', total_upfront_cost: 300, estimated_savings: 0, recommendations: [{}] },
    ));
    await loadAccountsForProvider('aws');
    await clickDelete();

    expect(api.getPurchaseDetails).toHaveBeenCalledTimes(2);
    const items = Array.from(confirmBody(1).querySelectorAll('li')).map(li => li.textContent);
    expect(items).toEqual([
      'Weekly EC2, step 2 · 2 commitments · scheduled Nov 2, 2026 · $1,200 upfront',
      'Manual purchase · 1 commitment · scheduled Nov 9, 2026 · $300 upfront',
    ]);
    expect(confirmBody(1).textContent).toContain('Account "Prod" (111111111111) has 2 pending purchases');
    expect(confirmBody(1).querySelector('.account-delete-pending-total')?.textContent)
      .toBe('Total upfront cost cancelled: $1,500');
  });

  test('a failed purchase lookup still lists the ID and drops the total', async () => {
    mockConfirmDialog.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    (api.deleteAccount as jest.Mock).mockRejectedValueOnce(pendingError(['exec-1']));
    (api.getPurchaseDetails as jest.Mock).mockRejectedValueOnce(new Error('forbidden'));
    await loadAccountsForProvider('aws');
    await clickDelete();

    expect(confirmBody(1).querySelector('li')?.textContent).toBe('Purchase exec-1 (details unavailable)');
    expect(confirmBody(1).querySelector('.account-delete-pending-total')).toBeNull();
  });

  test('more than 10 pending purchases: looks up 10 and summarizes the rest', async () => {
    const ids = Array.from({ length: 13 }, (_, i) => `exec-${i}`);
    mockConfirmDialog.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    (api.deleteAccount as jest.Mock).mockRejectedValueOnce(pendingError(ids));
    (api.getPurchaseDetails as jest.Mock).mockResolvedValue({
      execution_id: 'x', status: 'pending', scheduled_date: '2026-11-02', total_upfront_cost: 10, estimated_savings: 0, recommendations: [],
    });
    await loadAccountsForProvider('aws');
    await clickDelete();

    expect(api.getPurchaseDetails).toHaveBeenCalledTimes(10);
    const items = confirmBody(1).querySelectorAll('li');
    expect(items).toHaveLength(11);
    expect(items[10]!.textContent).toBe('and 3 more (see the History page)');
    expect(confirmBody(1).querySelector('.account-delete-pending-total')).toBeNull();
    expect((mockConfirmDialog.mock.calls[1]![0] as { confirmLabel: string }).confirmLabel).toBe('Cancel All 13 Pending + Delete');
  });
});

// ---------------------------------------------------------------------------
// Issue #112 — "Inherit (currently: X)" labels in the override modal
// ---------------------------------------------------------------------------

describe('Override modal Inherit labels (issue #112)', () => {
  /** Minimal DOM for openOverrideModal: just the fields the function touches. */
  function buildOverrideModalDOM(): void {
    document.body.replaceChildren();

    const modal = document.createElement('div');
    modal.id = 'override-modal';
    modal.className = 'modal hidden';

    const form = document.createElement('form');
    form.id = 'override-form';

    const acctIdInput = document.createElement('input');
    acctIdInput.type = 'hidden';
    acctIdInput.id = 'override-account-id';
    form.appendChild(acctIdInput);

    const provInput = document.createElement('input');
    provInput.type = 'hidden';
    provInput.id = 'override-provider';
    form.appendChild(provInput);

    const svcSel = document.createElement('select');
    svcSel.id = 'override-service';
    form.appendChild(svcSel);

    const termSel = document.createElement('select');
    termSel.id = 'override-term';
    const termInherit = document.createElement('option');
    termInherit.value = '';
    termInherit.textContent = 'Inherit (use global default)';
    termSel.appendChild(termInherit);
    for (const v of ['1', '3']) {
      const o = document.createElement('option');
      o.value = v;
      termSel.appendChild(o);
    }
    form.appendChild(termSel);

    const paySel = document.createElement('select');
    paySel.id = 'override-payment';
    form.appendChild(paySel);

    const covInput = document.createElement('input');
    covInput.type = 'number';
    covInput.id = 'override-coverage';
    covInput.placeholder = 'Inherit (use global default)';
    form.appendChild(covInput);

    const errEl = document.createElement('p');
    errEl.id = 'override-form-error';
    form.appendChild(errEl);

    const submitBtn = document.createElement('button');
    submitBtn.type = 'submit';
    form.appendChild(submitBtn);

    modal.appendChild(form);
    document.body.appendChild(modal);
  }

  afterEach(() => {
    // Reset to default so other tests are not affected.
    setGlobalDefaultsForTest({ term: 3, payment: 'all-upfront', coverage: 80 });
  });

  test('term and payment inherit options reflect the current global defaults (3yr / all-upfront)', () => {
    buildOverrideModalDOM();
    setGlobalDefaultsForTest({ term: 3, payment: 'all-upfront', coverage: 80 });

    const panel = document.createElement('div');
    openOverrideModal('acc-1', 'aws', [], panel);

    const termInherit = document.querySelector<HTMLOptionElement>('#override-term option[value=""]');
    expect(termInherit?.textContent).toBe('Inherit (currently: 3 Years)');

    const payInherit = document.querySelector<HTMLOptionElement>('#override-payment option[value=""]');
    expect(payInherit?.textContent).toBe('Inherit (currently: All Upfront)');

    const covInput = document.getElementById('override-coverage') as HTMLInputElement;
    expect(covInput.placeholder).toBe('Inherit (currently: 80%)');
  });

  test('before the config loads, the payment inherit label shows No Upfront (issue #1758)', () => {
    buildOverrideModalDOM();
    jest.isolateModules(() => {
      // eslint-disable-next-line @typescript-eslint/no-require-imports
      const fresh = require('../settings') as typeof import('../settings');
      fresh.openOverrideModal('acc-1', 'aws', [], document.createElement('div'));
    });

    const payInherit = document.querySelector<HTMLOptionElement>('#override-payment option[value=""]');
    expect(payInherit?.textContent).toBe('Inherit (currently: No Upfront)');
  });

  test('inherit labels update when globals differ from defaults (1yr / no-upfront / 70%)', () => {
    buildOverrideModalDOM();
    setGlobalDefaultsForTest({ term: 1, payment: 'no-upfront', coverage: 70 });

    const panel = document.createElement('div');
    openOverrideModal('acc-1', 'aws', [], panel);

    const termInherit = document.querySelector<HTMLOptionElement>('#override-term option[value=""]');
    expect(termInherit?.textContent).toBe('Inherit (currently: 1 Year)');

    const payInherit = document.querySelector<HTMLOptionElement>('#override-payment option[value=""]');
    expect(payInherit?.textContent).toBe('Inherit (currently: No Upfront)');

    const covInput = document.getElementById('override-coverage') as HTMLInputElement;
    expect(covInput.placeholder).toBe('Inherit (currently: 70%)');
  });
});
