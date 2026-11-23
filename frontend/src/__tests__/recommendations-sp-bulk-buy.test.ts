/**
 * Recommendations module tests: per-row Payment seed tail and SP and Azure SP bulk-buy bucketing.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import { openPurchaseModal, getPurchaseModalRecommendations } from '../recommendations';
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
