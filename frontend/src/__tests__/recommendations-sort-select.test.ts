/**
 * Recommendations module tests: group sort tail, select-all, default sort direction and sort URL persistence tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, setupOpportunitiesTabDom } from './recommendations-harness';
import { loadRecommendations } from '../recommendations';
import type { LocalRecommendation } from '../types';
import * as api from '../api';
import * as state from '../state';
import * as recsApi from '../api/recommendations';

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

  //
  // Issue #768: toggling checkboxes must not change row sort order.
  //
  // Before the fix, cellScoreFor() switched to the selected variant's
  // individual value when selectedRecs contained a variant id, causing
  // groupsInSortOrder() to produce a different order after a checkbox toggle.
  //
  test('Issue #768: row order is identical before and after toggling checkboxes', async () => {
    // Three cells with distinct savings so they sort in a predictable order.
    // Cell A: savings = 10 (lowest) → should be last under desc
    // Cell B: savings = 50 (middle)
    // Cell C: savings = 90 (highest) → should be first under desc
    const cellA = multiVariantCell({
      resourceType: 'cell-A', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 10, savings3y: 15,
    });
    const cellB = multiVariantCell({
      resourceType: 'cell-B', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 50, savings3y: 55,
    });
    const cellC = multiVariantCell({
      resourceType: 'cell-C', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 90, savings3y: 95,
    });
    const recs = [...cellA, ...cellB, ...cellC];

    // --- render with no selection ---
    setupTestFixture(recs, { column: 'savings', direction: 'desc' });
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set<string>());
    await loadRecommendations();
    const orderBefore = renderedCellOrder();

    // Verify base order (desc: C > B > A).
    expect(indexOrFail(orderBefore, 'cell-C'))
      .toBeLessThan(indexOrFail(orderBefore, 'cell-B'));
    expect(indexOrFail(orderBefore, 'cell-B'))
      .toBeLessThan(indexOrFail(orderBefore, 'cell-A'));

    // --- simulate toggling two checkboxes (select 1yr variant of A and 3yr of C) ---
    setupTestFixture(recs, { column: 'savings', direction: 'desc' });
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(
      new Set(['cellA-1y', 'cellC-3y']),
    );
    await loadRecommendations();
    const orderAfter = renderedCellOrder();

    // Row order must be identical after selection change.
    expect(orderAfter).toEqual(orderBefore);
  });

  test('Sort is deterministic across repeated renders for each affected column', async () => {
    const cells = [
      multiVariantCell({ resourceType: 'A-cell', payment1y: 'no-upfront',      payment3y: 'all-upfront',     upfront1y: 0,   upfront3y: 1500, monthly1y: 50,  monthly3y: 0,  savings1y: 20, savings3y: 30 }),
      multiVariantCell({ resourceType: 'B-cell', payment1y: 'partial-upfront', payment3y: 'partial-upfront', upfront1y: 300, upfront3y: 900,  monthly1y: 40,  monthly3y: 30, savings1y: 25, savings3y: 35 }),
      multiVariantCell({ resourceType: 'C-cell', payment1y: 'all-upfront',     payment3y: 'no-upfront',      upfront1y: 800, upfront3y: 0,    monthly1y: 60,  monthly3y: 45, savings1y: 30, savings3y: 40 }),
    ].flat();

    const columns: Array<{ column: string; direction: 'asc' | 'desc' }> = [
      { column: 'term',                  direction: 'asc' },
      { column: 'payment',               direction: 'asc' },
      { column: 'upfront_cost',          direction: 'asc' },
      { column: 'monthly_cost',          direction: 'asc' },
      { column: 'effective_savings_pct', direction: 'asc' },
    ];

    for (const sort of columns) {
      setupTestFixture(cells, sort);
      await loadRecommendations();
      const first = renderedCellOrder();
      // Re-run with the SAME inputs.
      setupTestFixture(cells, sort);
      await loadRecommendations();
      const second = renderedCellOrder();
      expect(second).toEqual(first);
      // Must not be the empty / single-element trivial case.
      expect(first.length).toBe(3);
    }
  });

  //
  // QA 4.13 - Monthly Cost: zero-cost (all-upfront) variants must not prevent
  // sort-direction toggle from reordering rows.
  //
  // Before the fix, cells with at least one all-upfront variant (monthly_cost=0)
  // all scored 0 via Math.min(...finite), tying every such cell so the direction
  // multiplier had nothing to act on and subsequent sort clicks were no-ops.
  // The fix uses the minimum NON-ZERO recurring cost, falling back to 0 only
  // when all finite values are 0 (pure all-upfront cell).
  //
  test('Monthly Cost: mixed cell (all-upfront + no-upfront) sorts by non-zero recurring cost, not 0', async () => {
    // cellLow: all-upfront (monthly=0) + no-upfront (monthly=20) -> score = 20
    const cellLow = multiVariantCell({
      resourceType: 'low-mixed', payment1y: 'all-upfront', payment3y: 'no-upfront',
      upfront1y: 500, upfront3y: 0, monthly1y: 0, monthly3y: 20,
    });
    // cellHigh: all-upfront (monthly=0) + no-upfront (monthly=80) -> score = 80
    const cellHigh = multiVariantCell({
      resourceType: 'high-mixed', payment1y: 'all-upfront', payment3y: 'no-upfront',
      upfront1y: 500, upfront3y: 0, monthly1y: 0, monthly3y: 80,
    });
    const recs = [...cellHigh, ...cellLow]; // wrong insertion order

    // Ascending: cellLow (score=20) must precede cellHigh (score=80).
    setupTestFixture(recs, { column: 'monthly_cost', direction: 'asc' });
    await loadRecommendations();
    const ascOrder = renderedCellOrder();
    expect(indexOrFail(ascOrder, 'low-mixed'))
      .toBeLessThan(indexOrFail(ascOrder, 'high-mixed'));

    // Descending: cellHigh (score=80) must now precede cellLow (score=20).
    // This is the toggle that was broken before the fix -- both cells scored 0
    // so direction-flip was a no-op.
    setupTestFixture(recs, { column: 'monthly_cost', direction: 'desc' });
    await loadRecommendations();
    const descOrder = renderedCellOrder();
    expect(indexOrFail(descOrder, 'high-mixed'))
      .toBeLessThan(indexOrFail(descOrder, 'low-mixed'));

    // The two directions must produce OPPOSITE orderings (the toggle is live).
    expect(ascOrder).not.toEqual(descOrder);
  });

  test('Monthly Cost: pure all-upfront cell scores 0 (not POSITIVE_INFINITY) and sorts before all-null cells', async () => {
    // cellPureAllUpfront: both variants have monthly_cost=0 (all-upfront) -> score=0
    const cellPureAllUpfront = multiVariantCell({
      resourceType: 'pure-upfront', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 600, upfront3y: 1800, monthly1y: 0, monthly3y: 0,
    });
    // cellAllNull: both variants have monthly_cost=null -> score=POSITIVE_INFINITY
    const cellAllNull = multiVariantCell({
      resourceType: 'pure-null', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 1000, upfront3y: 3000, monthly1y: null, monthly3y: null,
    });
    const recs = [...cellAllNull, ...cellPureAllUpfront];

    // Ascending: pure-upfront (score=0) must come before all-null (score=+Inf).
    setupTestFixture(recs, { column: 'monthly_cost', direction: 'asc' });
    await loadRecommendations();
    const order = renderedCellOrder();
    expect(indexOrFail(order, 'pure-upfront'))
      .toBeLessThan(indexOrFail(order, 'pure-null'));
  });
});

//
// Issue #479: Select-all checkbox tri-state.
// The header checkbox renders with the right .checked / .indeterminate state
// reflecting current selection vs. the set of best-variant-per-cell recs
// (the set the select-all click actually populates).
//
describe('Issue #479: Select-all header checkbox tri-state', () => {
  const recs = [
    { id: 'r1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
    { id: 'r2', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, savings: 200, upfront_cost: 800 },
  ];

  beforeEach(() => {
    setupOpportunitiesTabDom();
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
      last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      last_collection_error: null,
    });
  });

  test('with no selection, header checkbox is unchecked and not indeterminate', async () => {
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    await loadRecommendations();
    const cb = document.getElementById('select-all-recs') as HTMLInputElement;
    expect(cb.checked).toBe(false);
    expect(cb.indeterminate).toBe(false);
  });

  test('with a partial selection, header checkbox is indeterminate', async () => {
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['r1']));
    await loadRecommendations();
    const cb = document.getElementById('select-all-recs') as HTMLInputElement;
    expect(cb.indeterminate).toBe(true);
    expect(cb.checked).toBe(false);
  });

  test('with every best-variant selected, header checkbox is checked and not indeterminate', async () => {
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['r1', 'r2']));
    await loadRecommendations();
    const cb = document.getElementById('select-all-recs') as HTMLInputElement;
    expect(cb.checked).toBe(true);
    expect(cb.indeterminate).toBe(false);
  });

  test('clicking when all selected clears selection (the previously-broken second-click path)', async () => {
    // Repro from the issue: with all rows selected, the header checkbox
    // used to render as unchecked, so the second click flipped it to
    // checked and re-selected everything (no-op). Now it renders as
    // checked, so the second click flips to unchecked and clears.
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['r1', 'r2']));
    await loadRecommendations();
    const cb = document.getElementById('select-all-recs') as HTMLInputElement;
    expect(cb.checked).toBe(true);
    // Simulate the browser's pre-change flip from true → false.
    cb.checked = false;
    cb.dispatchEvent(new Event('change'));
    expect(state.clearSelectedRecommendations).toHaveBeenCalled();
  });
});

//
// Issue #480: First-click sort direction per column.
// Text columns and most numerics default to 'asc' (A→Z / low → high).
// `savings` and `on_demand_monthly` keep 'desc' as the platform default.
//
describe('Issue #480: per-column default sort direction', () => {
  const recs = [
    { id: 'r1', provider: 'aws',   cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment: 'no-upfront', savings: 100, upfront_cost: 0,    monthly_cost: 50,  on_demand_cost: 80  },
    { id: 'r2', provider: 'azure', cloud_account_id: 'a2', service: 'vm',  resource_type: 'D2s_v3',    region: 'eastus',    count: 1, term: 1, payment: 'no-upfront', savings: 150, upfront_cost: 1000, monthly_cost: 100, on_demand_cost: 130 },
  ];

  beforeEach(() => {
    setupOpportunitiesTabDom();
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
      last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      last_collection_error: null,
    });
  });

  test.each<[string, 'asc' | 'desc']>([
    ['provider',              'asc'],
    ['account',               'asc'],
    ['service',               'asc'],
    ['resource_type',         'asc'],
    ['region',                'asc'],
    ['count',                 'asc'],
    ['term',                  'asc'],
    ['payment',               'asc'],
    ['upfront_cost',          'asc'],
    ['monthly_cost',          'asc'],
    ['effective_savings_pct', 'asc'],
    ['savings',               'desc'],  // exception per QA notes
    ['on_demand_monthly',     'desc'],  // exception per QA notes
  ])('first click on %s header sorts %s', async (col, expectedDir) => {
    // Pin the active sort to a column distinct from the one we click so
    // the click is treated as a "first click on a previously-unsorted
    // column" rather than a toggle on the active column. Pick `region`
    // unless `region` itself is the column under test, in which case
    // pick `provider`.
    const initialColumn = col === 'region' ? 'provider' : 'region';
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: initialColumn, direction: 'asc' });
    await loadRecommendations();
    const header = document.querySelector<HTMLTableCellElement>(`th[data-sort="${col}"]`);
    expect(header).not.toBeNull();
    header!.click();
    expect(state.setRecommendationsSort).toHaveBeenCalledWith({ column: col, direction: expectedDir });
  });

  test('second click on the same column toggles the direction', async () => {
    // Provider's first click goes to asc (covered above). After
    // state reports the active sort, the next click flips to desc.
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'provider', direction: 'asc' });
    await loadRecommendations();
    const header = document.querySelector<HTMLTableCellElement>('th[data-sort="provider"]');
    header!.click();
    expect(state.setRecommendationsSort).toHaveBeenLastCalledWith({ column: 'provider', direction: 'desc' });
  });
});

//
// Issue #481: Sort column + direction persisted across page refresh via
// URL query params (?sort=<col>&dir=<asc|desc>).
//
describe('Issue #481: URL persistence of sort state', () => {
  const recs = [
    { id: 'r1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
  ];

  beforeEach(() => {
    setupOpportunitiesTabDom();
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
      last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      last_collection_error: null,
    });
    // Reset URL between tests.
    window.history.replaceState({}, '', '/');
  });

  test('valid ?sort=col&dir=asc seeds setRecommendationsSort on load', async () => {
    window.history.replaceState({}, '', '/?sort=upfront_cost&dir=asc');
    await loadRecommendations();
    expect(state.setRecommendationsSort).toHaveBeenCalledWith({ column: 'upfront_cost', direction: 'asc' });
  });

  test('valid ?sort=col&dir=desc seeds setRecommendationsSort on load', async () => {
    window.history.replaceState({}, '', '/?sort=provider&dir=desc');
    await loadRecommendations();
    expect(state.setRecommendationsSort).toHaveBeenCalledWith({ column: 'provider', direction: 'desc' });
  });

  test('invalid ?sort=<unknown> is silently ignored', async () => {
    window.history.replaceState({}, '', '/?sort=not_a_column&dir=asc');
    await loadRecommendations();
    expect(state.setRecommendationsSort).not.toHaveBeenCalledWith(
      expect.objectContaining({ column: 'not_a_column' }),
    );
  });
});
