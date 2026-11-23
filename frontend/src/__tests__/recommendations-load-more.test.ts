/**
 * Recommendations module tests: loadRecommendations rendering and error tests.
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

    test('variants differing only by payment get distinct checkbox names (issue #746)', async () => {
      const base = { provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 150, upfront_cost: 1000 };
      const recs = [
        { ...base, id: 'rec-np', payment: 'no-upfront' },
        { ...base, id: 'rec-au', payment: 'all-upfront' },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: { total_count: 2, total_monthly_savings: 300, total_upfront_cost: 2000, avg_payback_months: 1 },
        recommendations: recs,
        regions: [],
      });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
      await loadRecommendations();
      document.querySelector<HTMLButtonElement>('.rec-cell-chevron')!.click();

      const label = (id: string) =>
        document.querySelector<HTMLInputElement>(`input[data-rec-id="${id}"]`)?.getAttribute('aria-label') ?? '';
      expect(label('rec-np')).toContain('No Upfront');
      expect(label('rec-au')).toContain('All Upfront');
      expect(label('rec-np')).not.toBe(label('rec-au'));
    });

    test('engine-only rows get distinct and unmapped-payment rows get clean checkbox names (issue #746)', async () => {
      const base = { provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.r5.large', region: 'us-east-1', count: 1, term: 1, savings: 150, upfront_cost: 0 };
      const recs = [
        { ...base, id: 'rec-my', engine: 'mysql', payment: 'no-upfront' },
        { ...base, id: 'rec-pg', engine: 'postgresql', payment: 'no-upfront' },
        { ...base, id: 'rec-x', payment: 'weird-option' },
        { ...base, id: 'rec-none', resource_type: 'db.r5.xlarge' },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: { total_count: 4, total_monthly_savings: 600, total_upfront_cost: 0, avg_payback_months: 1 },
        recommendations: recs,
        regions: [],
      });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
      await loadRecommendations();
      document.querySelectorAll<HTMLButtonElement>('.rec-cell-chevron').forEach(b => b.click());

      const label = (id: string) =>
        document.querySelector<HTMLInputElement>(`input[data-rec-id="${id}"]`)?.getAttribute('aria-label') ?? '';
      expect(label('rec-my')).toContain('db.r5.large (mysql)');
      expect(label('rec-my')).not.toBe(label('rec-pg'));
      expect(label('rec-x')).not.toContain('weird-option');
      for (const id of ['rec-my', 'rec-pg', 'rec-x', 'rec-none']) {
        expect(label(id)).not.toMatch(/\s{2,}|\s$/);
      }
    });

    test('select-all change event updates summary cards in place (PR #283 CR pass-2)', async () => {
      // Verifies the select-all checkbox event path: after checking select-all,
      // summary cards reflect the picked-best-per-cell selection WITHOUT a
      // second loadRecommendations() call.
      //
      // pickBestVariantPerCell picks one variant per (provider, account, service,
      // region, resource_type, engine) group — for these 2 cells it picks one
      // from cell1 and one from cell2, so both cards must show 2 rows selected.
      const recs = [
        { id: 'cell1-cheap',  provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront',  savings: 100, upfront_cost: 0 },
        { id: 'cell1-pricey', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'all-upfront', savings: 150, upfront_cost: 1000 },
        { id: 'cell2-cheap',  provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront',  savings: 200, upfront_cost: 0 },
        { id: 'cell2-pricey', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, payment_option: 'all-upfront', savings: 250, upfront_cost: 2000 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: { total_count: 4, total_monthly_savings: 700, total_upfront_cost: 3000, avg_payback_months: 2 },
        recommendations: recs,
        regions: [],
      });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});

      // Wire add/remove/clear to a shared Set (same coordination pattern as the
      // row-checkbox test above and the evict-siblings test).
      const selectedIds = new Set<string>();
      (state.getSelectedRecommendationIDs as jest.Mock).mockImplementation(() => new Set(selectedIds));
      (state.addSelectedRecommendation as jest.Mock).mockImplementation((id: string) => { selectedIds.add(id); });
      (state.removeSelectedRecommendation as jest.Mock).mockImplementation((id: string) => { selectedIds.delete(id); });
      (state.clearSelectedRecommendations as jest.Mock).mockImplementation(() => { selectedIds.clear(); });

      await loadRecommendations();

      const cardValue = (titlePattern: RegExp): string => {
        const summary = document.getElementById('recommendations-summary');
        const card = Array.from(summary?.querySelectorAll('.card') ?? [])
          .find((c) => titlePattern.test(c.querySelector('h3')?.textContent ?? ''));
        return card?.querySelector('.value')?.textContent ?? '';
      };

      // Fire the real select-all change event — do NOT call loadRecommendations().
      const selectAll = document.getElementById('select-all-recs') as HTMLInputElement;
      expect(selectAll).not.toBeNull();
      selectAll.checked = true;
      selectAll.dispatchEvent(new Event('change'));

      // addSelectedRecommendation was called once per cell (pickBestVariantPerCell
      // picks one per cell → 2 cells → 2 adds).
      expect((state.addSelectedRecommendation as jest.Mock).mock.calls.length).toBe(2);

      // Recommendations card shows "2" (2 selected cells, not 4 raw variants).
      expect(cardValue(/Recommendations/)).toBe('2');
    });

    test('"Showing X of Y" surfaces selection count (closes #279)', async () => {
      const recs = [
        { id: 'r1', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 0 },
        { id: 'r2', provider: 'aws', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, savings: 200, upfront_cost: 0 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

      // No selection: line just shows "Showing X of Y".
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
      await loadRecommendations();
      const liveText = (): string =>
        document.querySelector('.recommendations-filter-live')?.textContent ?? '';
      expect(liveText()).toMatch(/^Showing 2 of 2 recommendations$/);

      // ≥1 selected: line prepends the selection count so the user has
      // visible feedback that their selection is influencing the cards.
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['r1']));
      await loadRecommendations();
      expect(liveText()).toMatch(/^1 selected · Showing 2 of 2 recommendations$/);
    });

    test('selection filtered out of view: cards and live line treat it as no selection (PR #283 CR)', async () => {
      // 4 recs / 2 cells. The user has selected cell2's variant ('r3'), but
      // then applies a column filter that hides cell2. The visible set is
      // cell1 only (r1, r2). The selected row (r3) is NOT in the visible set.
      //
      // Expected behaviour:
      //   - Summary cards reflect the visible (unselected-from-visible) set:
      //     cell1 range $100–$150, title "Total Recommendations" not "Selected".
      //   - Live status line reads "Showing 2 of 4 recommendations" — no
      //     "1 selected" prefix, because r3 is hidden.
      const allRecs = [
        { id: 'r1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront',  savings: 100, upfront_cost: 0 },
        { id: 'r2', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'all-upfront', savings: 150, upfront_cost: 1000 },
        { id: 'r3', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront',  savings: 200, upfront_cost: 0 },
        { id: 'r4', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, payment_option: 'all-upfront', savings: 250, upfront_cost: 2000 },
      ];
      const visibleRecs = allRecs.filter((r) => r.service === 'ec2'); // cell1 only

      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {}, recommendations: allRecs, regions: [],
      });
      (state.getRecommendations as jest.Mock).mockReturnValue(allRecs);
      // Visible set is cell1 only — column filter hides rds rows.
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(visibleRecs);
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        service: { kind: 'set', values: ['ec2'] },
      });
      // r3 is selected, but it is NOT in the visible set.
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['r3']));

      await loadRecommendations();

      const summary = document.getElementById('recommendations-summary');
      const savingsCard = Array.from(summary?.querySelectorAll('.card') ?? [])
        .find((c) => /Monthly Savings/.test(c.querySelector('h3')?.textContent ?? ''));
      const savingsValue = savingsCard?.querySelector('.value')?.textContent ?? '';
      const savingsTitle = savingsCard?.querySelector('h3')?.textContent ?? '';

      // Cards reflect the visible set (cell1: $100–$150), not the hidden selection.
      expect(savingsValue).toContain('$100');
      expect(savingsValue).toContain('$150');
      expect(savingsValue).not.toContain('$200');
      // Title must not flip to "Selected …" since no visible row is selected.
      expect(savingsTitle).toBe('Potential Monthly Savings');

      // Live line must not prefix "1 selected" because r3 is hidden.
      const liveText = document.querySelector('.recommendations-filter-live')?.textContent ?? '';
      expect(liveText).not.toMatch(/selected/);
      expect(liveText).toMatch(/Showing/);

      // Reset mocks so this filter doesn't leak into subsequent tests.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue([]);
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    });

    test('re-render after a sort click keeps the loaded total in "Showing X of Y" (closes #287)', async () => {
      const allRecs = [
        { id: 'r1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront', savings: 100, upfront_cost: 0 },
        { id: 'r2', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3', region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront', savings: 200, upfront_cost: 0 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: allRecs, regions: [] });
      (state.getRecommendations as jest.Mock).mockReturnValue(allRecs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue([allRecs[0]]);
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({ service: { kind: 'set', values: ['ec2'] } });
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());

      await loadRecommendations();
      const liveText = (): string =>
        document.querySelector('.recommendations-filter-live')?.textContent ?? '';
      expect(liveText()).toMatch(/Showing 1 of 2 recommendations$/);

      document.querySelector<HTMLElement>('th.sortable')?.click();
      expect(liveText()).toMatch(/Showing 1 of 2 recommendations$/);

      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue([]);
    });

    test.each([
      ['select-all', '#select-all-recs'],
      ['row selection', 'input[data-rec-id]'],
    ])('%s re-render keeps the loaded total in "Showing X of Y" (closes #287)', async (_name, selector) => {
      const allRecs = [
        { id: 'r1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront', savings: 100, upfront_cost: 0 },
        { id: 'r2', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3', region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront', savings: 200, upfront_cost: 0 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: allRecs, regions: [] });
      (state.getRecommendations as jest.Mock).mockReturnValue(allRecs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue([allRecs[0]]);
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({ service: { kind: 'set', values: ['ec2'] } });
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());

      await loadRecommendations();
      const liveText = (): string =>
        document.querySelector('.recommendations-filter-live')?.textContent ?? '';
      expect(liveText()).toMatch(/Showing 1 of 2 recommendations$/);

      const box = document.querySelector<HTMLInputElement>(selector)!;
      box.checked = true;
      box.dispatchEvent(new Event('change', { bubbles: true }));
      expect(liveText()).toMatch(/Showing 1 of 2 recommendations$/);

      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue([]);
    });

    test('renders recommendations list', async () => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [
          { id: 'rec-1', provider: 'aws',
            service: 'ec2',
            resource_type: 't3.medium',
            region: 'us-east-1',
            count: 5,
            term: 1,
            savings: 100,
            upfront_cost: 500
          }
        ],
        regions: ['us-east-1', 'us-west-2']
      });

      await loadRecommendations();

      const list = document.getElementById('recommendations-list');
      expect(list?.innerHTML).toContain('table');
      expect(list?.innerHTML).toContain('ec2');
      expect(list?.innerHTML).toContain('t3.medium');
      expect(list?.innerHTML).toContain('us-east-1');
    });

    // #219 end-to-end render regression. Feeds the REAL API shape the fixed
    // backend now emits: top-level vcpu/memory_gb (decoded from the nested
    // ComputeDetails by buildRecommendationsResponse). Asserts the Capacity
    // cell renders "8 vCPU / 32 GB" for a sized compute rec and "—" for a rec
    // with neither field. Pre-fix, the backend never emitted these top-level
    // fields, so the cell rendered "—" for every row in production while the
    // formatCapacity unit tests stayed green on injected values the API never
    // produced — this test ties the rendered cell to the real response shape.
    test('renders Capacity cell from top-level vcpu/memory_gb (#219)', async () => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [
          { id: 'rec-cap', provider: 'aws', service: 'ec2',
            resource_type: 'm5.2xlarge', region: 'us-east-1',
            count: 1, term: 1, savings: 100, upfront_cost: 500,
            vcpu: 8, memory_gb: 32 },
          { id: 'rec-nocap', provider: 'aws', service: 'rds',
            resource_type: 'db.t3.medium', region: 'us-east-1',
            count: 1, term: 1, savings: 50, upfront_cost: 200 },
        ],
        regions: ['us-east-1'],
      });

      await loadRecommendations();

      const list = document.getElementById('recommendations-list');
      // The capacity column is the cell immediately after resource_type.
      const capCell = (recId: string): string | undefined => {
        const checkbox = list?.querySelector<HTMLElement>(`input[data-rec-id="${recId}"]`);
        const row = checkbox?.closest('tr');
        const cells = Array.from(row?.querySelectorAll('td') ?? []);
        const rtIdx = cells.findIndex((td) => td.textContent?.includes(recId === 'rec-cap' ? 'm5.2xlarge' : 'db.t3.medium'));
        return cells[rtIdx + 1]?.textContent ?? undefined;
      };

      expect(capCell('rec-cap')).toBe('8 vCPU / 32 GB');
      // Rec with no vcpu/memory_gb renders the em-dash placeholder, not "0".
      expect(capCell('rec-nocap')).toBe('—');
    });

    test('shows the first-run empty state when nothing is loaded and no filter is set', async () => {
      // Issue #756: zero loaded rows with no column filter is a first run, not
      // a filter miss, so the "No rows match these filters." table hint
      // (issue #700, still used for filtered-to-zero) is replaced by an
      // onboarding card. The filtered case is covered by the #700 test below.
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: []
      });

      await loadRecommendations();

      const list = document.getElementById('recommendations-list');
      expect(list?.querySelector('thead')).toBeNull();
      expect(list?.querySelector('.first-run-empty-state')).not.toBeNull();
      expect(list?.textContent).not.toMatch(/No rows match/);
    });

    test('stores recommendations in state', async () => {
      const mockRecs = [
        { id: 'rec-2', provider: 'aws', service: 'ec2', savings: 100 }
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: mockRecs,
        regions: []
      });

      await loadRecommendations();

      expect(state.setRecommendations).toHaveBeenCalledWith(mockRecs);
    });

    test('clears selected recommendations on load', async () => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: []
      });

      await loadRecommendations();

      expect(state.clearSelectedRecommendations).toHaveBeenCalled();
    });

    test('shows error on API failure', async () => {
      (api.getRecommendations as jest.Mock).mockRejectedValue(new Error('API Error'));
      console.error = jest.fn();

      await loadRecommendations();

      const list = document.getElementById('recommendations-list');
      expect(list?.innerHTML).toContain('Failed to load recommendations');
    });

    test('renders select-all checkbox', async () => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [
          { id: 'rec-3', provider: 'aws', service: 'ec2', savings: 100 }
        ],
        regions: []
      });

      await loadRecommendations();

      const selectAll = document.getElementById('select-all-recs');
      expect(selectAll).toBeTruthy();
    });

    test('highlights selected recommendations', async () => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [
          { id: 'rec-sel', provider: 'aws', service: 'ec2', savings: 100 }
        ],
        regions: []
      });
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['rec-sel']));

      await loadRecommendations();

      const list = document.getElementById('recommendations-list');
      expect(list?.innerHTML).toContain('selected');
    });

    test('applies high-savings class for large savings', async () => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [
          { id: 'rec-5', provider: 'aws', service: 'ec2', savings: 2000 }
        ],
        regions: []
      });

      await loadRecommendations();

      const list = document.getElementById('recommendations-list');
      expect(list?.innerHTML).toContain('high-savings');
    });

    test('select-all checkbox selects all recommendations when checked', async () => {
      const mockRecs = [
        { id: 'rec-6', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
        { id: 'rec-7', provider: 'aws', service: 'rds', resource_type: 'db.t3.medium', region: 'us-east-1', count: 2, term: 1, savings: 200, upfront_cost: 1000 }
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: mockRecs,
        regions: []
      });

      await loadRecommendations();

      const selectAll = document.getElementById('select-all-recs') as HTMLInputElement;
      selectAll.checked = true;
      selectAll.dispatchEvent(new Event('change'));

      expect(state.addSelectedRecommendation).toHaveBeenCalledWith(expect.stringMatching(/^rec-/));
      expect((state.addSelectedRecommendation as jest.Mock).mock.calls.length).toBeGreaterThanOrEqual(2);
    });

    test('select-all checkbox clears all recommendations when unchecked', async () => {
      const mockRecs = [
        { id: 'rec-8', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 }
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: mockRecs,
        regions: []
      });

      await loadRecommendations();

      const selectAll = document.getElementById('select-all-recs') as HTMLInputElement;
      selectAll.checked = false;
      selectAll.dispatchEvent(new Event('change'));

      expect(state.clearSelectedRecommendations).toHaveBeenCalled();
    });
  });
});
