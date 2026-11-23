/**
 * Recommendations module tests: the last loadRecommendations tests and the first sort, bulk toolbar and detail drawer tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, setupRecommendationsTest } from './recommendations-harness';
import { loadRecommendations } from '../recommendations';
import * as api from '../api';
import * as state from '../state';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

describe('Recommendations Module', () => {
  beforeEach(setupRecommendationsTest);

  afterEach(() => {
    jest.useRealTimers();
  });

  describe('loadRecommendations', () => {
    afterEach(() => (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({}));

    test('individual row checkbox adds recommendation when checked', async () => {
      const mockRecs = [
        { id: 'rec-9', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 }
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: mockRecs,
        regions: []
      });

      await loadRecommendations();

      const checkbox = document.querySelector('input[data-rec-id]') as HTMLInputElement;
      checkbox.checked = true;
      checkbox.dispatchEvent(new Event('change'));

      expect(state.addSelectedRecommendation).toHaveBeenCalled();
      expect((state.addSelectedRecommendation as jest.Mock).mock.calls[0][0]).toMatch(/^rec-/);
    });

    test('individual row checkbox removes recommendation when unchecked', async () => {
      const mockRecs = [
        { id: 'rec-10', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 }
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: mockRecs,
        regions: []
      });

      await loadRecommendations();

      const checkbox = document.querySelector('input[data-rec-id]') as HTMLInputElement;
      checkbox.checked = false;
      checkbox.dispatchEvent(new Event('change'));

      expect(state.removeSelectedRecommendation).toHaveBeenCalled();
      expect((state.removeSelectedRecommendation as jest.Mock).mock.calls[0][0]).toMatch(/^rec-/);
    });

    // Issue #187: when two Azure recs share `(provider, service, region,
    // resource_type, payment)` but differ in subscription or term, they
    // each carry a distinct backend ID after the scheduler hash fix
    // (see TestScheduler_ConvertRecommendations_HashUniqueness). The
    // frontend selection toggle keys on data-rec-id, so distinct IDs →
    // independent toggles. Pin that here so a future regression on the
    // frontend rendering side surfaces immediately.
    test('toggling one row does NOT flip a sibling row with a distinct ID', async () => {
      const mockRecs = [
        { id: 'rec-azure-sub1', provider: 'azure', cloud_account_id: 'sub1', service: 'compute', resource_type: 'D2s', region: 'eastus', count: 1, term: 1, savings: 100, upfront_cost: 500 },
        { id: 'rec-azure-sub2', provider: 'azure', cloud_account_id: 'sub2', service: 'compute', resource_type: 'D2s', region: 'eastus', count: 1, term: 1, savings: 200, upfront_cost: 800 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: mockRecs,
        regions: []
      });

      await loadRecommendations();

      const checkboxes = Array.from(
        document.querySelectorAll<HTMLInputElement>('tbody input[data-rec-id]'),
      );
      expect(checkboxes).toHaveLength(2);
      const ids = checkboxes.map((cb) => cb.dataset['recId']).sort();
      expect(ids).toEqual(['rec-azure-sub1', 'rec-azure-sub2']);

      // Tick rec-azure-sub1 specifically — the table sort order would
      // otherwise depend on savings (sub2's 200 > sub1's 100), so we
      // pick by ID, not index.
      const sub1 = checkboxes.find((cb) => cb.dataset['recId'] === 'rec-azure-sub1');
      expect(sub1).toBeDefined();
      sub1!.checked = true;
      sub1!.dispatchEvent(new Event('change'));

      // Only sub1's ID should land in addSelectedRecommendation; sub2
      // stays untouched.
      const calls = (state.addSelectedRecommendation as jest.Mock).mock.calls;
      const calledIds = calls.map((c) => c[0]);
      expect(calledIds).toContain('rec-azure-sub1');
      expect(calledIds).not.toContain('rec-azure-sub2');
    });

    // Issue #224: when a sibling variant is selected but currently hidden by a
    // column filter (e.g. user filtered to 3yr, but 1yr sibling is still
    // selected in state), checking a 3yr sibling must evict the hidden 1yr
    // selection. The fix iterates state.getRecommendations() (full loaded set)
    // rather than the filtered `recommendations` array rendered in the DOM.
    test('checking a visible variant evicts a same-cell sibling hidden by filter', async () => {
      const allRecs = [
        { id: 'rec-1yr', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 0 },
        { id: 'rec-3yr', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 3, savings: 300, upfront_cost: 0 },
      ];
      // Simulate: API returns all recs, but only the 3yr one is visible (1yr is
      // hidden by filter). state.getRecommendations() returns the full set.
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [allRecs[1]], // only 3yr visible in rendered list
        regions: []
      });
      (state.getRecommendations as jest.Mock).mockReturnValue(allRecs); // full set in state
      (state as unknown as { getRecommendationByID: jest.Mock }).getRecommendationByID.mockImplementation((id: string) => allRecs.find((r) => r.id === id));
      // Simulate: 1yr is already selected in state.
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['rec-1yr']));

      await loadRecommendations();

      // Tick the 3yr checkbox.
      const cb = document.querySelector<HTMLInputElement>('input[data-rec-id="rec-3yr"]');
      expect(cb).not.toBeNull();
      cb!.checked = true;
      cb!.dispatchEvent(new Event('change'));

      // The hidden 1yr sibling must be evicted.
      const removeCalls = (state.removeSelectedRecommendation as jest.Mock).mock.calls.map(c => c[0]);
      expect(removeCalls).toContain('rec-1yr');
      // And the 3yr rec is added.
      const addCalls = (state.addSelectedRecommendation as jest.Mock).mock.calls.map(c => c[0]);
      expect(addCalls).toContain('rec-3yr');
    });

    test('purchase button opens modal for that recommendation', async () => {
      const mockRecs = [
        { id: 'rec-11', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 }
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: mockRecs,
        regions: []
      });
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(mockRecs);

      await loadRecommendations();

      // Bundle B: per-row Purchase buttons gone; the Purchase action lives
      // in the sticky bottom action box at #bulk-purchase-btn. #273: the
      // button is disabled until the user explicitly selects rows, so seed
      // the selection mock with the rec's id before clicking — the test is
      // asserting the modal-open flow, not the selection-gating UI.
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(mockRecs);
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['rec-11']));
      await loadRecommendations(); // re-render so the button picks up the selection
      const purchaseBtn = document.querySelector('#bulk-purchase-btn') as HTMLButtonElement;
      expect(purchaseBtn).not.toBeNull();
      expect(purchaseBtn.disabled).toBe(false);
      purchaseBtn.click();
      // Issue #111 (iii): openPurchaseModal is async; flush microtasks
      // so the modal-open call lands before we assert visibility.
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve();

      const modal = document.getElementById('purchase-modal');
      expect(modal?.classList.contains('hidden')).toBe(false);
    });

    test('renders engine info when present', async () => {
      const mockRecs = [
        { id: 'rec-12', provider: 'aws', service: 'rds', resource_type: 'db.t3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500, engine: 'mysql' }
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: mockRecs,
        regions: []
      });
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(mockRecs);

      await loadRecommendations();

      const list = document.getElementById('recommendations-list');
      expect(list?.innerHTML).toContain('mysql');
    });

    test('applies medium-savings class for moderate savings', async () => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [
          { id: 'rec-13', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 500, upfront_cost: 500 }
        ],
        regions: []
      });

      await loadRecommendations();

      const list = document.getElementById('recommendations-list');
      expect(list?.innerHTML).toContain('medium-savings');
    });
  });

  describe('P6: sort + bulk toolbar + detail drawer', () => {
    const twoRecs = [
      { id: 'rec-14', provider: 'aws', service: 'ec2', resource_type: 't3.large', region: 'us-east-1', count: 2, term: 1, savings: 100, upfront_cost: 500 },
      { id: 'rec-15', provider: 'aws', service: 'rds', resource_type: 'db.m5.large', region: 'us-east-1', count: 4, term: 3, savings: 1500, upfront_cost: 9000 },
    ];

    beforeEach(() => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        recommendations: twoRecs,
        summary: {},
        available_regions: [],
      });
    });

    test('renders sortable column headers with indicators (Bundle B + #282 + #317 + #219: 14 columns)', async () => {
      await loadRecommendations();
      const list = document.getElementById('recommendations-list');
      // Bundle B + issue #282 + #317 + #219: every data column is sortable. 14 sortable data columns:
      // provider, account, service, resource_type, capacity, region, count, term, payment,
      // savings, upfront_cost, monthly_cost, on_demand_monthly, effective_savings_pct.
      // The leading checkbox column is not sortable.
      const sortables = list?.querySelectorAll('th.sortable');
      expect(sortables?.length).toBe(14);
      // The default sort is savings desc → that header shows an active ▼.
      const savingsHeader = list?.querySelector('th[data-sort="savings"]');
      expect(savingsHeader?.innerHTML).toContain('active');
    });

    describe('header keyboard semantics (issue #755)', () => {
      test('header is a plain th with aria-sort and sibling sort and filter buttons', async () => {
        await loadRecommendations();
        const savings = document.querySelector<HTMLTableCellElement>('th[data-sort="savings"]');
        expect(savings?.getAttribute('role')).toBeNull();
        expect(savings?.hasAttribute('tabindex')).toBe(false);
        expect(savings?.getAttribute('aria-sort')).toBe('descending');
        expect(document.querySelector('th[data-sort="upfront_cost"]')?.hasAttribute('aria-sort')).toBe(false);
        const sortBtn = savings?.querySelector('button.sort-btn');
        const filterBtn = savings?.querySelector('button.column-filter-btn');
        expect(sortBtn?.textContent).toContain('Savings');
        expect(filterBtn).not.toBeNull();
        expect(sortBtn?.contains(filterBtn ?? null)).toBe(false);
      });

      test('Enter on a column-filter button does not sort', async () => {
        await loadRecommendations();
        (state.setRecommendationsSort as jest.Mock).mockClear();
        const filterBtn = document.querySelector<HTMLButtonElement>('th[data-sort="service"] .column-filter-btn');
        filterBtn?.focus();
        const enter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true });
        filterBtn?.dispatchEvent(enter);
        expect(enter.defaultPrevented).toBe(false);
        expect(state.setRecommendationsSort).not.toHaveBeenCalled();
        // Native Enter activation of a <button> is a click, which opens the popover.
        filterBtn?.click();
        expect(document.querySelector('.column-filter-popover')).not.toBeNull();
        expect(state.setRecommendationsSort).not.toHaveBeenCalled();
        document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
      });

      test('activating the sort button sorts and keeps focus on the re-rendered sort button', async () => {
        await loadRecommendations();
        const sortBtn = document.querySelector<HTMLButtonElement>('th[data-sort="upfront_cost"] .sort-btn');
        sortBtn?.focus();
        sortBtn?.click();
        expect(state.setRecommendationsSort).toHaveBeenCalledWith({ column: 'upfront_cost', direction: 'asc' });
        const active = document.activeElement;
        expect(active?.classList.contains('sort-btn')).toBe(true);
        expect(active?.closest('th')?.getAttribute('data-sort')).toBe('upfront_cost');
      });
    });

    test('clicking a sortable header calls setRecommendationsSort', async () => {
      await loadRecommendations();
      const header = document.querySelector<HTMLTableCellElement>('th[data-sort="upfront_cost"]');
      header?.click();
      // Issue #480: first click on a non-savings/non-on-demand numeric column
      // uses the platform-default ascending direction (low → high).
      expect(state.setRecommendationsSort).toHaveBeenCalledWith({ column: 'upfront_cost', direction: 'asc' });
    });

    test('Payment column header renders and is sortable (#282)', async () => {
      await loadRecommendations();
      const list = document.getElementById('recommendations-list');
      const paymentTh = list?.querySelector('th[data-sort="payment"]');
      expect(paymentTh).not.toBeNull();
      expect(paymentTh?.textContent).toContain('Payment');
      // Clicking the Payment header should call setRecommendationsSort with 'payment'.
      // Issue #480: text columns default to 'asc' on first click (alpha order).
      (paymentTh as HTMLElement)?.click();
      expect(state.setRecommendationsSort).toHaveBeenCalledWith({ column: 'payment', direction: 'asc' });
    });

    test('Payment column cell renders human-readable labels (#282)', async () => {
      const recs = [
        { id: 'p1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.small',  region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront',     savings: 100, upfront_cost: 1000 },
        { id: 'p2', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, payment: 'partial-upfront', savings: 200, upfront_cost: 500 },
        { id: 'p3', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-west-2', count: 1, term: 1, payment: 'no-upfront',      savings: 300, upfront_cost: 0 },
        { id: 'p4', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.m5',     region: 'us-west-2', count: 1, term: 3, payment: 'monthly',         savings: 400, upfront_cost: 0 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
      await loadRecommendations();

      const rows = document.querySelectorAll('tr.recommendation-row');
      const paymentCells = Array.from(rows).map((row) => {
        // Payment column is the 10th <td> (0-indexed: 0=checkbox, 1=provider,
        // 2=account, 3=service, 4=resource_type, 5=capacity, 6=region, 7=count, 8=term, 9=payment)
        return row.querySelectorAll('td')[9]?.textContent?.trim() ?? '';
      });
      expect(paymentCells).toContain('All Upfront');
      expect(paymentCells).toContain('Partial Upfront');
      expect(paymentCells).toContain('No Upfront');
      expect(paymentCells).toContain('Monthly');
      // Em-dash rendered for missing payment (no dedicated test rec here,
      // but the helper returns '—' for undefined — covered by formatPayment unit test).
    });

    test('Payment column filter narrows visible rows (#282)', async () => {
      const recs = [
        { id: 'f1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.small',  region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 1000 },
        { id: 'f2', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, payment: 'no-upfront',  savings: 200, upfront_cost: 0 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());

      // Apply a payment filter so only the all-upfront rec is visible.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        payment: { kind: 'set', values: ['all-upfront'] },
      });

      await loadRecommendations();

      const rows = document.querySelectorAll('tr.recommendation-row');
      // Only the all-upfront rec should be rendered.
      expect(rows.length).toBe(1);
      // Payment cell text should be "All Upfront".
      const paymentCell = rows[0]?.querySelectorAll('td')[9];
      expect(paymentCell?.textContent?.trim()).toBe('All Upfront');

      // Restore filter mock so it doesn't leak into subsequent tests.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    });

    test('bottom action box selection summary reflects current selection (Bundle B)', async () => {
      // Bundle B replaced the floating .recommendations-bulk-toolbar with
      // selection-summary text inside the sticky bottom action box.
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [
          { id: 'rec-bt', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 }
        ],
        regions: []
      });
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['rec-bt']));
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue([
        { id: 'rec-bt', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 }
      ]);

      await loadRecommendations();

      const summary = document.getElementById('recommendations-action-summary');
      // #281: summary text now surfaces the financial impact of the
      // current action target (savings/mo + upfront across N cells)
      // instead of just selection counts. The user can already see
      // selection state from row checkboxes; the action box is prime
      // real estate for the dollar figures they're authorising.
      expect(summary?.textContent).toMatch(/\$100\/mo/);
      expect(summary?.textContent).toMatch(/\$500 upfront/);
      expect(summary?.textContent).toMatch(/1 row\b/);
      // Old bulk-toolbar surface is gone.
      expect(document.querySelector('.recommendations-bulk-toolbar')).toBeNull();
    });

    test('action-box summary shows financial range when selection spans cells with multiple variants (#281)', async () => {
      // One cell (same provider+account+service+region+resource_type) with two
      // term variants: 1yr savings=$100/upfront=$500, 3yr savings=$300/upfront=$1200.
      // pageLevelRange sums per-cell envelopes: min=$100, max=$300 (one cell).
      // A second single-variant cell adds savings=$200/upfront=$600.
      // Total: savingsMin=100+200=$300, savingsMax=300+200=$500;
      //        upfrontMin=500+600=$1100, upfrontMax=1200+600=$1800; 2 cells.
      const base = { provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.large', region: 'us-east-1' };
      const v1yr = { ...base, id: 'rv-1yr', count: 2, term: 1, savings: 100, upfront_cost: 500  };
      const v3yr = { ...base, id: 'rv-3yr', count: 2, term: 3, savings: 300, upfront_cost: 1200 };
      const cell2 = { id: 'rv-c2', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.m5.large', region: 'us-east-1', count: 1, term: 1, savings: 200, upfront_cost: 600 };
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {}, recommendations: [v1yr, v3yr, cell2], regions: [],
      });
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['rv-1yr', 'rv-3yr', 'rv-c2']));
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue([v1yr, v3yr, cell2]);

      await loadRecommendations();

      const summary = document.getElementById('recommendations-action-summary');
      // #281: min!=max so formatSavingsRange emits the "X – Y" form.
      // formatCurrency is pinned to en-US (#1728), so $1,100 / $1,800 always
      // render with a comma; the regex still leaves it optional so the
      // assertion doesn't couple to that separator choice.
      expect(summary?.textContent).toMatch(/\$300\s*[–\-]\s*\$500\/mo/);
      expect(summary?.textContent).toMatch(/\$1,?100\s*[–\-]\s*\$1,?800 upfront/);
      expect(summary?.textContent).toMatch(/2 rows\b/);
    });

    test('action-box summary collapses to single value when all per-cell variant ranges are identical (#281)', async () => {
      // Both cells have only a single variant each, so min==max per cell and the
      // page-level sums also have min==max. formatSavingsRange collapses to one value.
      const recC = { id: 'r-c', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 200, upfront_cost: 600 };
      const recD = { id: 'r-d', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.m5.large', region: 'us-east-1', count: 1, term: 1, savings: 200, upfront_cost: 600 };
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {}, recommendations: [recC, recD], regions: [],
      });
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['r-c', 'r-d']));
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue([recC, recD]);

      await loadRecommendations();

      const summary = document.getElementById('recommendations-action-summary');
      // savingsMin==savingsMax==$400; formatSavingsRange collapses to "$400".
      expect(summary?.textContent).toMatch(/\$400\/mo/);
      expect(summary?.textContent).not.toMatch(/[–\-]\s*\$400/);
      expect(summary?.textContent).toMatch(/2 rows\b/);
    });

    test('bottom action box prompts for selection when no row is selected (#273)', async () => {
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(twoRecs);
      await loadRecommendations();
      const summary = document.getElementById('recommendations-action-summary');
      // #273: action buttons require an explicit selection. Summary
      // prompts the user instead of advertising a default-target count
      // that would fire on misclick.
      expect(summary?.textContent).toContain('Select rows to act on');
      expect(document.querySelector('.recommendations-bulk-toolbar')).toBeNull();
    });
  });
});
