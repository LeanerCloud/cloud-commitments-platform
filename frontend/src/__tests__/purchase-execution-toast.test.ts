/**
 * Tests for handleExecutePurchase / handleFanOutExecute toast messages.
 *
 * CR pass on PR #294 / issue #288:
 *   Finding 1 — success toasts must name the approval recipient when the
 *               backend returns approval_recipient.
 *   Finding 2 — fulfilled executePurchase responses with email_sent === false
 *               or status === 'failed' must count as failures, not successes.
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
import { confirmDialog } from '../confirmDialog';

// ── tests ─────────────────────────────────────────────────────────────────────

describe('handleFanOutExecute — fan-out path', () => {
  function buildBucket(id: string, capacityPercent = 100) {
    return {
      key: `key-${id}`,
      label: `Bucket ${id}`,
      provider: 'aws',
      service: `svc-${id}`,
      recs: [buildMinimalRec()],
      payment: 'all-upfront',
      capacityPercent,
    };
  }

  beforeEach(() => {
    jest.clearAllMocks();
    (recs.getPurchaseModalRecommendations as jest.Mock).mockReturnValue([]);
    (plans.closePurchaseModal as jest.Mock).mockImplementation(() => undefined);
  });

  afterEach(() => {
    document.body.textContent = '';
  });

  test('Finding 2 — email_sent === false counts as failure, email_reason in toast, recipient excluded', async () => {
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([
      buildBucket('a'),
      buildBucket('b'),
    ]);

    (api.executePurchase as jest.Mock)
      .mockResolvedValueOnce({
        execution_id: 'exec-a',
        status: 'queued',
        email_sent: true,
        approval_recipient: 'alice@example.com',
      })
      .mockResolvedValueOnce({
        execution_id: 'exec-b',
        status: 'queued',
        email_sent: false,
        email_reason: 'SMTP timeout',
        approval_recipient: 'bob@example.com',
      });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    const msg = lastToastMessage();
    // 1 of 2 succeeded; 1 failed (business-level email failure)
    expect(msg).toContain('1 of 2');
    expect(msg).toContain('failed');
    // The email_reason from the submission failure must appear in the message
    expect(msg).toContain('SMTP timeout');
    // bob's email_sent was false — must NOT appear as approved recipient
    expect(msg).not.toContain('bob@example.com');
    // Toast kind must reflect partial failure
    expect(lastToastKind()).toBe('warning');
  });

  test('#642 — partial fan-out failure names the submitted (still-actionable) buckets', async () => {
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([
      buildBucket('a'),
      buildBucket('b'),
    ]);

    // Bucket a submits cleanly (creates a pending execution); bucket b fails.
    (api.executePurchase as jest.Mock)
      .mockResolvedValueOnce({
        execution_id: 'exec-a',
        status: 'queued',
        email_sent: true,
        approval_recipient: 'alice@example.com',
      })
      .mockRejectedValueOnce(new Error('network down'));

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    const msg = lastToastMessage();
    expect(msg).toContain('1 of 2');
    expect(msg).toContain('failed');
    // The orphaned-but-actionable pending execution must be surfaced by name so
    // the user knows which request is live (issue #642).
    expect(msg).toContain('Submitted (awaiting approval)');
    expect(msg).toContain('AWS / Unattributed / svc-a@100%');
    // The failed bucket must NOT be listed as submitted.
    expect(msg).not.toContain('svc-b');
    expect(lastToastKind()).toBe('warning');
  });

  test('#434: the awaiting-approval note names each bucket\'s account on a multi-account split', async () => {
    // Same provider/service/capacity in two accounts: without the account the
    // note read "AWS svc-x@100%, AWS svc-x@100%".
    const inAccount = (accountId: string | undefined) => ({
      ...buildBucket('x'),
      recs: [{ ...buildMinimalRec(), cloud_account_id: accountId }],
    });
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([
      inAccount('acct-a'),
      inAccount('acct-b'),
      inAccount(undefined),
      inAccount('acct-c'),
    ]);
    (api.executePurchase as jest.Mock)
      .mockResolvedValueOnce({ execution_id: 'exec-a', status: 'queued', email_sent: true })
      .mockResolvedValueOnce({ execution_id: 'exec-b', status: 'queued', email_sent: true })
      .mockResolvedValueOnce({ execution_id: 'exec-u', status: 'queued', email_sent: true })
      .mockRejectedValueOnce(new Error('network down'));

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    const msg = lastToastMessage();
    expect(msg).toContain(
      'Submitted (awaiting approval): AWS / acct-a / svc-x@100%, AWS / acct-b / svc-x@100%, AWS / Unattributed / svc-x@100%.',
    );
    expect(msg).not.toContain('AWS / acct-c / svc-x');
  });

  test('#434: a joined bucket names its account alongside the existing execution', async () => {
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([
      { ...buildBucket('a'), recs: [{ ...buildMinimalRec(), cloud_account_id: 'acct-a' }] },
      { ...buildBucket('b'), recs: [{ ...buildMinimalRec(), cloud_account_id: 'acct-b' }] },
    ]);
    (api.executePurchase as jest.Mock)
      .mockResolvedValueOnce({ execution_id: 'abcd1234-0000', status: 'pending', email_sent: true, duplicate: true })
      .mockRejectedValueOnce(new Error('network down'));

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(lastToastMessage()).toContain('Joined existing: AWS / acct-a / svc-a@100% (existing abcd1234: ');
  });

  test('#333: partial fan-out failure prefixes each failure with its bucket account', async () => {
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([
      { ...buildBucket('a'), recs: [{ ...buildMinimalRec(), cloud_account_id: 'acct-a' }] },
      { ...buildBucket('b'), recs: [{ ...buildMinimalRec(), cloud_account_id: 'acct-b' }] },
    ]);
    (api.executePurchase as jest.Mock)
      .mockResolvedValueOnce({ execution_id: 'exec-a', status: 'queued', email_sent: true })
      .mockRejectedValueOnce(new Error('network down'));

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    const msg = lastToastMessage();
    expect(msg).toContain('failed: acct-b: network down');
    expect(msg).not.toContain('acct-a:');
  });

  test('Finding 2 — status === "failed" also counts as failure, recipient excluded', async () => {
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([
      buildBucket('a'),
      buildBucket('b'),
    ]);

    (api.executePurchase as jest.Mock)
      .mockResolvedValueOnce({
        execution_id: 'exec-a',
        status: 'queued',
        email_sent: true,
        approval_recipient: 'alice@example.com',
      })
      .mockResolvedValueOnce({
        execution_id: 'exec-b',
        status: 'failed',
        email_sent: undefined,
        email_reason: 'backend error',
        approval_recipient: 'bob@example.com',
      });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    const msg = lastToastMessage();
    expect(msg).toContain('1 of 2');
    expect(msg).toContain('failed');
    expect(msg).toContain('backend error');
    expect(msg).not.toContain('bob@example.com');
    expect(lastToastKind()).toBe('warning');
  });

  test('Finding 1 fan-out — all succeed: success toast lists unique recipients', async () => {
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([
      buildBucket('a'),
      buildBucket('b'),
    ]);

    (api.executePurchase as jest.Mock)
      .mockResolvedValueOnce({
        execution_id: 'exec-a',
        status: 'queued',
        email_sent: true,
        approval_recipient: 'alice@example.com',
      })
      .mockResolvedValueOnce({
        execution_id: 'exec-b',
        status: 'queued',
        email_sent: true,
        approval_recipient: 'bob@example.com',
      });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    const msg = lastToastMessage();
    expect(lastToastKind()).toBe('success');
    expect(msg).toContain('alice@example.com');
    expect(msg).toContain('bob@example.com');
    expect(msg).toContain('sent for approval');
  });

  test('Finding 1 fan-out — deduplicates same recipient across buckets', async () => {
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([
      buildBucket('a'),
      buildBucket('b'),
    ]);

    (api.executePurchase as jest.Mock)
      .mockResolvedValueOnce({
        execution_id: 'exec-a',
        status: 'queued',
        email_sent: true,
        approval_recipient: 'shared@example.com',
      })
      .mockResolvedValueOnce({
        execution_id: 'exec-b',
        status: 'queued',
        email_sent: true,
        approval_recipient: 'shared@example.com',
      });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    const msg = lastToastMessage() ?? '';
    expect(lastToastKind()).toBe('success');
    // Address should appear exactly once after deduplication
    const occurrences = (msg.match(/shared@example\.com/g) ?? []).length;
    expect(occurrences).toBe(1);
  });

  test('Finding 2 — all email failures: error toast with reason', async () => {
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([
      buildBucket('a'),
    ]);

    (api.executePurchase as jest.Mock).mockResolvedValueOnce({
      execution_id: 'exec-a',
      status: 'queued',
      email_sent: false,
      email_reason: 'no SMTP config',
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    const msg = lastToastMessage();
    expect(lastToastKind()).toBe('error');
    expect(msg).toContain('no SMTP config');
    // Issue #499 follow-up: with zero successful submissions there is nothing
    // to insure, so the offer must not fire.
    expect(archera.openArcheraOfferModal).not.toHaveBeenCalled();
  });

  // Issue #499 follow-up: offer fires when at least one bucket's approval
  // submission succeeds, immediately after the user approves the confirmation.
  test('opens Archera offer modal when at least one bucket succeeds', async () => {
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([
      buildBucket('a'),
      buildBucket('b'),
    ]);

    (api.executePurchase as jest.Mock)
      .mockResolvedValueOnce({
        execution_id: 'exec-a',
        status: 'queued',
        email_sent: true,
        approval_recipient: 'alice@example.com',
      })
      .mockResolvedValueOnce({
        execution_id: 'exec-b',
        status: 'failed',
        email_reason: 'backend error',
      });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(archera.openArcheraOfferModal).toHaveBeenCalledTimes(1);
    expect(archera.openArcheraOfferModal).toHaveBeenCalledWith();
  });

  // Issue #597: details must be preserved in the POST body on the fan-out path.
  // Windows EC2 rec with a non-trivial details blob.
  test('#597 fan-out — details blob preserved for EC2 Windows rec', async () => {
    const windowsDetails = { platform: 'Windows', tenancy: 'default', scope: 'Region' };
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([
      {
        key: 'key-win',
        label: 'Windows EC2',
        payment: 'all-upfront' as const,
        capacityPercent: 100,
        recs: [
          {
            id: 'rec-win-1',
            provider: 'aws' as const,
            service: 'ec2',
            region: 'us-east-1',
            resource_type: 'm5.large',
            engine: undefined,
            details: windowsDetails,
            count: 2,
            term: 1,
            payment: 'all-upfront',
            upfront_cost: 500,
            monthly_cost: 50,
            savings: 100,
          },
        ],
      },
    ]);

    (api.executePurchase as jest.Mock).mockResolvedValueOnce({
      execution_id: 'exec-win',
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

  // Issue #597: details must be preserved in the POST body on the fan-out path.
  // RDS Postgres rec.
  test('#597 fan-out — details blob preserved for RDS Postgres rec', async () => {
    const postgresDetails = { engine: 'postgres', multi_az: true };
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue([
      {
        key: 'key-rds',
        label: 'RDS Postgres',
        payment: 'partial-upfront' as const,
        capacityPercent: 100,
        recs: [
          {
            id: 'rec-rds-1',
            provider: 'aws' as const,
            service: 'rds',
            region: 'eu-west-1',
            resource_type: 'db.t3.medium',
            engine: 'postgres',
            details: postgresDetails,
            count: 1,
            term: 1,
            payment: 'partial-upfront',
            upfront_cost: 300,
            monthly_cost: 80,
            savings: 60,
          },
        ],
      },
    ]);

    (api.executePurchase as jest.Mock).mockResolvedValueOnce({
      execution_id: 'exec-rds',
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
    expect(submittedRecs[0]?.details).toEqual(postgresDetails);
    expect(submittedRecs[0]?.engine).toBe('postgres');
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
