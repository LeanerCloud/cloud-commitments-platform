/**
 * History Approve/Cancel actions: ownership and refresh recovery (#249). Split
 * from history-approve-button.test.ts to stay under the 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import { ADMIN_USER, DETAILS, deferred, settleActions, setupDOM, makeRow } from './history-approve-button-harness';
import { loadHistory, setupHistoryHandlers, viewPlanHistory } from '../history';
import * as api from '../api';
import { confirmDialog } from '../confirmDialog';
import { showToast } from '../toast';
import { getCurrentUser, subscribeAmortizeUpfront } from '../state';

describe('History action ownership and refresh recovery (#249)', () => {
  const data = { summary: {}, purchases: [makeRow({ purchase_id: 'guarded' })] };
  beforeEach(() => {
    setupDOM();
    jest.clearAllMocks();
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (api.getHistory as jest.Mock).mockReset().mockResolvedValue(data);
    (api.getPurchaseDetails as jest.Mock).mockReset().mockResolvedValue(DETAILS);
    (api.approvePurchase as jest.Mock).mockReset().mockResolvedValue(undefined);
    (confirmDialog as jest.Mock).mockReset().mockResolvedValue(true);
    (showToast as jest.Mock).mockReset();
  });

  test('details fetch owns both actions and same-ID projections while other IDs remain independent', async () => {
    const details = deferred<typeof DETAILS>();
    (api.getPurchaseDetails as jest.Mock).mockReturnValue(details.promise);
    (api.getHistory as jest.Mock).mockResolvedValue({ ...data, purchases: [...data.purchases, makeRow({ purchase_id: 'other' })] });
    (confirmDialog as jest.Mock).mockResolvedValue(false);
    await loadHistory();
    const btn = document.querySelector<HTMLButtonElement>('#purchases-approval-queue [data-approve-id="guarded"]')!;
    btn.click();
    btn.dispatchEvent(new MouseEvent('click'));
    document.querySelector<HTMLButtonElement>('#purchases-approval-queue [data-cancel-id="guarded"]')!.click();
    document.querySelector<HTMLButtonElement>('#purchases-approval-queue [data-approve-id="guarded"]')!.click();
    document.querySelector<HTMLButtonElement>('#purchases-approval-queue [data-approve-id="other"]')!.click();
    const disabled = btn.disabled;
    details.resolve(DETAILS);
    await settleActions();
    expect(disabled).toBe(true);
    expect(api.getPurchaseDetails).toHaveBeenCalledTimes(2);
    expect(confirmDialog).toHaveBeenCalledTimes(2);
    expect(api.approvePurchase).not.toHaveBeenCalled();
    expect(api.cancelPurchase).not.toHaveBeenCalled();
    expect(btn.disabled).toBe(false);
  });

  test.each([
    ['the details fetch fails', () => (api.getPurchaseDetails as jest.Mock).mockRejectedValue(new Error('403 Forbidden'))],
    ['the details carry no recommendations', () => (api.getPurchaseDetails as jest.Mock).mockResolvedValue({ ...DETAILS, recommendations: [] })],
  ])('Approve is not offered when %s (issue #247)', async (_label, arrange) => {
    arrange();
    console.error = jest.fn();
    await loadHistory();
    const btn = document.querySelector<HTMLButtonElement>('#purchases-approval-queue .history-approve-btn')!;
    btn.click();
    await settleActions();
    expect(confirmDialog).not.toHaveBeenCalled();
    expect(api.approvePurchase).not.toHaveBeenCalled();
    expect(showToast).toHaveBeenCalledWith(expect.objectContaining({
      kind: 'error',
      message: expect.stringContaining('cannot be approved without showing the amount'),
    }));
    expect(btn.disabled).toBe(false);
  });

  test.each(['cancel', 'reject', 'throw', 'api-error'] as const)('%s releases ownership for a later explicit attempt', async outcome => {
    await loadHistory();
    const btn = document.querySelector<HTMLButtonElement>('#purchases-approval-queue .history-approve-btn')!;
    if (outcome === 'cancel') (confirmDialog as jest.Mock).mockResolvedValueOnce(false);
    if (outcome === 'reject') (confirmDialog as jest.Mock).mockRejectedValueOnce(new Error('dialog failed'));
    if (outcome === 'throw') (confirmDialog as jest.Mock).mockImplementationOnce(() => { throw new Error('dialog failed'); });
    if (outcome === 'api-error') (api.approvePurchase as jest.Mock).mockRejectedValueOnce(new Error('mutation failed'));
    btn.click();
    await settleActions();
    expect(btn.disabled).toBe(false);
    const count = outcome === 'api-error' ? 1 : 0;
    expect(api.approvePurchase).toHaveBeenCalledTimes(count);
    btn.click();
    await settleActions();
    expect(api.approvePurchase).toHaveBeenCalledTimes(count + 1);
  });

  test('API remains guarded across a cached re-render and direct disabled dispatch', async () => {
    const mutation = deferred<void>();
    (api.approvePurchase as jest.Mock).mockReturnValue(mutation.promise);
    setupHistoryHandlers();
    await loadHistory();
    const btn = document.querySelector<HTMLButtonElement>('#purchases-approval-queue .history-approve-btn')!;
    btn.click();
    await settleActions();
    const redraw = (subscribeAmortizeUpfront as jest.Mock).mock.calls[0]![0] as () => void;
    redraw();
    document.querySelector<HTMLButtonElement>('#purchases-approval-queue .history-approve-btn')!.click();
    document.querySelector<HTMLButtonElement>('#purchases-approval-queue .history-cancel-btn')!.click();
    btn.dispatchEvent(new MouseEvent('click'));
    mutation.resolve();
    await settleActions();
    expect(api.approvePurchase).toHaveBeenCalledTimes(1);
    expect(api.cancelPurchase).not.toHaveBeenCalled();
  });

  test.each([false, true])('success survives failed refresh and cached redraw (toast throws=%s)', async toastThrows => {
    setupHistoryHandlers();
    await loadHistory();
    (api.getHistory as jest.Mock).mockRejectedValueOnce(new Error('refresh failed'));
    if (toastThrows) (showToast as jest.Mock).mockImplementationOnce(() => { throw new Error('toast failed'); });
    document.querySelector<HTMLButtonElement>('#purchases-approval-queue .history-approve-btn')!.click();
    await settleActions();
    const redraw = (subscribeAmortizeUpfront as jest.Mock).mock.calls[0]![0] as () => void;
    redraw();
    expect(document.querySelectorAll('[data-approve-id="guarded"]')).toHaveLength(0);
    expect(api.approvePurchase).toHaveBeenCalledTimes(1);
    expect(api.getHistory).toHaveBeenCalledTimes(2);
    expect((showToast as jest.Mock).mock.calls.some(([opts]) => opts.message?.startsWith('Failed to approve'))).toBe(false);
    await loadHistory();
    expect(document.querySelectorAll('[data-approve-id="guarded"]')).toHaveLength(1);
  });

  test('a purchase that is no longer pending shows a sticky refusal without a retry hint (issue #728)', async () => {
    setupHistoryHandlers();
    await loadHistory();
    (api.getPurchaseDetails as jest.Mock).mockResolvedValue({ ...DETAILS, status: 'approved' });
    document.querySelector<HTMLButtonElement>('#purchases-approval-queue .history-approve-btn')!.click();
    await settleActions();
    expect(showToast).toHaveBeenCalledWith({
      message: 'This purchase is already approved and can no longer be approved.', kind: 'error', timeout: null,
    });
    expect(api.approvePurchase).not.toHaveBeenCalled();
  });

  test('a failed approve toast stays until dismissed (issue #728)', async () => {
    setupHistoryHandlers();
    await loadHistory();
    (api.approvePurchase as jest.Mock).mockRejectedValueOnce(new Error('boom'));
    document.querySelector<HTMLButtonElement>('#purchases-approval-queue .history-approve-btn')!.click();
    await settleActions();
    expect(showToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Failed to approve: boom', kind: 'error', timeout: null }));
  });

  test.each(['history', 'plan'] as const)('%s loader cannot publish responses started before a successful mutation', async loader => {
    const old = deferred<typeof data>();
    setupHistoryHandlers();
    await loadHistory();
    (api.getHistory as jest.Mock).mockReturnValueOnce(old.promise);
    const oldLoad = loader === 'history' ? loadHistory() : viewPlanHistory('plan-1');
    const redraw = (subscribeAmortizeUpfront as jest.Mock).mock.calls[0]![0] as () => void;
    redraw();
    (api.getHistory as jest.Mock).mockRejectedValueOnce(new Error('refresh failed'));
    document.querySelector<HTMLButtonElement>('#purchases-approval-queue .history-approve-btn')!.click();
    await settleActions();
    old.resolve(data);
    await oldLoad;
    redraw();
    expect(document.querySelectorAll('[data-approve-id="guarded"]')).toHaveLength(0);
    await loadHistory();
    expect(document.querySelectorAll('[data-approve-id="guarded"]')).toHaveLength(1);
  });

  test.each(['history', 'plan'] as const)('%s loader ignores old errors after a successful mutation refresh', async loader => {
    const old = deferred<typeof data>();
    setupHistoryHandlers();
    await loadHistory();
    (api.getHistory as jest.Mock).mockReturnValueOnce(old.promise);
    const oldLoad = loader === 'history' ? loadHistory() : viewPlanHistory('plan-1');
    ((subscribeAmortizeUpfront as jest.Mock).mock.calls[0]![0] as () => void)();
    document.querySelector<HTMLButtonElement>('#purchases-approval-queue .history-approve-btn')!.click();
    await settleActions();
    old.reject(new Error('old failure'));
    await oldLoad;
    expect(document.querySelectorAll('[data-approve-id="guarded"]')).toHaveLength(1);
    expect(document.getElementById('history-list')!.textContent).not.toContain('old failure');
  });
});
