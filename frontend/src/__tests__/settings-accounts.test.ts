/**
 * Settings — account management tests
 * DOM is built with createElement to avoid innerHTML in source.
 */
// The harness registers the module mocks, so it must be imported first.
import {
  mockShowToast,
  mockConfirmDialog,
  buildAccountsDOM,
} from './settings-accounts-harness';
import {
  loadAccountsForProvider,
  openAccountModal,
  openOverrideModal,
  setupSettingsHandlers,
  setGlobalDefaultsForTest,
} from '../settings';

import * as api from '../api';

// ---------------------------------------------------------------------------
// Issue #606 — Cancel-All-Then-Delete UX
// ---------------------------------------------------------------------------

describe('deleteAccount — pending-executions 409 handling (issue #606)', () => {
  beforeEach(() => {
    buildAccountsDOM();
    jest.clearAllMocks();
  });

  // Helper to construct the 409 ApiError shape that client.ts attaches
  // for backend ClientError responses. The status + details are what
  // settings.ts branches on.
  function pendingExecutionsApiError(count: number, ids: string[]): Error & {
    status: number;
    details: Record<string, unknown>;
  } {
    const err = new Error(
      `cannot delete account: ${count} pending purchase(s) must be cancelled first`,
    ) as Error & { status: number; details: Record<string, unknown> };
    err.status = 409;
    err.details = {
      pending_count: count,
      pending_execution_ids: ids,
      reason: 'pending_executions',
    };
    return err;
  }

  // Variant: backend returned a count but no IDs (the server-side list
  // call failed — see TestDeleteAccount_PendingListErr_StillReturns409
  // on the Go side). The UI cannot auto-cancel without the ID list.
  function pendingExecutionsApiErrorNoIDs(count: number): Error & {
    status: number;
    details: Record<string, unknown>;
  } {
    const err = new Error(
      `cannot delete account: ${count} pending purchase(s) must be cancelled first`,
    ) as Error & { status: number; details: Record<string, unknown> };
    err.status = 409;
    err.details = {
      pending_count: count,
      reason: 'pending_executions',
      // No pending_execution_ids on this path.
    };
    return err;
  }

  // Click the Delete button on the only row of the rendered accounts table.
  async function clickDeleteOnFirstAccount(): Promise<void> {
    const container = document.getElementById('aws-accounts-list')!;
    const buttons = Array.from(container.querySelectorAll('button'));
    const deleteBtn = buttons.find(b => b.textContent === 'Delete');
    expect(deleteBtn).toBeDefined();
    deleteBtn!.click();
    // The click handler is async; await microtask flushes so the chain
    // (deleteAccount -> confirmDialog -> api.deleteAccount) starts.
    await Promise.resolve();
  }

  test('409 → second confirm dialog renders with correct count + label', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-1', name: 'Prod', external_id: '111111111111', enabled: true, provider: 'aws' },
    ]);
    // First confirm: "Delete account?" → user confirms.
    // Second confirm: "Pending purchases must be cancelled" → user declines so
    // we can inspect the opts without exercising the cancel + retry path.
    mockConfirmDialog.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    (api.deleteAccount as jest.Mock).mockRejectedValueOnce(
      pendingExecutionsApiError(2, ['exec-1', 'exec-2']),
    );

    await loadAccountsForProvider('aws');
    await clickDeleteOnFirstAccount();
    // Wait for the confirmDialog promise chain to advance through both calls.
    await new Promise(r => setTimeout(r, 0));
    await new Promise(r => setTimeout(r, 0));

    expect(mockConfirmDialog).toHaveBeenCalledTimes(2);
    const secondOpts = mockConfirmDialog.mock.calls[1]![0] as {
      title: string;
      body: HTMLElement;
      confirmLabel: string;
      destructive: boolean;
    };
    expect(secondOpts.title).toBe('Cancel pending purchases and delete "Prod" (111111111111)?');
    expect(secondOpts.body.textContent).toContain('2 pending purchases');
    expect(secondOpts.confirmLabel).toBe('Cancel All 2 Pending + Delete');
    expect(secondOpts.destructive).toBe(true);

    // User declined the second confirm — no cancelPurchase calls fire.
    expect(api.cancelPurchase).not.toHaveBeenCalled();
    // And no retry of deleteAccount.
    expect(api.deleteAccount).toHaveBeenCalledTimes(1);
  });

  test('confirm Cancel-All-Then-Delete posts cancel for each pending id then retries delete', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-1', name: 'Prod', external_id: '111111111111', enabled: true, provider: 'aws' },
    ]);
    mockConfirmDialog.mockResolvedValueOnce(true).mockResolvedValueOnce(true);
    (api.deleteAccount as jest.Mock)
      .mockRejectedValueOnce(pendingExecutionsApiError(3, ['exec-a', 'exec-b', 'exec-c']))
      .mockResolvedValueOnce(undefined);
    (api.cancelPurchase as jest.Mock).mockResolvedValue(undefined);

    await loadAccountsForProvider('aws');
    await clickDeleteOnFirstAccount();
    // Flush several microtasks so all sequential awaits in the handler resolve.
    for (let i = 0; i < 10; i++) {
      await new Promise(r => setTimeout(r, 0));
    }

    // cancelPurchase called once per execution id, in order.
    expect(api.cancelPurchase).toHaveBeenCalledTimes(3);
    expect((api.cancelPurchase as jest.Mock).mock.calls[0][0]).toBe('exec-a');
    expect((api.cancelPurchase as jest.Mock).mock.calls[1][0]).toBe('exec-b');
    expect((api.cancelPurchase as jest.Mock).mock.calls[2][0]).toBe('exec-c');

    // deleteAccount called twice (initial 409 + retry after cancels).
    expect(api.deleteAccount).toHaveBeenCalledTimes(2);

    // Success toast surfaced.
    expect(mockShowToast).toHaveBeenCalled();
    const successCalls = mockShowToast.mock.calls.filter(c => {
      const opts = c[0] as { kind?: string };
      return opts.kind === 'success';
    });
    expect(successCalls.length).toBe(1);
  });

  test('partial cancel failure leaves account in place and surfaces error toast', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-1', name: 'Prod', external_id: '111111111111', enabled: true, provider: 'aws' },
    ]);
    mockConfirmDialog.mockResolvedValueOnce(true).mockResolvedValueOnce(true);
    (api.deleteAccount as jest.Mock).mockRejectedValueOnce(
      pendingExecutionsApiError(2, ['exec-1', 'exec-2']),
    );
    (api.cancelPurchase as jest.Mock)
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error('cancel failed'));

    await loadAccountsForProvider('aws');
    await clickDeleteOnFirstAccount();
    for (let i = 0; i < 10; i++) {
      await new Promise(r => setTimeout(r, 0));
    }

    // Second cancelPurchase rejected — deleteAccount must NOT retry.
    expect(api.deleteAccount).toHaveBeenCalledTimes(1);
    // Error toast surfaced (kind: 'error').
    const errorCalls = mockShowToast.mock.calls.filter(c => {
      const opts = c[0] as { kind?: string; message?: string };
      return opts.kind === 'error';
    });
    expect(errorCalls.length).toBeGreaterThan(0);
    const lastErrorMsg = (errorCalls[errorCalls.length - 1]![0] as { message: string }).message;
    expect(lastErrorMsg).toContain('1 failed');
    expect(lastErrorMsg).toContain('Account "Prod" (111111111111) NOT deleted');
  });

  test('409 with empty pending_execution_ids surfaces History-page fallback', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-1', name: 'Prod', external_id: '111111111111', enabled: true, provider: 'aws' },
    ]);
    // Only the first "Delete account?" confirm fires. There is no second
    // confirm because the UI can't enumerate cancellations without IDs.
    mockConfirmDialog.mockResolvedValueOnce(true);
    (api.deleteAccount as jest.Mock).mockRejectedValueOnce(
      pendingExecutionsApiErrorNoIDs(3),
    );

    await loadAccountsForProvider('aws');
    await clickDeleteOnFirstAccount();
    for (let i = 0; i < 5; i++) {
      await new Promise(r => setTimeout(r, 0));
    }

    // Exactly one confirm dialog — no second one for cancellation.
    expect(mockConfirmDialog).toHaveBeenCalledTimes(1);
    // No cancelPurchase calls (we have no IDs to cancel).
    expect(api.cancelPurchase).not.toHaveBeenCalled();
    // deleteAccount called only once (initial attempt; no retry).
    expect(api.deleteAccount).toHaveBeenCalledTimes(1);
    // Error toast references the History page so the operator knows where
    // to go next.
    const errorCalls = mockShowToast.mock.calls.filter(c => {
      const opts = c[0] as { kind?: string };
      return opts.kind === 'error';
    });
    expect(errorCalls.length).toBeGreaterThan(0);
    const lastErrorMsg = (errorCalls[errorCalls.length - 1]![0] as { message: string }).message;
    expect(lastErrorMsg).toEqual(expect.stringContaining('History'));
  });

  test('non-409 error path keeps original behaviour (no second dialog)', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-1', name: 'Prod', external_id: '111111111111', enabled: true, provider: 'aws' },
    ]);
    mockConfirmDialog.mockResolvedValueOnce(true);
    (api.deleteAccount as jest.Mock).mockRejectedValueOnce(new Error('boom'));

    await loadAccountsForProvider('aws');
    await clickDeleteOnFirstAccount();
    for (let i = 0; i < 5; i++) {
      await new Promise(r => setTimeout(r, 0));
    }

    expect(mockConfirmDialog).toHaveBeenCalledTimes(1);
    expect(api.cancelPurchase).not.toHaveBeenCalled();
    const errorCalls = mockShowToast.mock.calls.filter(c => {
      const opts = c[0] as { kind?: string };
      return opts.kind === 'error';
    });
    expect(errorCalls.length).toBeGreaterThan(0);
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

  test('submit without any box checked shows inline error, never calls API', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);
    (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue({});

    const { form } = await openBulkModal();

    (document.getElementById('override-payment') as HTMLSelectElement).value = 'no-upfront';
    // No checkboxes checked.
    form.dispatchEvent(new Event('submit', { cancelable: true }));
    await new Promise(r => setTimeout(r, 0));

    expect(api.saveAccountServiceOverride).not.toHaveBeenCalled();
    const errEl = document.getElementById('override-form-error') as HTMLElement;
    expect(errEl.textContent).toMatch(/Select at least one/i);
  });

  test('already-overridden services are excluded from the checkbox list', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-1', name: 'Prod', external_id: '111', enabled: true, provider: 'aws' },
    ]);
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      { id: 'o1', account_id: 'acc-1', provider: 'aws', service: 'ec2', term: 1 },
      { id: 'o2', account_id: 'acc-1', provider: 'aws', service: 'rds',  term: 1 },
    ]);
    await loadAccountsForProvider('aws');

    const overridesBtn = document.querySelector(
      `button[aria-label="Service overrides for Prod (111)"]`,
    ) as HTMLButtonElement;
    // Panel has existing overrides so the modal is not auto-opened; open manually.
    const panel = document.getElementById('account-overrides-modal-body') as HTMLElement;
    overridesBtn.click();
    await new Promise(r => setTimeout(r, 0));

    const addBtn = Array.from(panel.querySelectorAll('button')).find(b => b.textContent === 'Add override');
    expect(addBtn).toBeDefined();
    addBtn!.click();

    const bulkToggle = document.getElementById('override-bulk-toggle') as HTMLInputElement;
    bulkToggle.checked = true;
    bulkToggle.dispatchEvent(new Event('change'));

    const bulkList = document.getElementById('override-bulk-services-list') as HTMLElement;
    const values = Array.from(bulkList.querySelectorAll('input[type="checkbox"]')).map(
      cb => (cb as HTMLInputElement).value,
    );
    expect(values).not.toContain('ec2');
    expect(values).not.toContain('rds');
    expect(values).toContain('elasticache');
  });

  test('closing the modal resets bulk toggle to off', async () => {
    const { bulkToggle } = await openBulkModal();
    expect(bulkToggle.checked).toBe(true);

    (document.getElementById('close-override-modal-btn') as HTMLButtonElement).click();

    expect(bulkToggle.checked).toBe(false);
    const singleRow = document.getElementById('override-single-service-row') as HTMLElement;
    expect(singleRow.classList.contains('hidden')).toBe(false);
  });

  // Preventive conflict gating (issue #119): selecting an incompatible
  // term/payment combo must disable the conflicting service checkbox BEFORE
  // the user hits Save, so the all-or-nothing submit block is unreachable in
  // normal use.  The only AWS hard restriction is rds + 3yr + no-upfront.
  test('selecting 3yr/no-upfront disables rds checkbox and API is never called', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);
    (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue({});

    const { bulkList, form } = await openBulkModal();

    // Check rds first so it is "selected" before the conflict appears.
    const rdsCb = bulkList.querySelector<HTMLInputElement>('input[value="rds"]');
    expect(rdsCb).not.toBeNull();
    rdsCb!.checked = true;
    rdsCb!.dispatchEvent(new Event('change'));
    expect(rdsCb!.disabled).toBe(false);

    // Set term = 3, then payment = no-upfront (the invalid combo for rds).
    const termSel = document.getElementById('override-term') as HTMLSelectElement;
    termSel.value = '3';
    termSel.dispatchEvent(new Event('change'));

    const paymentSel = document.getElementById('override-payment') as HTMLSelectElement;
    paymentSel.value = 'no-upfront';
    paymentSel.dispatchEvent(new Event('change'));

    // rds must now be disabled and unchecked.
    expect(rdsCb!.disabled).toBe(true);
    expect(rdsCb!.checked).toBe(false);

    // A service with no restriction (e.g. ec2) stays enabled.
    const ec2Cb = bulkList.querySelector<HTMLInputElement>('input[value="ec2"]');
    expect(ec2Cb!.disabled).toBe(false);

    // Attempting to submit with rds disabled (nothing checked) must not call
    // the API and must show an inline error.
    form.dispatchEvent(new Event('submit', { cancelable: true }));
    await new Promise(r => setTimeout(r, 0));
    expect(api.saveAccountServiceOverride).not.toHaveBeenCalled();
  });

  test('switching back to a compatible combo re-enables the rds checkbox', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);

    const { bulkList } = await openBulkModal();

    // Trigger the invalid combo.
    const termSel = document.getElementById('override-term') as HTMLSelectElement;
    termSel.value = '3';
    termSel.dispatchEvent(new Event('change'));
    const paymentSel = document.getElementById('override-payment') as HTMLSelectElement;
    paymentSel.value = 'no-upfront';
    paymentSel.dispatchEvent(new Event('change'));

    const rdsCb = bulkList.querySelector<HTMLInputElement>('input[value="rds"]');
    expect(rdsCb!.disabled).toBe(true);

    // Switch payment to a valid combo (partial-upfront).
    paymentSel.value = 'partial-upfront';
    paymentSel.dispatchEvent(new Event('change'));

    // rds should be re-enabled.
    expect(rdsCb!.disabled).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Account form submit: custom onSave callback (registration approval, #210)
// ---------------------------------------------------------------------------

describe('Account form submit with onSave callback', () => {
  const submit = async (): Promise<void> => {
    document.getElementById('account-form')!.dispatchEvent(new Event('submit'));
    await new Promise(r => setTimeout(r, 0));
  };
  const modalHidden = (): boolean => document.getElementById('account-modal')!.classList.contains('hidden');

  beforeEach(() => {
    buildAccountsDOM();
    jest.clearAllMocks();
    (api.listAccounts as jest.Mock).mockResolvedValue([]);
    setupSettingsHandlers();
  });

  test('delegates to onSave with the modal provider and built request, skipping create/update', async () => {
    const onSave = jest.fn<Promise<void>, [string, api.CloudAccountRequest]>().mockResolvedValue(undefined);
    openAccountModal('gcp', undefined, { onSave });
    (document.getElementById('account-name') as HTMLInputElement).value = ' Reg ';
    (document.getElementById('account-external-id') as HTMLInputElement).value = 'proj-1';
    await submit();

    expect(onSave).toHaveBeenCalledTimes(1);
    const [provider, req] = onSave.mock.calls[0]!;
    expect(provider).toBe('gcp');
    expect(req).toMatchObject({ name: 'Reg', provider: 'gcp', external_id: 'proj-1', gcp_project_id: 'proj-1' });
    expect(api.createAccount).not.toHaveBeenCalled();
    expect(api.updateAccount).not.toHaveBeenCalled();
  });

  test('closes the modal only after onSave resolves', async () => {
    let resolve!: () => void;
    const onSave = jest.fn(() => new Promise<void>((r) => { resolve = r; }));
    openAccountModal('aws', undefined, { onSave });
    await submit();
    expect(onSave).toHaveBeenCalled();
    expect(modalHidden()).toBe(false);

    resolve();
    await new Promise(r => setTimeout(r, 0));
    expect(modalHidden()).toBe(true);
  });

  test('onSave failure keeps the modal open, toasts the error and keeps delegating', async () => {
    const onSave = jest.fn().mockRejectedValue(new Error('nope'));
    jest.spyOn(console, 'error').mockImplementation(() => {});
    openAccountModal('aws', undefined, { onSave });
    await submit();

    expect(modalHidden()).toBe(false);
    expect(mockShowToast).toHaveBeenCalledWith({ message: 'Failed to save: nope', kind: 'error' });
    await submit();
    expect(onSave).toHaveBeenCalledTimes(2);
    expect(api.createAccount).not.toHaveBeenCalled();
  });

  test.each([
    ['successful onSave', async (): Promise<void> => { await submit(); }],
    ['close button', async (): Promise<void> => { document.getElementById('close-account-modal-btn')!.click(); }],
  ])('onSave is reset after %s so the next submit uses the normal flow', async (_label, finish) => {
    const onSave = jest.fn().mockResolvedValue(undefined);
    (api.createAccount as jest.Mock).mockResolvedValue({ id: 'n', name: 'A', provider: 'aws', external_id: '1', enabled: true });
    openAccountModal('aws', undefined, { onSave });
    await finish();
    onSave.mockClear();

    // No reopen: openAccountModal would overwrite onSave and mask a missing reset.
    await submit();
    expect(onSave).not.toHaveBeenCalled();
    expect(api.createAccount).toHaveBeenCalledTimes(1);
  });
});
