/**
 * Recommendations module tests: term-aware bucketing and the per-bucket Payment seed.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import { loadRecommendations } from '../recommendations';
import * as api from '../api';
import * as state from '../state';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

describe('Bundle B: term-aware bucketing in the Purchase flow', () => {
  beforeEach(() => {
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
  });

  test('multi-term selection produces multiple fan-out buckets', async () => {
    // Two different resource types (different cells) with different terms.
    // #273: resolvePurchaseTarget no longer falls back to "all visible" when
    // nothing is selected — the buttons are disabled in that state. Tests
    // that exercise the post-Purchase fan-out must explicitly select the
    // recs they want included in the target.
    const mixed = [
      { id: 'a', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500 },
      { id: 'b', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 'm5.large', region: 'us-east-1', count: 1, term: 3, payment: 'all-upfront', savings: 200, upfront_cost: 800 },
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: mixed, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(mixed);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(mixed);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['a', 'b']));

    await loadRecommendations();
    const purchaseBtn = document.getElementById('bulk-purchase-btn') as HTMLButtonElement;
    purchaseBtn.click();
    // openFanOutModal is async (issue #111: pre-fetches per-account
    // overrides). Yield twice so the awaits inside resolve before
    // we read getFanOutBuckets().
    await Promise.resolve();
    await Promise.resolve();

    const { getFanOutBuckets } = await import('../recommendations');
    const buckets = getFanOutBuckets();
    expect(buckets).not.toBeNull();
    expect(buckets!.length).toBe(2);
    expect(buckets!.map((b) => b.term).sort()).toEqual([1, 3]);
    // Each bucket carries the rec's own term, not a toolbar override.
    const b0 = buckets![0]!;
    const b1 = buckets![1]!;
    expect(b0.recs.every((r) => r.term === b0.term)).toBe(true);
    expect(b1.recs.every((r) => r.term === b1.term)).toBe(true);
  });

  it('keeps a single monthly GCP rec on the single-bucket modal (#1131)', async () => {
    const gcp = [
      { id: 'g', provider: 'gcp', cloud_account_id: 'g1', service: 'gce', resource_type: 'n2-standard-2', region: 'us-central1', count: 1, term: 1, payment: 'monthly', savings: 100, upfront_cost: 0 },
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: gcp, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(gcp);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(gcp);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['g']));

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await Promise.resolve();
    await Promise.resolve();

    const { getFanOutBuckets } = await import('../recommendations');
    // GCP no longer supports the all-upfront toolbar default; the rec's own
    // monthly payment must keep it off the fan-out path.
    expect(getFanOutBuckets()).toBeNull();
  });
});
