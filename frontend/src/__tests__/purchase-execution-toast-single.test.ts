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

  test('failure without an execution_id keeps the modal open for retry', async () => {
    (api.executePurchase as jest.Mock).mockRejectedValue(new Error('network down'));

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(plans.closePurchaseModal).not.toHaveBeenCalled();
  });

  test('does NOT open Archera offer modal when the approval submission throws', async () => {
    (api.executePurchase as jest.Mock).mockRejectedValue(new Error('boom'));

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(lastToastKind()).toBe('error');
    expect(archera.openArcheraOfferModal).not.toHaveBeenCalled();
  });

  // Issue #331: fan-out open with every bucket skipped must not fall through
  // to (possibly stale) single-row state.
  test('fan-out with every bucket skipped refuses instead of submitting single-row state', async () => {
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([]);

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(api.executePurchase).not.toHaveBeenCalled();
    expect(lastToastMessage()).toBe('No recommendations selected for purchase.');
    expect(lastToastKind()).toBe('warning');
  });

  // Issue #597: details must be preserved in the POST body on the single-rec path.
  test('#597 single-rec — details blob preserved for EC2 Windows rec', async () => {
    const windowsDetails = { platform: 'Windows', tenancy: 'dedicated', scope: 'Region' };
    (recs.getPurchaseModalRecommendations as jest.Mock).mockReturnValue([
      {
        id: 'rec-win-single',
        provider: 'aws' as const,
        service: 'ec2',
        region: 'us-east-1',
        resource_type: 'm5.xlarge',
        engine: undefined,
        details: windowsDetails,
        count: 1,
        term: 1,
        payment: 'all-upfront',
        upfront_cost: 400,
        monthly_cost: 40,
        savings: 80,
      },
    ]);

    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-win-single',
      status: 'queued',
      email_sent: true,
      approval_recipient: 'approver@example.com',
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(api.executePurchase).toHaveBeenCalledTimes(1);
    const [submittedRecs] = (api.executePurchase as jest.Mock).mock.calls[0] as [
      Array<{ details?: unknown }>,
      number,
    ];
    expect(submittedRecs[0]?.details).toEqual(windowsDetails);
  });

  // Issue #597: details must be preserved in the POST body on the single-rec path.
  test('#597 single-rec — details blob preserved for RDS Postgres rec', async () => {
    const rdsDetails = { engine: 'postgres', multi_az: false };
    (recs.getPurchaseModalRecommendations as jest.Mock).mockReturnValue([
      {
        id: 'rec-rds-single',
        provider: 'aws' as const,
        service: 'rds',
        region: 'ap-southeast-1',
        resource_type: 'db.r5.large',
        engine: 'postgres',
        details: rdsDetails,
        count: 1,
        term: 3,
        payment: 'no-upfront',
        upfront_cost: 0,
        monthly_cost: 120,
        savings: 90,
      },
    ]);

    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-rds-single',
      status: 'queued',
      email_sent: true,
      approval_recipient: 'approver@example.com',
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(api.executePurchase).toHaveBeenCalledTimes(1);
    const [submittedRecs] = (api.executePurchase as jest.Mock).mock.calls[0] as [
      Array<{ details?: unknown; engine?: string }>,
      number,
    ];
    expect(submittedRecs[0]?.details).toEqual(rdsDetails);
    expect(submittedRecs[0]?.engine).toBe('postgres');
  });

  // Issue #647: a scaled rec carries recommended_count (the pre-scaling count)
  // so the backend can verify capacity_percent against the scaled count. The
  // single-rec submit path must forward it unchanged in the POST body.
  test('#647 single-rec — recommended_count preserved in POST body', async () => {
    (recs.getPurchaseModalRecommendations as jest.Mock).mockReturnValue([
      {
        ...buildMinimalRec(),
        count: 5,
        recommended_count: 10,
      },
    ]);
    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-647',
      status: 'queued',
      email_sent: true,
      approval_recipient: 'approver@example.com',
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(api.executePurchase).toHaveBeenCalledTimes(1);
    const [submittedRecs] = (api.executePurchase as jest.Mock).mock.calls[0] as [
      Array<{ count?: number; recommended_count?: number }>,
      number,
    ];
    expect(submittedRecs[0]?.count).toBe(5);
    expect(submittedRecs[0]?.recommended_count).toBe(10);
  });
});


// Issue #735: toast must show notification_email (approval_recipient from API),
// not a hardcoded or per-account contact_email. The backend now sets
// approval_recipient = notification_email when configured; the toast must use
// whatever the API returns without a hardcoded fallback.
describe('issue #735 — toast uses API approval_recipient, not hardcoded email', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue(null);
    (recs.getPurchaseModalRecommendations as jest.Mock).mockReturnValue([buildMinimalRec()]);
    (plans.closePurchaseModal as jest.Mock).mockImplementation(() => undefined);
  });

  afterEach(() => {
    document.body.textContent = '';
  });

  test('toast shows the notification_email returned by the API as approval_recipient', async () => {
    const notificationEmail = 'admin@company.example';
    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-735',
      status: 'queued',
      email_sent: true,
      approval_recipient: notificationEmail,
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(lastToastMessage()).toContain(notificationEmail);
    expect(lastToastMessage()).toContain('Approval request sent to');
    expect(lastToastKind()).toBe('success');
  });

  test('toast does not contain a different address when API returns notification_email', async () => {
    const notificationEmail = 'admin@company.example';
    const contactEmail = 'contact@acct.example';
    // Simulate backend now returning notification_email (not contact_email).
    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-735b',
      status: 'queued',
      email_sent: true,
      approval_recipient: notificationEmail,
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(lastToastMessage()).toContain(notificationEmail);
    expect(lastToastMessage()).not.toContain(contactEmail);
    expect(lastToastKind()).toBe('success');
  });
});
