/**
 * handleExecutePurchase single-record toast tests (and the #735 approval_recipient
 * toast). Split from purchase-execution-toast.test.ts to stay under the 500-line
 * ceiling; the module mocks and helpers live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  buildMinimalRec,
  setup,
  lastToastMessage,
  lastToastKind,
} from './purchase-execution-toast-harness';
import * as api from '../api';
import * as recs from '../recommendations';
import * as plans from '../plans';
import * as archera from '../archera';

// ── tests ─────────────────────────────────────────────────────────────────────

describe('handleExecutePurchase — single-record path', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    // Default: fan-out never opened (null; [] means fan-out is open with every bucket skipped), one single-record rec
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue(null);
    (recs.getPurchaseModalRecommendations as jest.Mock).mockReturnValue([buildMinimalRec()]);
    (plans.closePurchaseModal as jest.Mock).mockImplementation(() => undefined);
  });

  afterEach(() => {
    document.body.textContent = '';
  });

  test('Finding 1 — with approval_recipient: toast shows recipient name', async () => {
    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-1',
      status: 'queued',
      email_sent: true,
      approval_recipient: 'approver@example.com',
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(lastToastMessage()).toContain('approver@example.com');
    expect(lastToastMessage()).toContain('Approval request sent to');
    expect(lastToastKind()).toBe('success');
  });

  test.each(['0', '', 'abc', '50.5', '101'])(
    'capacity %p is refused before any POST instead of being coerced',
    async (typed) => {
      const btn = setup();
      (document.getElementById('bulk-purchase-capacity') as HTMLInputElement).value = typed;
      btn.click();
      await new Promise((r) => setTimeout(r, 0));

      expect(api.executePurchase).not.toHaveBeenCalled();
      expect(lastToastKind()).toBe('error');
      expect(lastToastMessage()).toContain('Capacity %');
    },
  );

  test('capacity 50 is submitted as 50', async () => {
    (api.executePurchase as jest.Mock).mockResolvedValue({ execution_id: 'e', status: 'queued', email_sent: true });
    const btn = setup();
    (document.getElementById('bulk-purchase-capacity') as HTMLInputElement).value = '50';
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect((api.executePurchase as jest.Mock).mock.calls[0]![1]).toBe(50);
  });

  test('Finding 1 — without approval_recipient: toast falls back to generic copy', async () => {
    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-2',
      status: 'queued',
      email_sent: true,
      // approval_recipient intentionally absent
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    const msg = lastToastMessage();
    expect(msg).toContain('check your email');
    expect(lastToastKind()).toBe('success');
  });

  test('email_sent === false: warning toast, not success', async () => {
    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-3',
      status: 'queued',
      email_sent: false,
      email_reason: 'no notification email configured',
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(lastToastKind()).toBe('warning');
    expect(lastToastMessage()).toContain('no notification email configured');
  });

  // Issue #499 follow-up: offer timing moved from post-execution to the
  // approval-submission success path (right after the user approves the
  // pre-purchase confirmation).
  test('opens Archera offer modal once the approval submission succeeds', async () => {
    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-ok',
      status: 'queued',
      email_sent: true,
      approval_recipient: 'approver@example.com',
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(archera.openArcheraOfferModal).toHaveBeenCalledTimes(1);
    expect(archera.openArcheraOfferModal).toHaveBeenCalledWith();
  });

  // Issue #1694: a recorded failed execution must not leave Execute armed.
  test('definite failure with an execution_id closes the modal', async () => {
    const err = Object.assign(new Error('AWS refused the purchase'), {
      status: 403, details: { execution_id: 'exec-failed' },
    });
    (api.executePurchase as jest.Mock).mockRejectedValue(err);

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(plans.closePurchaseModal).toHaveBeenCalledTimes(1);
    expect(recs.clearPurchaseModalRecommendations).toHaveBeenCalled();
    expect(lastToastKind()).toBe('error');
  });

  // A 5xx (e.g. a run whose final status could not be saved) or a 409 after the
  // row was claimed may follow a real purchase: never leave Execute armed.
  test.each([
    ['audit-loss 500', 500],
    ['stale-pricing 409', 409],
  ])('%s without an execution_id closes the modal', async (_name, status) => {
    (api.executePurchase as jest.Mock).mockRejectedValue(
      Object.assign(new Error('execution ran but its final status could not be saved'), { status }),
    );

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(plans.closePurchaseModal).toHaveBeenCalledTimes(1);
    expect(recs.clearPurchaseModalRecommendations).toHaveBeenCalled();
  });

  test('a 400 validation refusal keeps the modal open so the user can fix the input', async () => {
    (api.executePurchase as jest.Mock).mockRejectedValue(
      Object.assign(new Error('capacity must be between 1 and 100'), { status: 400 }),
    );

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(plans.closePurchaseModal).not.toHaveBeenCalled();
  });

  test('a late failure does not close a purchase modal opened after the request was sent', async () => {
    let reject: (e: unknown) => void = () => {};
    (api.executePurchase as jest.Mock).mockReturnValue(new Promise((_res, rej) => { reject = rej; }));

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));
    // The user closed the modal and opened a new purchase while the first was in flight.
    (recs.getPurchaseModalSession as jest.Mock).mockReturnValue(2);
    reject(Object.assign(new Error('refused'), { status: 403, details: { execution_id: 'exec-old' } }));
    await new Promise((r) => setTimeout(r, 0));

    expect(plans.closePurchaseModal).not.toHaveBeenCalled();
    expect(recs.clearPurchaseModalRecommendations).not.toHaveBeenCalled();
    expect(lastToastKind()).toBe('error');
  });
});
