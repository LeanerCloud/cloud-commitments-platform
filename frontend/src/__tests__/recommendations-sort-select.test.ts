/**
 * Recommendations module tests: group sort tail, select-all, default sort direction and sort URL persistence tests.
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
});
