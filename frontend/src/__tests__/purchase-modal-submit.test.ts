/**
 * Purchase-modal submit regressions that span both modal modes: the
 * Execute Now confirm copy (#251, #701), Escape discarding purchase state
 * (#331), interleaved single-bucket and fan-out opens (#430) and per-account
 * fan-out buckets (#333).
 *
 * The other purchase-modal suites split out of this file (#332):
 * purchase-modal-variant-identity, purchase-modal-repricing,
 * purchase-modal-fanout and purchase-modal-fanout-payment. Shared mocks,
 * fixtures and the per-test reset live in purchase-modal-harness.ts.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  buildRows,
  deferred,
  flush,
  setupPurchaseModalTest,
} from './purchase-modal-harness';
import * as api from '../api';
import * as state from '../state';
import { confirmDialog } from '../confirmDialog';
import { openModal } from '../modal';
import {
  openPurchaseModal,
  getPurchaseModalRecommendations,
  getFanOutBuckets,
  loadRecommendations,
  scaleRecForCapacity,
} from '../recommendations';
import { showToast } from '../toast';
import { formatCurrency } from '../utils';
import type { LocalRecommendation } from '../types';

beforeEach(setupPurchaseModalTest);

describe('Issue #251: Execute Now makes no cancellation promise', () => {
  test.each<Pick<LocalRecommendation, 'provider' | 'service' | 'payment' | 'upfront_cost'>>([
    { provider: 'aws', service: 'ec2', payment: 'all-upfront', upfront_cost: 1200 },
    { provider: 'azure', service: 'compute', payment: 'all-upfront', upfront_cost: 2400 },
    { provider: 'gcp', service: 'gce', payment: 'monthly', upfront_cost: 0 },
  ])('$provider shows the charge and approval bypass only', async (sample) => {
    const rec: LocalRecommendation = { ...buildRows()[0]!, ...sample, term: 1 };
    (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
    await openPurchaseModal([rec]);
    const warning = document.querySelector<HTMLElement>('.direct-execute-warning')!;
    expect(warning).not.toBeNull();
    expect(warning.hidden).toBe(true);
    (document.getElementById('execute-mode-direct') as HTMLInputElement).click();
    expect(warning.hidden).toBe(false);
    expect(warning.textContent).toBe(
      `Warning: This will charge ${formatCurrency(sample.upfront_cost, '$', 2)} upfront immediately. This bypasses the approval step.`,
    );
    expect(warning.textContent).not.toMatch(/AWS|cancell|24 hours/);
    expect(document.getElementById('execute-purchase-btn')?.textContent).toBe('Execute purchase now');
    (document.getElementById('execute-mode-approval') as HTMLInputElement).click();
    expect(warning.hidden).toBe(true);
    expect(document.getElementById('execute-purchase-btn')?.textContent).toBe('Send for approval');
    (document.getElementById('execute-mode-direct') as HTMLInputElement).click();
    (document.getElementById('close-purchase-modal-btn') as HTMLButtonElement).click();
    await openPurchaseModal([rec]);
    expect((document.getElementById('execute-mode-approval') as HTMLInputElement).checked).toBe(true);
    expect(document.querySelector<HTMLElement>('.direct-execute-warning')!.hidden).toBe(true);
    expect(api.executePurchase).not.toHaveBeenCalled();
  });

  test('approval confirm labels the amount as upfront on approval, not charged today', async () => {
    const rec: LocalRecommendation = { ...buildRows()[0]!, provider: 'aws', service: 'ec2', term: 1 };
    (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
    (confirmDialog as jest.Mock).mockClear();
    await openPurchaseModal([rec]);
    (document.getElementById('execute-purchase-btn') as HTMLButtonElement).click();
    await flush();
    const opts = (confirmDialog as jest.Mock).mock.calls[0]![0] as { body: HTMLElement };
    const text = opts.body.textContent ?? '';
    expect(text).toContain('Nothing is charged until the approver');
    expect(text).toContain(`Upfront on approval${formatCurrency(rec.upfront_cost, '$', 2)}`);
    expect(text).not.toContain('Charged today');
  });

  test('confirm button names the action and the upfront amount in both modes (#701)', async () => {
    const rec: LocalRecommendation = { ...buildRows()[0]!, provider: 'aws', service: 'ec2', term: 1 };
    (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
    const amount = formatCurrency(rec.upfront_cost, '$', 2);
    const confirmButtonLabel = (): string =>
      ((confirmDialog as jest.Mock).mock.calls[0]![0] as { confirmLabel: string }).confirmLabel;

    (confirmDialog as jest.Mock).mockClear();
    await openPurchaseModal([rec]);
    const modalBtn = document.getElementById('execute-purchase-btn') as HTMLButtonElement;
    const approvalVerb = modalBtn.textContent;
    modalBtn.click();
    await flush();
    expect(confirmButtonLabel()).toBe(`${approvalVerb} (${amount} upfront on approval)`);

    (confirmDialog as jest.Mock).mockClear();
    (document.getElementById('close-purchase-modal-btn') as HTMLButtonElement).click();
    await openPurchaseModal([rec]);
    (document.getElementById('execute-mode-direct') as HTMLInputElement).click();
    const directVerb = (document.getElementById('execute-purchase-btn') as HTMLButtonElement).textContent;
    (document.getElementById('execute-purchase-btn') as HTMLButtonElement).click();
    await flush();
    expect(confirmButtonLabel()).toBe(`${directVerb} (${amount} charged today)`);
  });

  test('the purchase modal title is not the generic "Configure Purchase" (#701)', () => {
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    const html = (require('fs') as typeof import('fs')).readFileSync(require('path').join(__dirname, '..', 'index.html'), 'utf8');
    const title = /<h2 id="purchase-modal-title">([^<]*)<\/h2>/.exec(html)?.[1];
    expect(title).toBe('Review purchase');
  });

  test('direct-execute confirm dialog makes no cancellation promise', async () => {
    const rec: LocalRecommendation = { ...buildRows()[0]!, provider: 'aws', service: 'ec2', term: 1 };
    (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
    (confirmDialog as jest.Mock).mockClear();
    await openPurchaseModal([rec]);
    (document.getElementById('execute-mode-direct') as HTMLInputElement).click();
    (document.getElementById('execute-purchase-btn') as HTMLButtonElement).click();
    await flush();
    expect(confirmDialog).toHaveBeenCalledTimes(1);
    const opts = (confirmDialog as jest.Mock).mock.calls[0]![0] as { title: string; body: HTMLElement };
    const bodyText = opts.body.textContent ?? '';
    expect(bodyText).toMatch(/charges the amount shown under "Charged today" immediately/);
    expect(bodyText).toContain('Charged today');
    expect(bodyText).toContain(formatCurrency(rec.upfront_cost, '$', 2));
    expect(`${opts.title} ${bodyText}`).not.toMatch(/AWS|cancell|24 hours/i);
  });
});

// Issue #331: closing the purchase modal with Escape bypassed purchase-state
// cleanup, so a later single-bucket modal could submit the previous fan-out
// buckets instead of the newly displayed recommendation. modal.ts is mocked
// (see jest.mock('../modal', ...) in purchase-modal-harness.ts), so these tests
// capture the onClose callback openPurchaseModal/openFanOutModal pass to
// openModal() and invoke it directly to simulate what the real modal.ts's
// Escape handler now does on every close path -- modal.test.ts separately
// proves Escape actually invokes that callback via the real keydown handler.
describe('Issue #331: Escape discards purchase state like the explicit close button', () => {
  // Grabs the onClose callback from the most recent openModal(...) call.
  function lastRegisteredOnClose(): () => void {
    const calls = (openModal as jest.Mock).mock.calls;
    const opts = calls[calls.length - 1]![1] as { onClose?: () => void } | undefined;
    expect(opts?.onClose).toBeInstanceOf(Function);
    return opts!.onClose!;
  }

  function fanOutEligibleRows(): LocalRecommendation[] {
    return [
      {
        id: 'fo-ec2', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        region: 'us-east-1', resource_type: 'm5.large', term: 1, payment: 'no-upfront',
        count: 1, upfront_cost: 0, monthly_cost: 100, savings: 50,
      },
      {
        id: 'fo-rds', provider: 'aws', cloud_account_id: 'a1', service: 'rds',
        region: 'us-east-1', resource_type: 'db.r5.large', term: 3, payment: 'all-upfront',
        count: 1, upfront_cost: 1000, monthly_cost: 0, savings: 200,
      },
    ];
  }

  test('Escape on a fan-out modal discards its buckets, so a later single-bucket selection submits only itself', async () => {
    const [ec2Rec, rdsRec] = fanOutEligibleRows();
    (api.getConfig as jest.Mock).mockResolvedValue({ global: { default_payment: 'no-upfront' } });
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {}, recommendations: [ec2Rec, rdsRec], regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([ec2Rec, rdsRec]);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([ec2Rec, rdsRec]);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['fo-ec2', 'fo-rds']));

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await flush();

    // Sanity check: both buckets are live before the simulated Escape.
    expect(getFanOutBuckets()).toHaveLength(2);

    // Simulate the user pressing Escape instead of submitting or clicking
    // the explicit close button.
    lastRegisteredOnClose()();

    expect(getFanOutBuckets()).toBeNull();
    expect(getPurchaseModalRecommendations()).toEqual([]);

    // A different, single-bucket recommendation is now selected and its
    // modal opened -- the exact reproduction from the issue.
    const otherRec: LocalRecommendation = {
      id: 'single-only', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
      region: 'us-west-2', resource_type: 'm6i.large', term: 3, payment: 'all-upfront',
      count: 1, upfront_cost: 4000, monthly_cost: 0, savings: 300,
    };
    await openPurchaseModal([otherRec]);

    (document.getElementById('execute-purchase-btn') as HTMLButtonElement).click();
    await flush();

    // Only the newly displayed recommendation must be submitted -- never
    // the stale fan-out buckets from the modal closed via Escape.
    expect(api.executePurchase).toHaveBeenCalledTimes(1);
    const body = (api.executePurchase as jest.Mock).mock.calls[0]![0] as Array<Record<string, unknown>>;
    expect(body).toHaveLength(1);
    expect(body[0]!['id']).toBe('single-only');
  });

  test('each fan-out modal open gets fresh bucket ids, so results from an earlier open cannot match it (issue #757)', async () => {
    const [ec2Rec, rdsRec] = fanOutEligibleRows();
    (api.getConfig as jest.Mock).mockResolvedValue({ global: { default_payment: 'no-upfront' } });
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {}, recommendations: [ec2Rec, rdsRec], regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([ec2Rec, rdsRec]);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([ec2Rec, rdsRec]);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['fo-ec2', 'fo-rds']));

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await flush();
    const first = getFanOutBuckets()!.map((b) => b.id);
    lastRegisteredOnClose()();

    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await flush();
    const second = getFanOutBuckets()!.map((b) => b.id);

    expect(first).toHaveLength(2);
    expect(second).toHaveLength(2);
    expect(new Set([...first, ...second]).size).toBe(4);
    const rendered = [...document.querySelectorAll<HTMLElement>('section.fanout-bucket')].map((s) => s.dataset['bucketId']);
    expect(rendered).toEqual(second);
  });

  test('Escape on a single-bucket modal discards its selection too (reverse direction)', async () => {
    const staleRec = buildRows()[0]!; // v-3-all
    await openPurchaseModal([staleRec]);
    expect(getPurchaseModalRecommendations()).toHaveLength(1);

    lastRegisteredOnClose()();

    expect(getPurchaseModalRecommendations()).toEqual([]);
    expect(getFanOutBuckets()).toBeNull();
  });
});

// Issue #430: openPurchaseModal and openFanOutModal both await an override
// fetch before rendering. Without a shared open token, an older open that
// resolved last could render over the newer one, or the single-row modal
// could render while fan-out buckets from the other open were still set, so
// Execute submitted buckets the dialog did not show.
describe('Issue #430: interleaved purchase modal opens submit what the newest open displays', () => {
  type Opener = 'single' | 'fanout';
  const cases: Array<[Opener[], Opener[]]> = [
    [['fanout', 'single'], ['fanout', 'single']],
    [['fanout', 'single'], ['single', 'fanout']],
    [['single', 'fanout'], ['single', 'fanout']],
    [['single', 'fanout'], ['fanout', 'single']],
  ];

  test.each(cases)('opens %j resolving %j', async (starts, resolves) => {
    const fanOutRows: LocalRecommendation[] = [
      {
        id: 'fo-ec2', provider: 'aws', cloud_account_id: 'fo-acct', service: 'ec2',
        region: 'us-east-1', resource_type: 'm5.large', term: 1, payment: 'no-upfront',
        count: 1, upfront_cost: 0, monthly_cost: 100, savings: 50,
      },
      {
        id: 'fo-rds', provider: 'aws', cloud_account_id: 'fo-acct', service: 'rds',
        region: 'us-east-1', resource_type: 'db.r5.large', term: 3, payment: 'all-upfront',
        count: 1, upfront_cost: 1000, monthly_cost: 0, savings: 200,
      },
    ];
    const singleRec: LocalRecommendation = {
      id: 'single-only', provider: 'aws', cloud_account_id: 'single-acct', service: 'ec2',
      region: 'us-west-2', resource_type: 'm6i.large', term: 3, payment: 'all-upfront',
      count: 1, upfront_cost: 4000, monthly_cost: 0, savings: 300,
    };
    (api.getConfig as jest.Mock).mockResolvedValue({ global: { default_payment: 'no-upfront' } });
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {}, recommendations: fanOutRows, regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([...fanOutRows, singleRec]);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(fanOutRows);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['fo-ec2', 'fo-rds']));
    await loadRecommendations();

    const fetches: Record<Opener, ReturnType<typeof deferred<Awaited<ReturnType<typeof api.listAccountServiceOverrides>>>>> = {
      single: deferred(),
      fanout: deferred(),
    };
    (api.listAccountServiceOverrides as jest.Mock).mockImplementation((id: string) =>
      (id === 'single-acct' ? fetches.single : fetches.fanout).promise);

    const pending: Array<Promise<void>> = [];
    for (const opener of starts) {
      if (opener === 'single') {
        pending.push(openPurchaseModal([singleRec]));
      } else {
        (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
        await flush();
      }
    }
    // Both opens must be parked on their fetch before either resolves, or
    // the interleaving under test never happens.
    expect(api.listAccountServiceOverrides).toHaveBeenCalledWith('single-acct');
    expect(api.listAccountServiceOverrides).toHaveBeenCalledWith('fo-acct');

    for (const opener of resolves) {
      fetches[opener].resolve([]);
      await flush();
    }
    await Promise.all(pending);

    const newest = starts[starts.length - 1]!;
    expect(document.getElementById('fanout-summary') !== null).toBe(newest === 'fanout');

    (document.getElementById('execute-purchase-btn') as HTMLButtonElement).click();
    await flush();

    const submittedIDs = (api.executePurchase as jest.Mock).mock.calls
      .flatMap(([payload]) => (payload as Array<{ id: string }>).map((rec) => rec.id))
      .sort();
    expect(submittedIDs).toEqual(newest === 'fanout' ? ['fo-ec2', 'fo-rds'] : ['single-only']);
  });
});

// Issue #333: the frontend grouped bulk purchases without account identity,
// so two recommendations sharing (provider, service, term, payment) but
// belonging to different cloud accounts landed in ONE bucket and were sent
// in a single executePurchase POST. internal/purchase/execution.go's
// SingleCloudAccountIDFromRecs -- enforced both at the API boundary
// (validateExecutePurchaseRecommendations, internal/api/handler_purchases_execute.go)
// and by the executor -- rejects a request whose selected recs target more
// than one cloud account, or mix attributed and unattributed recs, with
// HTTP 400 (issue #1902). handleBulkPurchaseClick's bucket key now includes
// cloud_account_id so this frontend/backend contract mismatch can no longer
// occur.
describe('Issue #333: bulk purchase buckets never span more than one cloud account', () => {
  test('two recs on different accounts with identical provider/service/term/payment split into separate fan-out buckets', async () => {
    const rows: LocalRecommendation[] = [
      {
        id: 'acct-1-ec2', provider: 'aws', cloud_account_id: 'account-1', service: 'ec2',
        region: 'us-east-1', resource_type: 'm5.large', term: 3, payment: 'all-upfront',
        count: 2, upfront_cost: 4000, monthly_cost: 0, savings: 900,
      },
      {
        id: 'acct-2-ec2', provider: 'aws', cloud_account_id: 'account-2', service: 'ec2',
        region: 'us-east-1', resource_type: 'm5.large', term: 3, payment: 'all-upfront',
        count: 2, upfront_cost: 4200, monthly_cost: 0, savings: 950,
      },
    ];
    (api.getConfig as jest.Mock).mockResolvedValue({ global: { default_payment: 'all-upfront' } });
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: rows, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(rows);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(rows);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['acct-1-ec2', 'acct-2-ec2']));

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await flush();

    // Pre-#333, this would have collapsed into ONE bucket (same provider/
    // service/term/payment) and opened the single-bucket preview modal
    // instead of the fan-out modal (getFanOutBuckets() would be null).
    const buckets = getFanOutBuckets();
    expect(buckets).not.toBeNull();
    expect(buckets).toHaveLength(2);
    for (const bucket of buckets!) {
      const accountIDs = new Set(bucket.recs.map((r) => r.cloud_account_id));
      expect(accountIDs.size).toBe(1);
    }

    // Submitting must fire one executePurchase POST per account, never a
    // single POST spanning both -- the exact shape the backend's
    // SingleCloudAccountIDFromRecs (internal/purchase/execution.go) rejects
    // with HTTP 400.
    (document.getElementById('execute-purchase-btn') as HTMLButtonElement).click();
    await flush();

    expect(api.executePurchase).toHaveBeenCalledTimes(2);
    const posts = (api.executePurchase as jest.Mock).mock.calls
      .map(([payload]) => payload as Array<{ cloud_account_id?: string }>);
    for (const post of posts) {
      const accountIDs = new Set(post.map((rec) => rec.cloud_account_id));
      expect(accountIDs.size).toBe(1);
    }
    const allAccounts = new Set(posts.flat().map((rec) => rec.cloud_account_id));
    expect(allAccounts).toEqual(new Set(['account-1', 'account-2']));
  });

  test('each fan-out bucket header names its account, so a two-account split is distinguishable', async () => {
    const rows: LocalRecommendation[] = [
      {
        id: 'named', provider: 'aws', cloud_account_id: 'account-1', service: 'ec2',
        region: 'us-east-1', resource_type: 'm5.large', term: 3, payment: 'all-upfront',
        count: 2, upfront_cost: 4000, monthly_cost: 0, savings: 900,
      },
      {
        id: 'unnamed', provider: 'aws', cloud_account_id: 'account-2', service: 'ec2',
        region: 'us-east-1', resource_type: 'm5.large', term: 3, payment: 'all-upfront',
        count: 2, upfront_cost: 4200, monthly_cost: 0, savings: 950,
      },
      {
        id: 'ambient', provider: 'aws', service: 'ec2',
        region: 'us-east-1', resource_type: 'm5.large', term: 3, payment: 'all-upfront',
        count: 2, upfront_cost: 4200, monthly_cost: 0, savings: 950,
      },
    ];
    (api.listAccountsMinimal as jest.Mock).mockResolvedValueOnce([{ id: 'account-1', name: '<b>Prod</b>' }]);
    (api.getConfig as jest.Mock).mockResolvedValue({ global: { default_payment: 'all-upfront' } });
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: rows, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(rows);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(rows);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['named', 'unnamed', 'ambient']));

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await flush();

    const headers = Array.from(document.querySelectorAll<HTMLElement>('.fanout-bucket h4'));
    expect(headers.map((h) => h.textContent)).toEqual(expect.arrayContaining([
      expect.stringContaining('AWS / <b>Prod</b> / ec2'),
      expect.stringContaining('AWS / account-2 / ec2'),
      expect.stringContaining('AWS / Unattributed / ec2'),
    ]));
    expect(headers).toHaveLength(3);
    // The account name is rendered as text, never parsed as markup.
    expect(document.querySelector('.fanout-bucket h4 b')).toBeNull();
  });

  test('an unattributed rec never shares a bucket with an attributed rec of the same shape', async () => {
    // Same provider/service/term/payment, but one rec carries no
    // cloud_account_id at all (the ambient single-account deployment
    // shape) while the other targets a real account. The backend rejects
    // mixing attributed and unattributed recs in one request just as it
    // rejects two distinct accounts.
    const rows: LocalRecommendation[] = [
      {
        id: 'attributed', provider: 'aws', cloud_account_id: 'account-1', service: 'ec2',
        region: 'us-east-1', resource_type: 'm5.large', term: 3, payment: 'all-upfront',
        count: 2, upfront_cost: 4000, monthly_cost: 0, savings: 900,
      },
      {
        id: 'unattributed', provider: 'aws', service: 'ec2',
        region: 'us-east-1', resource_type: 'm5.large', term: 3, payment: 'all-upfront',
        count: 2, upfront_cost: 4000, monthly_cost: 0, savings: 900,
      },
    ];
    (api.getConfig as jest.Mock).mockResolvedValue({ global: { default_payment: 'all-upfront' } });
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: rows, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(rows);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(rows);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['attributed', 'unattributed']));

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await flush();

    const buckets = getFanOutBuckets();
    expect(buckets).not.toBeNull();
    expect(buckets).toHaveLength(2);
    const attributedBucket = buckets!.find((b) => b.recs[0]!.cloud_account_id === 'account-1')!;
    const unattributedBucket = buckets!.find((b) => !b.recs[0]!.cloud_account_id)!;
    expect(attributedBucket.recs).toHaveLength(1);
    expect(unattributedBucket.recs).toHaveLength(1);
  });
});

describe('Issue #711: purchase screens state the monthly charge and the term total', () => {
  // v-1-no: 1 year, no upfront, $800 a month, so the term costs $9,600.
  const noUpfront = (): LocalRecommendation => buildRows()[3]!;

  test('the single purchase modal shows upfront, monthly and total over term for the checked rows', async () => {
    await openPurchaseModal([noUpfront()]);
    const band = document.getElementById('purchase-modal-commitment-band')!;
    expect(band.textContent).toBe('Upfront$0.00Monthly$800.00Total over term$9,600.00');

    (document.querySelector('.purchase-modal-row-include') as HTMLInputElement).click();
    expect(band.textContent).toBe('Upfront$0.00Monthly$0.00Total over term$0.00');
  });

  test('the Execute Now warning names the monthly charge of a no-upfront purchase', async () => {
    await openPurchaseModal([noUpfront()]);
    (document.getElementById('execute-mode-direct') as HTMLInputElement).click();
    expect(document.querySelector<HTMLElement>('.direct-execute-warning')!.textContent).toBe(
      'Warning: This will charge $0.00 upfront immediately, then $800.00 a month for the term ($9,600.00 in total). This bypasses the approval step.',
    );
  });

  test('the Execute Now warning says monthly fees apply when the provider did not report them', async () => {
    await openPurchaseModal([{ ...noUpfront(), monthly_cost: null }]);
    (document.getElementById('execute-mode-direct') as HTMLInputElement).click();
    expect(document.querySelector<HTMLElement>('.direct-execute-warning')!.textContent).toContain(
      'upfront immediately, plus monthly fees the provider did not report.',
    );
  });

  test('the bulk summary, bucket totals and confirm state the monthly charge and term total', async () => {
    const rows: LocalRecommendation[] = [
      {
        id: 'ec2-no', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        region: 'us-east-1', resource_type: 'm5.large', term: 1, payment: 'no-upfront',
        count: 1, upfront_cost: 0, monthly_cost: 100, savings: 50,
      },
      {
        id: 'rds-no', provider: 'aws', cloud_account_id: 'a1', service: 'rds',
        region: 'us-east-1', resource_type: 'db.r5.large', term: 1, payment: 'no-upfront',
        count: 1, upfront_cost: 0, monthly_cost: 200, savings: 80,
      },
    ];
    (api.getConfig as jest.Mock).mockResolvedValue({ global: { default_payment: 'no-upfront' } });
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: rows, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(rows);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(rows);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['ec2-no', 'rds-no']));

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await flush();

    const line = (label: string): string | null | undefined => Array.from(document.querySelectorAll('#fanout-summary p'))
      .find((p) => p.textContent?.startsWith(label))?.querySelector('strong')?.textContent;
    expect(line('Total upfront')).toBe('$0');
    expect(line('Monthly recurring')).toBe('$300');
    expect(line('Total over term')).toBe('$3,600');
    const bucketTotals = Array.from(document.querySelectorAll('.fanout-bucket-totals')).map((p) => p.textContent);
    expect(bucketTotals).toEqual(expect.arrayContaining([
      expect.stringContaining('$0 upfront · $100/mo recurring'),
      expect.stringContaining('$0 upfront · $200/mo recurring'),
    ]));

    (confirmDialog as jest.Mock).mockClear();
    (document.getElementById('execute-purchase-btn') as HTMLButtonElement).click();
    await flush();
    const opts = (confirmDialog as jest.Mock).mock.calls[0]![0] as { body: HTMLElement };
    const text = opts.body.textContent ?? '';
    expect(text).toContain('Upfront on approval$0.00');
    expect(text).toContain('Monthly$300.00');
    expect(text).toContain('Total over term$3,600.00');
  });
});

// Issue #1959: Savings Plans rows carry count 1, so the count cannot be
// scaled. They go through unscaled with capacity_percent and the backend sizes
// the hourly commitment (floor to whole cents, minimum $0.01/hr).
describe('Issue #1959: Savings Plans are sized by hourly commitment, not count', () => {
  const sp = (id: string, service: string, hourly = 1): LocalRecommendation => ({
    id, provider: service === 'savingsplans' ? 'azure' : 'aws', service,
    region: 'us-east-1', resource_type: 'ComputeSavingsPlans', term: 1, payment: 'all-upfront',
    count: 1, upfront_cost: 1000, monthly_cost: 0, savings: 100,
    details: { plan_type: 'Compute', hourly_commitment: hourly, offering_id: 'off-1' },
  });
  const ec2: LocalRecommendation = {
    id: 'ec2-1', provider: 'aws', service: 'ec2', region: 'us-east-1', resource_type: 'm5.large',
    term: 1, payment: 'all-upfront', count: 4, upfront_cost: 4000, monthly_cost: 0, savings: 900,
  };

  async function clickBulk(rows: LocalRecommendation[], capacity: number): Promise<void> {
    (localStorage.getItem as jest.Mock).mockReturnValue(JSON.stringify({ capacity }));
    (api.getConfig as jest.Mock).mockResolvedValue({ global: { default_payment: 'all-upfront' } });
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: rows, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(rows);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(rows);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(rows.map((r) => r.id)));
    await loadRecommendations();
    (showToast as jest.Mock).mockClear();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await flush();
  }

  function dropToast(): { message: string; timeout?: number | null } | undefined {
    return (showToast as jest.Mock).mock.calls.map((c) => c[0] as { message: string; timeout?: number | null })
      .find((o) => o.message.includes('Savings Plans rows left out'));
  }

  async function submitSingleBucket(): Promise<Array<Record<string, unknown>>> {
    (document.getElementById('execute-purchase-btn') as HTMLButtonElement).click();
    await flush();
    expect(api.executePurchase).toHaveBeenCalledTimes(1);
    return (api.executePurchase as jest.Mock).mock.calls[0]![0] as Array<Record<string, unknown>>;
  }

  test.each(['savings-plans-compute', 'savings-plans-ec2instance', 'savings-plans-sagemaker', 'savings-plans-database'])(
    '%s at 50%% is not refused: Count stays 1, capacity_percent 50, the modal shows the sized hourly',
    async (service) => {
      await clickBulk([sp('sp-1', service)], 50);
      expect(dropToast()).toBeUndefined();
      const rows = getPurchaseModalRecommendations();
      expect(rows.map((r) => [r.id, r.count])).toEqual([['sp-1', 1]]);
      expect(rows[0]!.upfront_cost).toBe(500);
      expect(document.querySelector('.purchase-modal-sp-hourly')!.textContent)
        .toBe('Savings Plans hourly commitment: $0.50/hr (50% of $1.00/hr recommended)');
      const body = await submitSingleBucket();
      expect(body[0]).toMatchObject({ id: 'sp-1', count: 1, recommended_count: 1 });
      expect((api.executePurchase as jest.Mock).mock.calls[0]![1]).toBe(50);
    },
  );

  test('the sized hourly floors to whole cents like the backend (0.58/hr at 50% is 0.29/hr)', async () => {
    await clickBulk([sp('sp-1', 'savings-plans-compute', 0.58)], 50);
    expect(document.querySelector('.purchase-modal-sp-hourly')!.textContent)
      .toBe('Savings Plans hourly commitment: $0.29/hr (50% of $0.58/hr recommended)');
  });

  test('SP plus RI rows at 50%: SP keeps Count 1, the RI count is scaled, the fan-out shows the sized hourly', async () => {
    await clickBulk([sp('sp-1', 'savings-plans-compute'), ec2], 50);
    expect(dropToast()).toBeUndefined();
    const buckets = getFanOutBuckets()!;
    const byService = Object.fromEntries(buckets.map((b) => [b.service, b.recs]));
    expect(byService['savings-plans']!.map((r) => r.count)).toEqual([1]);
    expect(byService['ec2']!.map((r) => r.count)).toEqual([2]);
    expect(document.querySelector('.fanout-bucket-sp-hourly')!.textContent)
      .toBe('Savings Plans hourly commitment: $0.50/hr (50% of $1.00/hr recommended)');
  });

  test('a Savings Plans row below the $0.01/hr minimum is dropped with a sticky warning; the RI row still goes', async () => {
    const tiny = { ...sp('sp-tiny', 'savings-plans-compute', 0.01), resource_type: 'TinyPlan' };
    await clickBulk([tiny, ec2], 50);
    const toast = dropToast()!;
    expect(toast.message).toContain('TinyPlan');
    expect(toast.message).toContain('$0.01/hr minimum');
    expect(toast.timeout).toBeNull();
    expect(getPurchaseModalRecommendations().map((r) => r.id)).toEqual(['ec2-1']);
    const body = await submitSingleBucket();
    expect(body.map((r) => r['id'])).toEqual(['ec2-1']);
  });

  test('only below-minimum Savings Plans rows: warning, nothing opens, nothing is posted', async () => {
    await clickBulk([sp('sp-tiny', 'savings-plans-compute', 0.01)], 50);
    expect(dropToast()).toBeDefined();
    expect((showToast as jest.Mock)).toHaveBeenCalledTimes(1);
    expect(getPurchaseModalRecommendations()).toHaveLength(0);
    expect(getFanOutBuckets()).toBeNull();
    expect(api.executePurchase).not.toHaveBeenCalled();
  });

  test('a Savings Plans row with no hourly commitment to size is dropped and named, never sent', async () => {
    const noDetails = { ...sp('sp-x', 'savings-plans-compute'), details: undefined };
    await clickBulk([noDetails, ec2], 50);
    expect(dropToast()!.message).toContain('savings-plans-compute ComputeSavingsPlans us-east-1');
    expect(getPurchaseModalRecommendations().map((r) => r.id)).toEqual(['ec2-1']);
  });

  test('capacity 100 opens the modal with the Savings Plans row unchanged', async () => {
    await clickBulk([sp('sp-1', 'savings-plans-compute')], 100);
    expect(dropToast()).toBeUndefined();
    expect(getPurchaseModalRecommendations().map((r) => [r.id, r.count, r.upfront_cost])).toEqual([['sp-1', 1, 1000]]);
  });

  test('non-SP rows at 50% are scaled as before', async () => {
    await clickBulk([ec2], 50);
    expect(showToast).not.toHaveBeenCalled();
    expect(getPurchaseModalRecommendations().map((r) => r.count)).toEqual([2]);
  });

  test('scaleRecForCapacity never scales the SP count (it only sizes the money fields)', () => {
    const row = sp('sp-1', 'savings-plans-compute');
    expect(scaleRecForCapacity(row, 100)).toMatchObject({ count: 1, recommended_count: 1 });
    expect(scaleRecForCapacity(row, 50)).toMatchObject({ count: 1, recommended_count: 1, upfront_cost: 500, savings: 50 });
    expect(scaleRecForCapacity(sp('sp-t', 'savings-plans-compute', 0.01), 50)).toBeNull();
  });

  test('storage says 100 but the input says 50: the POST still carries recommended_count', async () => {
    await clickBulk([sp('sp-1', 'savings-plans-compute')], 100);
    (document.getElementById('bulk-purchase-capacity') as HTMLInputElement).value = '50';
    const body = await submitSingleBucket();
    expect((body[0] as { recommended_count?: number }).recommended_count).toBe(1);
  });

  test('the drop warning lists at most 5 rows then +M more, naming the account', async () => {
    const rows = Array.from({ length: 7 }, (_, i) => ({
      ...sp(`sp-${i}`, 'savings-plans-compute', 0.01), resource_type: `Plan${i}`, cloud_account_id: 'acct-9',
    }));
    await clickBulk(rows, 50);
    const msg = dropToast()!.message;
    expect(msg).toContain('Plan4 us-east-1 (acct-9)');
    expect(msg).not.toContain('Plan5');
    expect(msg).toContain(' +2 more.');
  });

  test('a repeated warning and a passing click dismiss the previous warning toast', async () => {
    const rows = [sp('sp-1', 'savings-plans-compute', 0.01)];
    const first = { dismiss: jest.fn() };
    const second = { dismiss: jest.fn() };
    (showToast as jest.Mock).mockReturnValueOnce(first).mockReturnValueOnce(second);
    await clickBulk(rows, 50);
    expect(first.dismiss).not.toHaveBeenCalled();
    (localStorage.getItem as jest.Mock).mockReturnValue(JSON.stringify({ capacity: 50 }));
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    expect(first.dismiss).toHaveBeenCalledTimes(1);
    expect(second.dismiss).not.toHaveBeenCalled();
    (localStorage.getItem as jest.Mock).mockReturnValue(JSON.stringify({ capacity: 100 }));
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    expect(second.dismiss).toHaveBeenCalledTimes(1);
  });

  test('the real toast renders the message as text, not markup', () => {
    const { showToast: realToast } = jest.requireActual('../toast') as typeof import('../toast');
    realToast({ message: '<img src=x onerror=alert(1)> savings-plans-compute', kind: 'warning', timeout: null });
    expect(document.querySelector('img')).toBeNull();
    expect(document.body.textContent).toContain('<img src=x');
  });
});
