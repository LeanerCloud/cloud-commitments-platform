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
import { closeModal } from '../modal';
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

  test('superseded open with a failed override fetch shows no toast (#1957)', async () => {
    let rejectFirst!: (e: Error) => void;
    (api.listAccountServiceOverrides as jest.Mock)
      .mockImplementationOnce(() => new Promise((_, rej) => { rejectFirst = rej; }))
      .mockResolvedValue([]);
    const rec = {
      id: 'rec-x', provider: 'aws' as const, cloud_account_id: 'test-account-a',
      service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
      count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500,
    };
    (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
    mockShowToast.mockClear();
    const first = openPurchaseModal([rec]);
    await openPurchaseModal([rec]);
    rejectFirst(new Error('late'));
    await first;
    expect(mockShowToast.mock.calls.filter(
      ([o]) => String((o as { message?: string }).message).includes('overrides could not be loaded'),
    )).toHaveLength(0);
  });

  const failingRecs = (n: number) => Array.from({ length: n }, (_, i) => ({
    id: `rec-n${i}`, provider: 'aws' as const, cloud_account_id: `acct-${i + 1}`,
    service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
    count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500,
  }));
  const warningMessages = () => mockShowToast.mock.calls
    .map(([o]) => String((o as { message?: string }).message))
    .filter((m) => m.includes('overrides could not be loaded'));

  test.each([
    [5, '5 account(s) (acct-1, acct-2, acct-3, acct-4, acct-5); check'],
    [6, '6 account(s) (acct-1, acct-2, acct-3, acct-4, acct-5 +1 more); check'],
    [7, '7 account(s) (acct-1, acct-2, acct-3, acct-4, acct-5 +2 more); check'],
  ])('%i failed accounts: count is the total, ids capped at 5 (#1957)', async (n, expected) => {
    (api.listAccountServiceOverrides as jest.Mock).mockRejectedValue(new Error('boom'));
    const recs = failingRecs(n);
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    mockShowToast.mockClear();
    await openPurchaseModal(recs);
    const msgs = warningMessages();
    expect(msgs).toHaveLength(1);
    expect(msgs[0]).toContain(`for ${expected}`);
  });

  test('cached account names replace raw ids in the warning (#1957)', async () => {
    (api.listAccountsMinimal as jest.Mock).mockResolvedValue([{ id: 'acct-1', name: 'Prod Account' }]);
    try {
      await loadRecommendations();
      (api.listAccountServiceOverrides as jest.Mock).mockRejectedValue(new Error('boom'));
      const recs = failingRecs(2);
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      mockShowToast.mockClear();
      await openPurchaseModal(recs);
      expect(warningMessages()[0]).toContain('(Prod Account, acct-2)');
    } finally {
      (api.listAccountsMinimal as jest.Mock).mockResolvedValue([]);
      await loadRecommendations();
    }
  });

  const openFailingModal = async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockRejectedValue(new Error('boom'));
    const recs = failingRecs(1);
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    mockShowToast.mockClear();
    await openPurchaseModal(recs);
    const dismiss = mockShowToast.mock.results[0]!.value.dismiss as jest.Mock;
    expect(dismiss).not.toHaveBeenCalled();
    return dismiss;
  };

  test('closing the modal dismisses the warning toast (#1957)', async () => {
    const dismiss = await openFailingModal();
    closeModal(document.getElementById('purchase-modal') as HTMLElement);
    expect(dismiss).toHaveBeenCalledTimes(1);
  });

  test('Escape on the modal dismisses the warning toast (#1957)', async () => {
    const dismiss = await openFailingModal();
    document.getElementById('purchase-modal')!.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }),
    );
    expect(dismiss).toHaveBeenCalledTimes(1);
  });

  test('a new open replaces the previous warning instead of stacking (#1957)', async () => {
    const dismiss = await openFailingModal();
    await openPurchaseModal(failingRecs(1));
    expect(dismiss).toHaveBeenCalledTimes(1);
    expect(warningMessages()).toHaveLength(2);
  });

  test('an open with no failures clears the previous warning (#1957)', async () => {
    const dismiss = await openFailingModal();
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([]);
    await openPurchaseModal(failingRecs(1));
    expect(dismiss).toHaveBeenCalledTimes(1);
    expect(warningMessages()).toHaveLength(1);
  });

  test('successful override fetch: no override warning toast (#1957)', async () => {
    const rec = {
      id: 'rec-s', provider: 'aws' as const, cloud_account_id: 'test-account-a',
      service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
      count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500,
    };
    (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
    await openPurchaseModal([rec]);
    expect(overrideWarnings()).toHaveLength(0);
  });

  test('(a) single rec with matching override → row Payment seeded from override; source-note rendered', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockImplementation(async (id: string) => {
      if (id === 'test-account-a') {
        return [{ id: 'ovr-1', account_id: 'test-account-a', provider: 'aws', service: 'ec2', payment: 'partial-upfront' }];
      }
      return [];
    });

    const rec = {
      id: 'rec-1', provider: 'aws' as const, cloud_account_id: 'test-account-a',
      service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
      count: 5, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500,
    };
    // Issue #1903: the override is only honoured when a priced variant for
    // it was actually loaded.
    (state.getRecommendations as jest.Mock).mockReturnValue([
      rec,
      { ...rec, id: 'rec-1-partial', payment: 'partial-upfront', upfront_cost: 250, monthly_cost: 20 },
    ]);

    await openPurchaseModal([rec]);

    const live = getPurchaseModalRecommendations();
    expect(live).toHaveLength(1);
    expect(live[0]!.payment).toBe('partial-upfront');
    expect(live[0]!.upfront_cost).toBe(250);
    expect(live[0]!.id).toBe('rec-1-partial');

    const select = document.querySelector<HTMLSelectElement>('.purchase-row-payment');
    expect(select).not.toBeNull();
    expect(select!.value).toBe('partial-upfront');

    const note = document.querySelector<HTMLElement>('.purchase-row-payment-source');
    expect(note).not.toBeNull();
    expect(note!.textContent).toContain('account override');
  });

  test('(b) single rec with NO matching override → row Payment seeded from rec.payment; no source-note', async () => {
    // Override exists but for a different service — the lookup misses
    // and the rec's own payment ('partial-upfront') wins.
    (api.listAccountServiceOverrides as jest.Mock).mockImplementation(async (id: string) => {
      if (id === 'test-account-a') {
        return [{ id: 'ovr-1', account_id: 'test-account-a', provider: 'aws', service: 'rds', payment: 'no-upfront' }];
      }
      return [];
    });

    const rec = {
      id: 'rec-2', provider: 'aws' as const, cloud_account_id: 'test-account-a',
      service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
      count: 5, term: 1, payment: 'partial-upfront', savings: 100, upfront_cost: 500,
    };

    await openPurchaseModal([rec]);

    const live = getPurchaseModalRecommendations();
    expect(live[0]!.payment).toBe('partial-upfront');

    const select = document.querySelector<HTMLSelectElement>('.purchase-row-payment');
    expect(select!.value).toBe('partial-upfront');

    const note = document.querySelector('.purchase-row-payment-source');
    expect(note).toBeNull();
  });
});
