/**
 * Recommendations module tests: per-bucket and per-row Payment seed tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, mockShowToast } from './recommendations-harness';
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

  // CR finding on PR #710: account override carrying Azure's 'upfront' synonym
  // must be normalized before seeding the bucket, so the fan-out dropdown
  // receives 'all-upfront' (not the raw 'upfront') and paymentSource is
  // 'override', not 'toolbar'.
  test('(f) account override with upfront (Azure synonym) normalizes to all-upfront with source override', async () => {
    // Two azure/vm recs, same single account, different terms to force fan-out.
    const recs = [
      { id: 'g1', provider: 'azure', cloud_account_id: 'az-account-a', service: 'vm', resource_type: 'Standard_D2s_v3', region: 'eastus', count: 1, term: 1, payment: 'all-upfront', savings: 120, upfront_cost: 600 },
      { id: 'g2', provider: 'azure', cloud_account_id: 'az-account-a', service: 'vm', resource_type: 'Standard_D2s_v3', region: 'eastus', count: 1, term: 3, payment: 'all-upfront', savings: 300, upfront_cost: 1200 },
    ];
    setupMixedTermRecs(recs);
    // Override uses the Azure-canonical 'upfront' synonym — before the fix this
    // would seed FanOutBucket.payment = 'upfront' (not rendered by the dropdown);
    // after the fix it normalizes to 'all-upfront'.
    (api.listAccountServiceOverrides as jest.Mock).mockImplementation(async (id: string) => {
      if (id === 'az-account-a') {
        return [{ id: 'ovr-az', account_id: 'az-account-a', provider: 'azure', service: 'vm', payment: 'upfront' }];
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
      // Override 'upfront' must normalize to canonical 'all-upfront'.
      expect(b.payment).toBe('all-upfront');
      expect(b.paymentSource).toBe('override');
    }
  });

  // Regression: CR finding on PR #710.  'upfront' is a synonym for
  // 'all-upfront' used by some upstream rows (Azure canonical form).
  // Both must land in ONE bucket with the normalized 'all-upfront' seed.
  test('(g) payment synonyms upfront / all-upfront collapse into one bucket seeded as all-upfront', async () => {
    // Two recs, same aws/ec2/1yr, but one carries 'upfront' (Azure
    // synonym) and the other carries 'all-upfront' (canonical form).
    // Different resource_type so each is its own cell and both survive
    // pickBestVariantPerCell without collapsing.
    // After normalization they share the same bucket key and should
    // produce a SINGLE bucket (not two), forcing the single-bucket happy
    // path (openPurchaseModal, not openFanOutModal).
    const recs = [
      { id: 'f1', provider: 'aws', cloud_account_id: 'test-account-a', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment: 'upfront',     savings: 100, upfront_cost: 500 },
      { id: 'f2', provider: 'aws', cloud_account_id: 'test-account-a', service: 'ec2', resource_type: 'm5.large',  region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront', savings: 150, upfront_cost: 600 },
    ];
    setupMixedTermRecs(recs);
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);

    await loadRecommendations();
    (document.getElementById('bulk-purchase-btn') as HTMLButtonElement).click();
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();

    const { getFanOutBuckets } = await import('../recommendations');
    const buckets = getFanOutBuckets();
    // Single bucket expected: 'upfront' normalized to 'all-upfront' before keying.
    // Both recs share the same normalized key so no fan-out is triggered and
    // getFanOutBuckets() returns null (single-bucket happy path opens
    // openPurchaseModal instead of openFanOutModal).
    expect(buckets).toBeNull();
  });
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

  const OVERRIDE_FAIL_MSG = "Account payment overrides could not be loaded for 2 account(s) (test-account-a, test-account-b); check each row's payment before buying.";
  const overrideWarnings = () => mockShowToast.mock.calls.filter(
    ([o]) => (o as { message?: string }).message === OVERRIDE_FAIL_MSG,
  );

  test('failed override fetch: one warning toast, row seeded from rec payment (#1957)', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockRejectedValue(new Error('boom'));
    const rec = {
      id: 'rec-f', provider: 'aws' as const, cloud_account_id: 'test-account-a',
      service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
      count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500,
    };
    (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
    await openPurchaseModal([rec, { ...rec, id: 'rec-g', cloud_account_id: 'test-account-b' }]);
    expect(overrideWarnings()).toHaveLength(1);
    expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'warning', timeout: null }));
    expect(getPurchaseModalRecommendations()[0]!.payment).toBe('all-upfront');
  });
});
