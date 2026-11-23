/**
 * handleExecutePurchase double-submit guard, payment coercion disclosure and
 * missing payment option toast tests. Split from purchase-execution-toast.test.ts
 * to stay under the 500-line ceiling; the module mocks and helpers live in the
 * harness.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  buildMinimalRec,
  setup,
  lastToastMessage,
  allToastMessages,
  lastToastKind,
} from './purchase-execution-toast-harness';
import * as api from '../api';
import * as recs from '../recommendations';
import * as plans from '../plans';
import { confirmDialog } from '../confirmDialog';

// ── tests ─────────────────────────────────────────────────────────────────────

// #644: the execute button must be disabled BEFORE the confirm dialog / network
// call so a double-click can't fire a second POST (duplicate pending execution).
describe('handleExecutePurchase — double-submit guard (#644)', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue(null);
    (recs.getPurchaseModalRecommendations as jest.Mock).mockReturnValue([buildMinimalRec()]);
    (plans.closePurchaseModal as jest.Mock).mockImplementation(() => undefined);
  });

  afterEach(() => {
    document.body.textContent = '';
  });

  test('button is disabled while the confirm dialog is pending (before any POST)', async () => {
    let resolveConfirm: (v: boolean) => void = () => undefined;
    (confirmDialog as jest.Mock).mockReturnValueOnce(
      new Promise<boolean>((resolve) => {
        resolveConfirm = resolve;
      }),
    );
    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-1',
      status: 'pending',
      email_sent: true,
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    // Confirm dialog is still open: button disabled, no POST yet.
    expect(btn.disabled).toBe(true);
    expect(api.executePurchase).not.toHaveBeenCalled();

    resolveConfirm(true);
    await new Promise((r) => setTimeout(r, 0));
    expect(api.executePurchase).toHaveBeenCalledTimes(1);
  });

  test('button is re-enabled when the user cancels the confirm dialog', async () => {
    (confirmDialog as jest.Mock).mockResolvedValueOnce(false);

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(api.executePurchase).not.toHaveBeenCalled();
    expect(btn.disabled).toBe(false);
    expect(btn.textContent).toBe('Send for approval');
  });
});

// ── #1503: payment-option coercion must be disclosed to the user ─────────────
//
// The backend does not reject a payment option the target provider cannot
// express (Azure has exactly two billing plans, so an inherited AWS-style
// 'partial-upfront' token has nowhere to land). It coerces to the closest
// supported schedule and reports it in `payment_adjustments`. That coercion is
// only defensible if the user is actually TOLD -- otherwise their billing
// schedule changes behind their back, which is the failure #1503 reported.
//
// These are the end-to-end guards: a green unit test on the formatter alone
// could not prove the notice survives to a toast the user actually sees.
describe('#1503 — payment-option coercion is disclosed in the purchase toast', () => {
  function buildAzureBucket(id: string) {
    return {
      key: `key-${id}`,
      label: `Bucket ${id}`,
      provider: 'azure',
      service: `svc-${id}`,
      recs: [buildMinimalRec()],
      payment: 'partial-upfront',
      capacityPercent: 100,
    };
  }

  beforeEach(() => {
    jest.clearAllMocks();
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue(null);
    (recs.getPurchaseModalRecommendations as jest.Mock).mockReturnValue([buildMinimalRec()]);
    (plans.closePurchaseModal as jest.Mock).mockImplementation(() => undefined);
  });

  afterEach(() => {
    document.body.textContent = '';
  });

  test('single path — partial-upfront coerced to monthly raises a warning toast', async () => {
    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-adj-1',
      status: 'queued',
      email_sent: true,
      approval_recipient: 'approver@example.com',
      payment_adjustments: [
        {
          rec_index: 0,
          provider: 'azure',
          service: 'vm',
          requested_payment_option: 'partial-upfront',
          applied_payment_option: 'monthly',
          reason: 'payment option "partial-upfront" is not in azure\'s supported set',
        },
      ],
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    const messages = allToastMessages();
    // The success toast must still be shown -- disclosure is additive.
    expect(messages.some((m) => m.includes('Approval request sent to'))).toBe(true);
    // ...and the coercion must be surfaced, naming both schedules so the user
    // can tell what they asked for from what they are actually getting.
    const notice = messages.find((m) => m.includes('Billing schedule adjusted'));
    expect(notice).toBeDefined();
    expect(notice).toContain('Partial Upfront');
    expect(notice).toContain('Pay Monthly');
    expect(notice).toContain('AZURE');
    // Warning, and non-expiring: an irreversible billing-schedule change must
    // not scroll away before the user reads it.
    expect(lastToastKind()).toBe('warning');
  });

  test('single path — no adjustments means no extra toast (no noise on the common case)', async () => {
    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-adj-2',
      status: 'queued',
      email_sent: true,
      approval_recipient: 'approver@example.com',
      // payment_adjustments intentionally absent: everything was canonical.
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    const messages = allToastMessages();
    expect(messages.some((m) => m.includes('Billing schedule adjusted'))).toBe(false);
    expect(lastToastKind()).toBe('success');
  });

  test('fan-out path — adjustments from every bucket are disclosed, incl. email-failed buckets', async () => {
    (recs.getPurchaseModalRecommendations as jest.Mock).mockReturnValue([]);
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([
      buildAzureBucket('a'),
      buildAzureBucket('b'),
    ]);

    const adjustment = (recIndex: number) => ({
      rec_index: recIndex,
      provider: 'azure',
      service: 'vm',
      requested_payment_option: 'partial-upfront',
      applied_payment_option: 'monthly',
      reason: 'no azure equivalent',
    });

    (api.executePurchase as jest.Mock)
      .mockResolvedValueOnce({
        execution_id: 'exec-a',
        status: 'queued',
        email_sent: true,
        approval_recipient: 'alice@example.com',
        payment_adjustments: [adjustment(0)],
      })
      // Bucket b's approval email failed to send, but the pending execution
      // still exists carrying the coerced schedule, so its adjustment must
      // be disclosed too rather than dropped with the "failed" bucket.
      .mockResolvedValueOnce({
        execution_id: 'exec-b',
        status: 'queued',
        email_sent: false,
        email_reason: 'SMTP timeout',
        payment_adjustments: [adjustment(0)],
      });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    const notice = allToastMessages().find((m) => m.includes('Billing schedule adjusted'));
    expect(notice).toBeDefined();
    // Both buckets contributed, so the count must be 2 -- proving the
    // email-failed bucket's coercion was not silently dropped.
    expect(notice).toContain('2 recommendations');
    expect(notice).toContain('Partial Upfront');
    expect(notice).toContain('Pay Monthly');
  });
});

describe('handleExecutePurchase - missing payment option (#431)', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue(null);
    (plans.closePurchaseModal as jest.Mock).mockImplementation(() => undefined);
  });

  afterEach(() => {
    document.body.textContent = '';
  });

  test.each([
    ['undefined', undefined],
    ['empty string', ''],
    ['whitespace only', '   '],
  ])('refuses with an error toast and no POST when payment is %s', async (_label, payment) => {
    const rec: Record<string, unknown> = { ...buildMinimalRec() };
    if (payment === undefined) delete rec.payment;
    else rec.payment = payment;
    (recs.getPurchaseModalRecommendations as jest.Mock).mockReturnValue([rec]);

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(api.executePurchase).not.toHaveBeenCalled();
    expect(confirmDialog).not.toHaveBeenCalled();
    expect(lastToastKind()).toBe('error');
    expect(lastToastMessage()).toContain('no payment option');
    expect(btn.disabled).toBe(false);
    expect(plans.closePurchaseModal).not.toHaveBeenCalled();
  });

  test('refuses the whole batch when only one of several recs lacks payment', async () => {
    const missing: Record<string, unknown> = { ...buildMinimalRec(), id: 'rec-2' };
    delete missing.payment;
    (recs.getPurchaseModalRecommendations as jest.Mock).mockReturnValue([buildMinimalRec(), missing]);

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(api.executePurchase).not.toHaveBeenCalled();
    expect(lastToastMessage()).toContain('1 of 2');
  });

  test('a rec with a payment option is sent unchanged', async () => {
    (recs.getPurchaseModalRecommendations as jest.Mock).mockReturnValue([
      { ...buildMinimalRec(), payment: 'no-upfront' },
    ]);
    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-1',
      status: 'queued',
      email_sent: true,
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    const sent = (api.executePurchase as jest.Mock).mock.calls[0][0];
    expect(sent[0].payment).toBe('no-upfront');
  });
});
