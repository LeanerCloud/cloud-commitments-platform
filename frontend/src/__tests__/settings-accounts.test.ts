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
  setupSettingsHandlers,
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
