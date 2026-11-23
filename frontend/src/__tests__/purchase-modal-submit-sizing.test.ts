/**
 * Purchase-modal submit regressions on the amounts shown and the Savings Plans
 * sizing: the monthly charge and term total on the purchase screens (#711) and
 * Savings Plans rows sized by hourly commitment instead of count (#1959).
 *
 * Split out of purchase-modal-submit.test.ts (#1965). Shared mocks, fixtures and
 * the per-test reset live in purchase-modal-harness.ts.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  buildRows,
  flush,
  setupPurchaseModalTest,
} from './purchase-modal-harness';
import * as api from '../api';
import * as state from '../state';
import { confirmDialog } from '../confirmDialog';
import {
  openPurchaseModal,
  getPurchaseModalRecommendations,
  getFanOutBuckets,
  loadRecommendations,
  scaleRecForCapacity,
} from '../recommendations';
import { showToast } from '../toast';
import type { LocalRecommendation } from '../types';

beforeEach(setupPurchaseModalTest);

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
