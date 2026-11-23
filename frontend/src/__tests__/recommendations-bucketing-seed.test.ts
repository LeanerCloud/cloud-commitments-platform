/**
 * Recommendations module tests: term-aware bucketing and the per-bucket Payment seed.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, mockShowToast } from './recommendations-harness';
import { loadRecommendations, openPurchaseModal } from '../recommendations';
import { closeModal } from '../modal';
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

// Issue #111: per-bucket Payment dropdown in the fan-out modal,
// seeded from the per-account service override when all recs in a
// bucket share one cloud_account_id and that account has a saved
// override matching the bucket's (provider, service). Otherwise
// (multi-account, no override, override has no payment, override
// payment unsupported by the (provider, service, term) cell) the
// bucket falls back to the toolbar payment.
describe('Issue #111: per-bucket Payment seed from per-account service override', () => {
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
    // Default: empty overrides — overridden per-test.
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);
    // Module-level fan-out state survives test isolation; reset it so a
    // previous test's openFanOutModal call does not leak into tests that
    // assert the single-bucket happy path (getFanOutBuckets() === null).
    const { clearFanOutBuckets } = await import('../recommendations');
    clearFanOutBuckets();
  });

  // Force a multi-bucket fan-out by mixing terms (1yr + 3yr both
  // present); this drives openFanOutModal rather than the
  // single-bucket happy path. Each test seeds overrides differently
  // to exercise the (override / no-override / multi-account / edit)
  // matrix.
  const setupMixedTermRecs = (recs: Array<Record<string, unknown>>): void => {
    const loadedRecs = recs.flatMap((rec) => {
      if (rec['provider'] !== 'aws' || rec['service'] !== 'ec2') return [rec];
      const payment = rec['payment'] ?? 'all-upfront';
      const variants = [
        { ...rec, id: `${String(rec['id'])}-partial`, payment: 'partial-upfront' },
        { ...rec, id: `${String(rec['id'])}-no`, payment: 'no-upfront' },
      ];
      return [{ ...rec, payment }, ...variants];
    });
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(loadedRecs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(loadedRecs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    // #273: action buttons now require an explicit selection. Each fan-out
    // test in this suite is asserting bucket assembly, not the selection-
    // gating UI, so seed the selection with every rec id to keep Purchase
    // enabled and the click effective. The earlier
    // "no-selection → default to visible" fallback path is gone.
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(
      new Set(recs.map((r) => r.id as string)),
    );
  };

  test('failed override fetch: exactly one warning toast, toolbar-seeded buckets (#1957)', async () => {
    const recs = [
      { id: 'a', provider: 'aws', cloud_account_id: 'test-account-a', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
      { id: 'b', provider: 'aws', cloud_account_id: 'test-account-b', service: 'ec2', resource_type: 'm5.large', region: 'us-east-1', count: 1, term: 3, savings: 200, upfront_cost: 800 },
    ];
    setupMixedTermRecs(recs);
    (api.listAccountServiceOverrides as jest.Mock).mockRejectedValue(new Error('boom'));
    mockShowToast.mockClear();

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();

    const { getFanOutBuckets } = await import('../recommendations');
    const buckets = getFanOutBuckets();
    expect(buckets).not.toBeNull();
    for (const b of buckets!) expect(b.paymentSource).toBe('toolbar');
    const warnings = mockShowToast.mock.calls.filter(
      ([o]) => String((o as { message?: string }).message).startsWith('Account payment overrides could not be loaded for 2 account(s) (test-account-a, test-account-b)'),
    );
    expect(warnings).toHaveLength(1);
  });

  test('superseded fan-out open with a failed override fetch shows no toast (#1957)', async () => {
    const recs = [
      { id: 'a', provider: 'aws', cloud_account_id: 'test-account-a', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
      { id: 'b', provider: 'aws', cloud_account_id: 'test-account-a', service: 'ec2', resource_type: 'm5.large', region: 'us-east-1', count: 1, term: 3, savings: 200, upfront_cost: 800 },
    ];
    setupMixedTermRecs(recs);
    let rejectFirst!: (e: Error) => void;
    (api.listAccountServiceOverrides as jest.Mock)
      .mockImplementationOnce(() => new Promise((_, rej) => { rejectFirst = rej; }))
      .mockResolvedValue([]);
    mockShowToast.mockClear();

    await loadRecommendations();
    const btn = document.getElementById('bulk-purchase-btn') as HTMLButtonElement;
    btn.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(api.listAccountServiceOverrides).toHaveBeenCalledTimes(1);
    // A newer open (shared token) supersedes the pending fan-out open.
    await openPurchaseModal([recs[0] as never]);
    rejectFirst(new Error('late'));
    await new Promise((r) => setTimeout(r, 0));

    expect(mockShowToast.mock.calls.filter(
      ([o]) => String((o as { message?: string }).message).includes('overrides could not be loaded'),
    )).toHaveLength(0);
  });

  test('closing the fan-out modal dismisses the warning toast (#1957)', async () => {
    const recs = [
      { id: 'a', provider: 'aws', cloud_account_id: 'test-account-a', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
      { id: 'b', provider: 'aws', cloud_account_id: 'test-account-a', service: 'ec2', resource_type: 'm5.large', region: 'us-east-1', count: 1, term: 3, savings: 200, upfront_cost: 800 },
    ];
    setupMixedTermRecs(recs);
    (api.listAccountServiceOverrides as jest.Mock).mockRejectedValue(new Error('boom'));
    mockShowToast.mockClear();
    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await new Promise((r) => setTimeout(r, 0));
    const { getFanOutBuckets } = await import('../recommendations');
    expect(getFanOutBuckets()).not.toBeNull();
    const call = mockShowToast.mock.calls.findIndex(
      ([o]) => String((o as { message?: string }).message).includes('overrides could not be loaded'),
    );
    expect(call).toBeGreaterThanOrEqual(0);
    const dismiss = mockShowToast.mock.results[call]!.value.dismiss as jest.Mock;
    expect(dismiss).not.toHaveBeenCalled();
    closeModal(document.getElementById('purchase-modal') as HTMLElement);
    expect(dismiss).toHaveBeenCalledTimes(1);
  });

  test('no override warning toast when every override fetch succeeds (#1957)', async () => {
    const recs = [
      { id: 'a', provider: 'aws', cloud_account_id: 'test-account-a', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
      { id: 'b', provider: 'aws', cloud_account_id: 'test-account-a', service: 'ec2', resource_type: 'm5.large', region: 'us-east-1', count: 1, term: 3, savings: 200, upfront_cost: 800 },
    ];
    setupMixedTermRecs(recs);
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);
    mockShowToast.mockClear();

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();

    expect(mockShowToast.mock.calls.filter(
      ([o]) => String((o as { message?: string }).message).includes('overrides could not be loaded'),
    )).toHaveLength(0);
  });

  test('(a) single-account bucket with matching override → bucket payment seeded from override', async () => {
    // issues #225/#226: resolvePurchaseTarget uses pickBestVariantPerCell for the
    // default path. Use different resource_type values so each rec is its own cell,
    // ensuring both appear in the default target and trigger fan-out.
    const recs = [
      { id: 'a', provider: 'aws', cloud_account_id: 'test-account-a', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
      { id: 'b', provider: 'aws', cloud_account_id: 'test-account-a', service: 'ec2', resource_type: 'm5.large', region: 'us-east-1', count: 1, term: 3, savings: 200, upfront_cost: 800 },
    ];
    setupMixedTermRecs(recs);
    // Override pins payment=partial-upfront for AWS EC2 on this
    // account. partial-upfront is supported for both 1yr and 3yr.
    (api.listAccountServiceOverrides as jest.Mock).mockImplementation(async (id: string) => {
      if (id === 'test-account-a') {
        return [{ id: 'ovr-1', account_id: 'test-account-a', provider: 'aws', service: 'ec2', payment: 'partial-upfront' }];
      }
      return [];
    });

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();

    const { getFanOutBuckets } = await import('../recommendations');
    const buckets = getFanOutBuckets();
    expect(buckets).not.toBeNull();
    expect(buckets!.length).toBe(2);
    for (const b of buckets!) {
      expect(b.payment).toBe('partial-upfront');
      expect(b.paymentSource).toBe('override');
    }
    // Source-note span renders.
    const noteSpans = document.querySelectorAll('.fanout-bucket-payment-source');
    expect(noteSpans.length).toBe(2);
    expect((noteSpans[0] as HTMLElement).textContent).toContain('account override');
    // Dropdown's selected value matches.
    const selects = document.querySelectorAll<HTMLSelectElement>('.fanout-bucket-payment');
    expect(selects.length).toBe(2);
    expect(selects[0]!.value).toBe('partial-upfront');
  });
});
