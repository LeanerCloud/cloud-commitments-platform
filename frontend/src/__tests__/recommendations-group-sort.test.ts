/**
 * Recommendations module tests: deterministic group sort on multi-variant cells.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import { loadRecommendations } from '../recommendations';
import type { LocalRecommendation } from '../types';
import * as api from '../api';
import * as state from '../state';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

//
// Issue #494: deterministic group sort on multi-variant cells.
//
// After PR #195's per-(term, payment) fan-out, every cell has BOTH 1yr and 3yr
// variants. The previous `Math.max(...va.map(numericKey))` cell-score collapses
// to the same value across every cell for Term / Payment / Monthly Cost /
// Effective % / (and many cells for Upfront), producing apparently-random row
// order. These regression tests assert deterministic ordering matching what
// the user sees in the rendered summary row.
//
// PR #491 (closes #480) fixed the default-direction inversion; this PR fixes
// the upstream "every cell ties" symptom.
//
describe('Issue #494: deterministic group sort on multi-variant cells', () => {
  /** Build the minimum DOM loadRecommendations needs, using createElement so
   * no innerHTML assignment is required (the rest of the file uses innerHTML;
   * here we use the safer DOM API to match the test boilerplate pattern in
   * `Bundle B: column header filter triggers`). */
  function buildTestDOM(): void {
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
  }

  /**
   * Build a single multi-variant cell (2 variants: term=1 + term=3) with the
   * provided per-variant payments / upfront / monthly_cost. Cell identity is
   * controlled by `resource_type` so callers can build N distinct cells in one
   * fixture.
   */
  function multiVariantCell(opts: {
    resourceType: string;
    payment1y: string;
    payment3y: string;
    upfront1y: number;
    upfront3y: number;
    monthly1y?: number | null;
    monthly3y?: number | null;
    onDemand?: number | null;
    savings1y?: number;
    savings3y?: number;
  }): LocalRecommendation[] {
    const slug = opts.resourceType.replace(/\W/g, '');
    return [
      {
        id: `${slug}-1y`,
        provider: 'aws',
        cloud_account_id: 'a1',
        service: 'ec2',
        resource_type: opts.resourceType,
        region: 'us-east-1',
        count: 1,
        term: 1,
        payment: opts.payment1y,
        savings: opts.savings1y ?? 100,
        upfront_cost: opts.upfront1y,
        monthly_cost: opts.monthly1y === undefined ? 50 : opts.monthly1y,
        on_demand_cost: opts.onDemand === undefined ? 200 : opts.onDemand,
      } as unknown as LocalRecommendation,
      {
        id: `${slug}-3y`,
        provider: 'aws',
        cloud_account_id: 'a1',
        service: 'ec2',
        resource_type: opts.resourceType,
        region: 'us-east-1',
        count: 1,
        term: 3,
        payment: opts.payment3y,
        savings: opts.savings3y ?? 300,
        upfront_cost: opts.upfront3y,
        monthly_cost: opts.monthly3y === undefined ? 40 : opts.monthly3y,
        on_demand_cost: opts.onDemand === undefined ? 200 : opts.onDemand,
      } as unknown as LocalRecommendation,
    ];
  }

  /**
   * Read the rendered cell order from the DOM. Multi-variant cells render as
   * `tr.rec-cell-summary-row[data-cell-key]`; the test fixtures here use only
   * multi-variant cells (every cell has 2 variants) so this single selector
   * captures the full ordering.
   */
  function renderedCellOrder(): string[] {
    return Array.from(
      document.querySelectorAll<HTMLTableRowElement>('tr.rec-cell-summary-row'),
    ).map((tr) => tr.getAttribute('data-cell-key') ?? '');
  }

  /**
   * Find a key fragment in the rendered order and assert it is actually
   * present. Returns the index. Prevents false-positive `<` comparisons that
   * would silently pass when `findIndex` returns `-1` for both sides (per CR
   * review on the initial commit).
   */
  function indexOrFail(order: string[], keyFragment: string): number {
    const idx = order.findIndex((k) => k.includes(keyFragment));
    expect(idx).toBeGreaterThanOrEqual(0);
    return idx;
  }

  /** Set up the DOM + standard mocks; caller provides the rec list + sort. */
  function setupTestFixture(
    recs: LocalRecommendation[],
    sort: { column: string; direction: 'asc' | 'desc' },
  ): void {
    buildTestDOM();
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: recs,
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    (state.getRecommendationsSort as jest.Mock).mockReturnValue(sort);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
  }

  test('Term desc: order is the exact reverse of ascending', async () => {
    const cellMixed = multiVariantCell({
      resourceType: 'aaa-mixed', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0,
    });
    const cell1yOnly: LocalRecommendation[] = [
      { id: 'bbb-1y-nu', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'bbb-1y-only', region: 'us-east-1', count: 1, term: 1,
        payment: 'no-upfront', savings: 100, upfront_cost: 0, monthly_cost: 50 } as unknown as LocalRecommendation,
      { id: 'bbb-1y-au', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'bbb-1y-only', region: 'us-east-1', count: 1, term: 1,
        payment: 'all-upfront', savings: 110, upfront_cost: 800, monthly_cost: 0 } as unknown as LocalRecommendation,
    ];
    const cell3yOnly: LocalRecommendation[] = [
      { id: 'ccc-3y-nu', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'ccc-3y-only', region: 'us-east-1', count: 1, term: 3,
        payment: 'no-upfront', savings: 300, upfront_cost: 0, monthly_cost: 40 } as unknown as LocalRecommendation,
      { id: 'ccc-3y-au', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'ccc-3y-only', region: 'us-east-1', count: 1, term: 3,
        payment: 'all-upfront', savings: 320, upfront_cost: 2400, monthly_cost: 0 } as unknown as LocalRecommendation,
    ];
    const recs = [...cellMixed, ...cell1yOnly, ...cell3yOnly];

    setupTestFixture(recs, { column: 'term', direction: 'desc' });
    await loadRecommendations();

    const order = renderedCellOrder();
    expect(indexOrFail(order, 'ccc-3y-only'))
      .toBeLessThan(indexOrFail(order, 'aaa-mixed'));
    expect(indexOrFail(order, 'aaa-mixed'))
      .toBeLessThan(indexOrFail(order, 'bbb-1y-only'));
  });

  //
  // 4.10 - Payment
  //
  test('Payment asc: orders cells by canonical PAYMENT_ORDER (no-upfront < partial-upfront < all-upfront)', async () => {
    // Each cell has term=1 + term=3 variants with the *same* payment so the
    // canonical first-variant payment per cell is unambiguous.
    const cellPartial = multiVariantCell({
      resourceType: 'partial-cell', payment1y: 'partial-upfront', payment3y: 'partial-upfront',
      upfront1y: 200, upfront3y: 600,
    });
    const cellAllUp = multiVariantCell({
      resourceType: 'allup-cell', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 1000, upfront3y: 3000,
    });
    const cellNoUp = multiVariantCell({
      resourceType: 'noup-cell', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0,
    });
    const recs = [...cellAllUp, ...cellPartial, ...cellNoUp];  // intentionally wrong insertion order

    setupTestFixture(recs, { column: 'payment', direction: 'asc' });
    await loadRecommendations();

    const order = renderedCellOrder();
    // Expected semantic order: no-upfront < partial-upfront < all-upfront.
    // Previous alphabetic comparator would have produced all-upfront <
    // no-upfront < partial-upfront, which this test would fail under.
    expect(indexOrFail(order, 'noup-cell'))
      .toBeLessThan(indexOrFail(order, 'partial-cell'));
    expect(indexOrFail(order, 'partial-cell'))
      .toBeLessThan(indexOrFail(order, 'allup-cell'));
  });

  //
  // 4.12 - Upfront Cost
  //
  test('Upfront Cost asc: orders cells by summary.upfrontMin', async () => {
    const cellLow = multiVariantCell({
      resourceType: 'low-upfront', payment1y: 'no-upfront', payment3y: 'partial-upfront',
      upfront1y: 0, upfront3y: 500,
    });
    const cellMid = multiVariantCell({
      resourceType: 'mid-upfront', payment1y: 'partial-upfront', payment3y: 'all-upfront',
      upfront1y: 300, upfront3y: 1500,
    });
    const cellHigh = multiVariantCell({
      resourceType: 'high-upfront', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 800, upfront3y: 2400,
    });
    const recs = [...cellHigh, ...cellMid, ...cellLow];  // wrong insertion order

    setupTestFixture(recs, { column: 'upfront_cost', direction: 'asc' });
    await loadRecommendations();

    const order = renderedCellOrder();
    // Expected: upfrontMin asc -> 0 < 300 < 800.
    expect(indexOrFail(order, 'low-upfront'))
      .toBeLessThan(indexOrFail(order, 'mid-upfront'));
    expect(indexOrFail(order, 'mid-upfront'))
      .toBeLessThan(indexOrFail(order, 'high-upfront'));
  });
});
