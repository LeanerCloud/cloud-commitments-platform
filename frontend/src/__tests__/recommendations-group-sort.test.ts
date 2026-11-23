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

  //
  // 4.13 - Monthly Cost
  //
  test('Monthly Cost asc: orders cells by Math.min over non-null variants', async () => {
    const cellLow = multiVariantCell({  // min(30, 20) = 20
      resourceType: 'low-monthly', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, monthly1y: 30, monthly3y: 20,
    });
    const cellMid = multiVariantCell({  // min(60, null) = 60 (best-case-of-known)
      resourceType: 'mid-monthly', payment1y: 'no-upfront', payment3y: 'all-upfront',
      upfront1y: 0, upfront3y: 2400, monthly1y: 60, monthly3y: null,
    });
    const cellHigh = multiVariantCell({  // min(100, 90) = 90
      resourceType: 'high-monthly', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, monthly1y: 100, monthly3y: 90,
    });
    const recs = [...cellHigh, ...cellMid, ...cellLow];

    setupTestFixture(recs, { column: 'monthly_cost', direction: 'asc' });
    await loadRecommendations();

    const order = renderedCellOrder();
    // Expected: 20 < 60 < 90.
    expect(indexOrFail(order, 'low-monthly'))
      .toBeLessThan(indexOrFail(order, 'mid-monthly'));
    expect(indexOrFail(order, 'mid-monthly'))
      .toBeLessThan(indexOrFail(order, 'high-monthly'));
  });

  test('Monthly Cost: cells with all-null monthly_cost sort last in both asc and desc', async () => {
    const cellFinite = multiVariantCell({
      resourceType: 'finite-monthly', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, monthly1y: 50, monthly3y: 40,
    });
    const cellAllNull = multiVariantCell({
      resourceType: 'allnull-monthly', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 1000, upfront3y: 3000, monthly1y: null, monthly3y: null,
    });
    const recs = [...cellAllNull, ...cellFinite];

    // Ascending: finite first, all-null last.
    setupTestFixture(recs, { column: 'monthly_cost', direction: 'asc' });
    await loadRecommendations();
    let order = renderedCellOrder();
    expect(indexOrFail(order, 'finite-monthly'))
      .toBeLessThan(indexOrFail(order, 'allnull-monthly'));

    // Descending: all-null cells STAY last regardless of direction - the
    // POSITIVE_INFINITY null-sentinel logic flips them out of the normal
    // descending order specifically because "no data" rows should be
    // de-emphasised in both directions.
    setupTestFixture(recs, { column: 'monthly_cost', direction: 'desc' });
    await loadRecommendations();
    order = renderedCellOrder();
    expect(indexOrFail(order, 'finite-monthly'))
      .toBeLessThan(indexOrFail(order, 'allnull-monthly'));
  });

  test('Monthly Cost: two all-null cells sort deterministically via cellKey tiebreaker', async () => {
    // Both cells have ALL null monthly_cost - their scores are both
    // POSITIVE_INFINITY. The naive numeric diff would be Infinity - Infinity
    // = NaN; the comparator MUST short-circuit to the cellKey tiebreaker so
    // the two cells render in a stable order across repeated invocations.
    // Closes the gap surfaced by CodeRabbit on the initial #494 commit.
    const cellA = multiVariantCell({
      resourceType: 'aaa-null', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 1000, upfront3y: 3000, monthly1y: null, monthly3y: null,
    });
    const cellB = multiVariantCell({
      resourceType: 'bbb-null', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 2000, upfront3y: 6000, monthly1y: null, monthly3y: null,
    });
    const recs = [...cellB, ...cellA];  // intentionally wrong insertion order

    setupTestFixture(recs, { column: 'monthly_cost', direction: 'asc' });
    await loadRecommendations();
    const first = renderedCellOrder();
    expect(first.length).toBe(2);
    // cellKey contains the resource_type slug, so cellKey for 'aaa-null'
    // sorts before 'bbb-null' under localeCompare.
    expect(indexOrFail(first, 'aaa-null'))
      .toBeLessThan(indexOrFail(first, 'bbb-null'));

    // Re-run to confirm determinism (would fail under Infinity - Infinity = NaN).
    setupTestFixture(recs, { column: 'monthly_cost', direction: 'asc' });
    await loadRecommendations();
    const second = renderedCellOrder();
    expect(second).toEqual(first);
  });

  //
  // 4.15 - Effective %
  //
  test('Effective % asc: orders cells by Math.max over non-null variants (lowest best-pct first)', async () => {
    // effectiveSavingsPct uses on_demand_cost when set. Pick on-demand values
    // so each cell's pct is predictable. Formula:
    //   pct = (savings - upfront/(term*12)) / on_demand * 100
    // low-pct  cell: both variants score ~5%, max = 5%
    const cellLowPct = multiVariantCell({
      resourceType: 'low-pct', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 10, savings3y: 10, onDemand: 200,
    });
    // mid-pct cell: both variants score ~15%, max = 15%
    const cellMidPct = multiVariantCell({
      resourceType: 'mid-pct', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 30, savings3y: 30, onDemand: 200,
    });
    // high-pct cell: both variants score ~25%, max = 25%
    const cellHighPct = multiVariantCell({
      resourceType: 'high-pct', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 50, savings3y: 50, onDemand: 200,
    });
    const recs = [...cellHighPct, ...cellMidPct, ...cellLowPct];

    setupTestFixture(recs, { column: 'effective_savings_pct', direction: 'asc' });
    await loadRecommendations();

    const order = renderedCellOrder();
    // Expected asc: 5% < 15% < 25%.
    expect(indexOrFail(order, 'low-pct'))
      .toBeLessThan(indexOrFail(order, 'mid-pct'));
    expect(indexOrFail(order, 'mid-pct'))
      .toBeLessThan(indexOrFail(order, 'high-pct'));
  });

  test('Effective %: cells with all-null pct sort last regardless of direction', async () => {
    // A cell whose every variant has term=0 returns null from
    // effectiveSavingsPct - that's the null sentinel under test.
    const cellFinite = multiVariantCell({
      resourceType: 'finite-pct', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 20, savings3y: 30, onDemand: 200,
    });
    const cellAllNull: LocalRecommendation[] = [
      { id: 'nullpct-v1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'allnull-pct', region: 'us-east-1', count: 1, term: 0,
        payment: 'no-upfront', savings: 0, upfront_cost: 0, monthly_cost: 50,
        on_demand_cost: 200 } as unknown as LocalRecommendation,
      { id: 'nullpct-v2', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'allnull-pct', region: 'us-east-1', count: 1, term: 0,
        payment: 'all-upfront', savings: 0, upfront_cost: 0, monthly_cost: 50,
        on_demand_cost: 200 } as unknown as LocalRecommendation,
    ];
    const recs = [...cellAllNull, ...cellFinite];

    setupTestFixture(recs, { column: 'effective_savings_pct', direction: 'asc' });
    await loadRecommendations();
    let order = renderedCellOrder();
    expect(indexOrFail(order, 'finite-pct'))
      .toBeLessThan(indexOrFail(order, 'allnull-pct'));

    setupTestFixture(recs, { column: 'effective_savings_pct', direction: 'desc' });
    await loadRecommendations();
    order = renderedCellOrder();
    expect(indexOrFail(order, 'finite-pct'))
      .toBeLessThan(indexOrFail(order, 'allnull-pct'));
  });

  //
  // Determinism: repeating the same sort yields the same order.
  //
  // The pre-#494 bug also surfaced as "two clicks of the same header may
  // produce a different broken order" because every cell tied -> fallback to
  // browser sort stability, which is implementation-defined for old V8 builds
  // and varies across JS engines. With the new comparator every cell has a
  // distinct score (or, if genuinely tied, a stable cellKey tiebreaker), so
  // repeated invocations MUST produce the same order.
  //
  // Selection-independent Term sort: cells with different term distributions
  // must sort correctly by cellSummary score (termMin*100+termMax) regardless
  // of which variants the user has selected. Fix for Issue #768: the previous
  // "selected-variant short-circuit" in cellScoreFor() switched the score to
  // the selected variant's individual term value, which caused rows to reorder
  // on every checkbox toggle.
  //
  test('Term sort is selection-independent: cells rank by term distribution not by selected variant', async () => {
    // cell-1y-only: both variants are term=1 (termMin=1, termMax=1, score=101)
    const cell1yOnly: LocalRecommendation[] = [
      { id: '1yonly-nu', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: '1y-only', region: 'us-east-1', count: 1, term: 1,
        payment: 'no-upfront', savings: 100, upfront_cost: 0, monthly_cost: 50 } as unknown as LocalRecommendation,
      { id: '1yonly-au', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: '1y-only', region: 'us-east-1', count: 1, term: 1,
        payment: 'all-upfront', savings: 110, upfront_cost: 800, monthly_cost: 0 } as unknown as LocalRecommendation,
    ];
    // cell-mixed: term=1 and term=3 variants (termMin=1, termMax=3, score=103)
    const cellMixed = multiVariantCell({
      resourceType: 'mixed', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0,
    });
    // cell-3y-only: both variants are term=3 (termMin=3, termMax=3, score=303)
    const cell3yOnly: LocalRecommendation[] = [
      { id: '3yonly-nu', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: '3y-only', region: 'us-east-1', count: 1, term: 3,
        payment: 'no-upfront', savings: 300, upfront_cost: 0, monthly_cost: 40 } as unknown as LocalRecommendation,
      { id: '3yonly-au', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: '3y-only', region: 'us-east-1', count: 1, term: 3,
        payment: 'all-upfront', savings: 320, upfront_cost: 2400, monthly_cost: 0 } as unknown as LocalRecommendation,
    ];
    const recs = [...cell3yOnly, ...cellMixed, ...cell1yOnly];

    // Select the 3yr variant from the mixed cell. Under the old (buggy) code
    // this caused mixed-cell's score to become 3*100+3=303, tying it with
    // 3y-only and pushing it behind 1y-only in asc order.
    setupTestFixture(recs, { column: 'term', direction: 'asc' });
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(
      new Set(['mixed-3y']),
    );
    await loadRecommendations();

    const order = renderedCellOrder();
    // Expected under term asc (selection must NOT affect order):
    //   1y-only (score=101) < mixed (score=103) < 3y-only (score=303)
    expect(indexOrFail(order, '1y-only'))
      .toBeLessThan(indexOrFail(order, 'mixed'));
    expect(indexOrFail(order, 'mixed'))
      .toBeLessThan(indexOrFail(order, '3y-only'));
  });
});
