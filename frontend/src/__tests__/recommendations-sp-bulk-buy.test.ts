/**
 * Recommendations module tests: per-row Payment seed tail and SP and Azure SP bulk-buy bucketing.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import {
  loadRecommendations,
  openPurchaseModal,
  getPurchaseModalRecommendations,
} from '../recommendations';
import * as api from '../api';
import * as state from '../state';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

// Issue #111 (iii): per-row Payment seed in openPurchaseModal — the
// single-bucket / single-rec purchase modal renders editable Term and
// Payment dropdowns. Each row's defaults walk the precedence
// override → rec.payment → paymentOptionsFor[0]. Edits mutate
// currentPurchaseRecommendations[idx] in place; getPurchaseModalRecommendations()
// returns the user's choices; app.ts::handleExecutePurchase posts
// `r.payment` per rec (no longer hardcoded 'all-upfront').
describe('Issue #111 (iii): per-row Payment seed in openPurchaseModal', () => {
  beforeEach(() => {
    document.body.replaceChildren();
    const purchaseModal = document.createElement('div');
    purchaseModal.id = 'purchase-modal';
    purchaseModal.className = 'hidden';
    const purchaseDetails = document.createElement('div');
    purchaseDetails.id = 'purchase-details';
    purchaseModal.appendChild(purchaseDetails);
    document.body.appendChild(purchaseModal);
    jest.clearAllMocks();
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);
    // Issue #1903: pricedCellVariant/cellTermOptions/cellPaymentOptions read
    // state.getRecommendations() for the loaded sibling (term, payment) rows.
    // Reset to empty here so per-test mockReturnValue overrides below don't
    // leak into sibling describe blocks (clearAllMocks does not reset return
    // values — see the note above at the #288 execute-mode-toggle suite).
    (state.getRecommendations as jest.Mock).mockReturnValue([]);
  });

  test('(e) user changes Payment dropdown → live state reflects new value (and would round-trip via handleExecutePurchase)', async () => {
    const rec = {
      id: 'rec-5', provider: 'aws' as const, cloud_account_id: 'test-account-a',
      service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
      count: 5, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500,
    };
    // Issue #1903: the no-upfront option must be a loaded (priced) variant.
    (state.getRecommendations as jest.Mock).mockReturnValue([
      rec,
      { ...rec, id: 'rec-5-no', payment: 'no-upfront', upfront_cost: 0, monthly_cost: 60 },
    ]);

    await openPurchaseModal([rec]);

    const paymentSelect = document.querySelector<HTMLSelectElement>('.purchase-row-payment');
    expect(paymentSelect).not.toBeNull();

    // Change to 'no-upfront' (always supported for AWS EC2 1yr).
    paymentSelect!.value = 'no-upfront';
    paymentSelect!.dispatchEvent(new Event('change'));

    const live = getPurchaseModalRecommendations();
    expect(live[0]!.payment).toBe('no-upfront');
    expect(live[0]!.upfront_cost).toBe(0);
    expect(live[0]!.monthly_cost).toBe(60);

    // The mapping in app.ts::handleExecutePurchase reads this value
    // verbatim (`payment: r.payment ?? 'all-upfront'`), so a downstream
    // assertion that the API call carries 'no-upfront' is implicit in
    // (a) the live state above and (b) the source-of-truth read at
    // app.ts:289-303. No separate mock-call assertion needed here —
    // the integration of that mapping is exercised by app.ts'
    // existing handleExecutePurchase tests.
  });

  test('(f) Issue #1402: Term select options display full labels "1 Year" / "3 Years", not abbreviated "1yr" / "3yr"', async () => {
    const rec = {
      id: 'rec-6', provider: 'aws' as const, cloud_account_id: 'test-account-a',
      service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
      count: 2, term: 1, payment: 'all-upfront', savings: 50, upfront_cost: 200,
    };
    // Issue #1903: a 3yr option is only offered when a 3yr variant was
    // actually loaded for this cell.
    (state.getRecommendations as jest.Mock).mockReturnValue([
      rec,
      { ...rec, id: 'rec-6-3yr', term: 3 },
    ]);

    await openPurchaseModal([rec]);

    const termSelect = document.querySelector<HTMLSelectElement>('.purchase-row-term');
    expect(termSelect).not.toBeNull();
    const labels = Array.from(termSelect!.options).map((o) => o.text);
    expect(labels).toContain('1 Year');
    expect(labels).toContain('3 Years');
    expect(labels).not.toContain('1yr');
    expect(labels).not.toContain('3yr');
  });
});

// Issue #132: pre-PR-#123 a 'savings-plans' Compute SP rec and a
// 'savings-plans' SageMaker SP rec landed in the same bulk-buy
// bucket. PR #123 split them into per-plan-type service slugs, which
// silently fanned out into N separate buckets and N separate approval
// emails. This restores the one-bucket experience.
describe('Issue #132: bulk-buy collapses SP plan types into one bucket', () => {
  beforeEach(async () => {
    document.body.replaceChildren();
    const recsTab = document.createElement('div');
    recsTab.id = 'opportunities-tab';
    recsTab.className = 'tab-content active';
    const summary = document.createElement('div');
    summary.id = 'recommendations-summary';
    const list = document.createElement('div');
    list.id = 'recommendations-list';
    recsTab.appendChild(summary);
    recsTab.appendChild(list);
    document.body.appendChild(recsTab);
    const purchaseModal = document.createElement('div');
    purchaseModal.id = 'purchase-modal';
    purchaseModal.className = 'hidden';
    const purchaseDetails = document.createElement('div');
    purchaseDetails.id = 'purchase-details';
    purchaseModal.appendChild(purchaseDetails);
    document.body.appendChild(purchaseModal);
    jest.clearAllMocks();
    // Default: empty overrides — issue #132's bucket-key collapse is
    // independent of the issue #111 override-seed path, but
    // openFanOutModal still calls listAccountServiceOverrides per
    // single-account bucket, so we keep the mock primed.
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);
    // Module-level FanOut state survives test isolation; reset it so
    // a previous test's openFanOutModal call doesn't leak into a
    // single-bucket-happy-path assertion here.
    const { clearFanOutBuckets, clearPurchaseModalRecommendations } = await import('../recommendations');
    clearFanOutBuckets();
    clearPurchaseModalRecommendations();
  });

  test('compute + sagemaker SPs at term=1 share a single bucket (happy path)', async () => {
    const recs = [
      { id: 's1', provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-compute',   resource_type: 'sp', region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500 },
      { id: 's2', provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-sagemaker', resource_type: 'sp', region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront', savings: 200, upfront_cost: 800 },
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(recs.map((r) => r.id as string)));

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();

    const { getFanOutBuckets, getPurchaseModalRecommendations } = await import('../recommendations');
    // 1 collapsed bucket → openPurchaseModal happy path, no fan-out.
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    expect(getFanOutBuckets()).toBeNull();
    // The single-bucket modal carries BOTH SPs (proves they collapsed).
    const modalRecs = getPurchaseModalRecommendations();
    expect(modalRecs.map((r) => r.id).sort()).toEqual(['s1', 's2']);
    expect(modalRecs.map((r) => r.service).sort()).toEqual([
      'savings-plans-compute',
      'savings-plans-sagemaker',
    ]);
  });

  test('SP plan types + a non-SP rec produce one SP bucket + one EC2 bucket', async () => {
    const recs = [
      { id: 's1', provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-compute',     resource_type: 'sp', region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500 },
      { id: 's2', provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-sagemaker',   resource_type: 'sp', region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront', savings: 150, upfront_cost: 600 },
      { id: 's3', provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-ec2instance', resource_type: 'sp', region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront', savings: 200, upfront_cost: 800 },
      { id: 'e1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',                       resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront', savings:  50, upfront_cost: 300 },
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(recs.map((r) => r.id as string)));

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    // fetchOverridesForAccounts is a separate async function (one extra
    // microtask boundary vs the previous inline Promise.all); four ticks
    // are needed for openFanOutModal to finish populating the DOM.
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); await Promise.resolve();

    const { getFanOutBuckets } = await import('../recommendations');
    const buckets = getFanOutBuckets();
    expect(buckets).not.toBeNull();
    // Expect 2 buckets total: 1 collapsed SP bucket (3 recs) + 1 EC2 bucket (1 rec).
    expect(buckets!.length).toBe(2);

    const spBucket = buckets!.find((b) => b.service === 'savings-plans');
    expect(spBucket).toBeDefined();
    expect(spBucket!.recs).toHaveLength(3);
    // Per-rec services are preserved — backend audit/suppression keeps
    // the real plan type per rec.
    expect(spBucket!.recs.map((r) => r.service).sort()).toEqual([
      'savings-plans-compute',
      'savings-plans-ec2instance',
      'savings-plans-sagemaker',
    ]);

    const ec2Bucket = buckets!.find((b) => b.service === 'ec2');
    expect(ec2Bucket).toBeDefined();
    expect(ec2Bucket!.recs).toHaveLength(1);
  });

  test('SP recs at different terms still split by term', async () => {
    const recs = [
      { id: 's1', provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-compute',   resource_type: 'sp', region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500 },
      { id: 's2', provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-sagemaker', resource_type: 'sp', region: 'us-east-1', count: 1, term: 3, payment: 'all-upfront', savings: 200, upfront_cost: 800 },
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(recs.map((r) => r.id as string)));

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();

    const { getFanOutBuckets } = await import('../recommendations');
    const buckets = getFanOutBuckets();
    expect(buckets).not.toBeNull();
    // Different terms must still fan out — SP collapsing only joins
    // recs that share (provider, term).
    expect(buckets!.length).toBe(2);
    expect(buckets!.every((b) => b.service === 'savings-plans')).toBe(true);
    expect(buckets!.map((b) => b.term).sort()).toEqual([1, 3]);
  });

  test('mixed-SP bucket renders combined plan-type label', async () => {
    const recs = [
      { id: 's1', provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-compute',   resource_type: 'sp', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
      { id: 's2', provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-sagemaker', resource_type: 'sp', region: 'us-east-1', count: 1, term: 1, savings: 200, upfront_cost: 800 },
      { id: 'e1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',                     resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings:  50, upfront_cost: 300 },
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(recs.map((r) => r.id as string)));

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    // fetchOverridesForAccounts is a separate async function (one extra
    // microtask boundary vs the previous inline Promise.all); four ticks
    // are needed for openFanOutModal to finish populating the DOM.
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); await Promise.resolve();

    // The fan-out modal renders one section per bucket; the SP section
    // title carries the combined plan-type label.
    const sectionTitles = Array.from(
      document.querySelectorAll('.fanout-bucket h4'),
    ).map((el) => el.textContent || '');
    expect(sectionTitles.some((t) => t.includes('Savings Plans (Compute + SageMaker)'))).toBe(true);
    // Non-SP bucket title still uses the raw service slug.
    expect(sectionTitles.some((t) => t.includes('AWS / a1 / ec2'))).toBe(true);
  });
});
