/**
 * Recommendations module tests: loadRecommendations fetch and filter tests.
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

    test('fetches with provider/account_ids when no column filter is set', async () => {
      // No column filters active: service and region are absent from the
      // API call (undefined); provider + account_ids carry the only hints.
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: []
      });

      await loadRecommendations();

      expect(api.getRecommendations).toHaveBeenCalledWith({
        provider: 'all',
        account_ids: undefined,
        service: undefined,
        region: undefined,
      });
    });

    test('pushes single-value service filter to API params (issue #162)', async () => {
      // A single-value service column filter must be forwarded to the API
      // so the backend applies it in SQL, saving bandwidth on large tenants.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        service: { kind: 'set', values: ['ec2'] },
      });
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: [],
      });

      await loadRecommendations();

      expect(api.getRecommendations).toHaveBeenCalledWith(
        expect.objectContaining({ service: 'ec2' }),
      );
    });

    test('pushes single-value region filter to API params (issue #162)', async () => {
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        region: { kind: 'set', values: ['us-east-1'] },
      });
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: [],
      });

      await loadRecommendations();

      expect(api.getRecommendations).toHaveBeenCalledWith(
        expect.objectContaining({ region: 'us-east-1' }),
      );
    });

    test('does not push multi-value service filter to API (stays client-side)', async () => {
      // When the user has selected multiple services the server only supports
      // equality (single-value); fall back to client-side filtering.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        service: { kind: 'set', values: ['ec2', 'rds'] },
      });
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: [],
      });

      await loadRecommendations();

      const call = (api.getRecommendations as jest.Mock).mock.calls[0][0];
      expect(call.service).toBeUndefined();
    });

    test('renders recommendations summary', async () => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {
          total_count: 10,
          total_monthly_savings: 5000,
          total_upfront_cost: 10000,
          avg_payback_months: 2
        },
        recommendations: [],
        regions: []
      });

      await loadRecommendations();

      const summary = document.getElementById('recommendations-summary');
      expect(summary?.innerHTML).toContain('Total Recommendations');
      expect(summary?.innerHTML).toContain('Potential Monthly Savings');
      expect(summary?.innerHTML).toContain('Total Upfront Cost');
      expect(summary?.innerHTML).toContain('Payback Period');
    });

    test('Potential Monthly Savings card mirrors page-level range, not the API sum (#272)', async () => {
      // Two cells, two variants per cell. The cells share (provider, account,
      // service, resource_type, region, term, engine) within each group; the
      // variants differ by payment_option. The user can only buy one variant
      // per cell, so the achievable monthly savings is bounded by:
      //   min = sum of per-cell savingsMin = 100 + 200 = 300
      //   max = sum of per-cell savingsMax = 150 + 250 = 400
      // The API's total_monthly_savings sums all 4 variants (= 700), which
      // overstates achievable savings by ~75% on this 2-cell page. The card
      // must NOT render 700; it must render the range "$300 – $400".
      const recs = [
        { id: 'cell1-cheap',  provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront',     savings: 100, upfront_cost: 0 },
        { id: 'cell1-pricey', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'all-upfront',    savings: 150, upfront_cost: 1000 },
        { id: 'cell2-cheap',  provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront',     savings: 200, upfront_cost: 0 },
        { id: 'cell2-pricey', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, payment_option: 'all-upfront',    savings: 250, upfront_cost: 2000 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: { total_count: 4, total_monthly_savings: 700, total_upfront_cost: 3000, avg_payback_months: 1 },
        recommendations: recs,
        regions: [],
      });
      // The summary card now reads the visible set via
      // state.getVisibleRecommendations() (so it stays in sync with
      // the banner under filter changes — see the next test for the
      // filter-divergence case). On an unfiltered initial render the
      // visible set equals the loaded set; mock it explicitly because
      // setVisibleRecommendations() inside renderRecommendationsList
      // is a no-op when state is mocked.
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

      await loadRecommendations();

      const summary = document.getElementById('recommendations-summary');
      const savingsCard = Array.from(summary?.querySelectorAll('.card') ?? [])
        .find((c) => c.querySelector('h3')?.textContent === 'Potential Monthly Savings');
      const value = savingsCard?.querySelector('.value.savings')?.textContent ?? '';
      // The page-level range is $300 – $400/mo; the API's flat 700 must not
      // appear on the card.
      expect(value).toContain('$300');
      expect(value).toContain('$400');
      expect(value).not.toContain('$700');
    });

    test('savings card recomputes from visible set on filter change (#272 CR follow-up)', async () => {
      // Same 4-rec / 2-cell setup as above. Initial render: card shows
      // $300 – $400 (range across both cells). Then we apply a real
      // column filter (service = "ec2") via the state mock so the
      // applyColumnFilters() call inside renderRecommendationsList
      // narrows the visible set to cell1's two variants, and trigger
      // a rerender via a sortable-header click. The card MUST recompute
      // to cell1's range ($100 – $150) — otherwise it stays pinned at
      // the unfiltered $300 – $400 and diverges from the banner under
      // the table, which is exactly the CR finding on #276 we're
      // closing.
      const allRecs = [
        { id: 'cell1-cheap',  provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront',  savings: 100, upfront_cost: 0 },
        { id: 'cell1-pricey', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'all-upfront', savings: 150, upfront_cost: 1000 },
        { id: 'cell2-cheap',  provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront',  savings: 200, upfront_cost: 0 },
        { id: 'cell2-pricey', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, payment_option: 'all-upfront', savings: 250, upfront_cost: 2000 },
      ];

      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: { total_count: 4, total_monthly_savings: 700, total_upfront_cost: 3000, avg_payback_months: 1 },
        recommendations: allRecs,
        regions: [],
      });
      (state.getRecommendations as jest.Mock).mockReturnValue(allRecs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(allRecs);
      // No column filters initially — applyColumnFilters returns the input
      // clone so all 4 recs reach the summary recompute.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});

      await loadRecommendations();

      const cardValue = (): string => {
        const summary = document.getElementById('recommendations-summary');
        const card = Array.from(summary?.querySelectorAll('.card') ?? [])
          .find((c) => c.querySelector('h3')?.textContent === 'Potential Monthly Savings');
        return card?.querySelector('.value.savings')?.textContent ?? '';
      };
      expect(cardValue()).toContain('$300');
      expect(cardValue()).toContain('$400');
      // #180: unfiltered, the card covers every loaded row (same scope as
      // Home), so it carries no "Filtered" scope line.
      const scopeLine = (): string | null =>
        document.querySelector('#recommendations-summary [data-testid="savings-scope"]')?.textContent ?? null;
      expect(scopeLine()).toBeNull();

      // Apply a real column filter restricting to service=ec2 (cell1
      // only). renderRecommendationsList re-runs applyColumnFilters
      // against the loaded set on every entry, so a sort-header click
      // triggers the narrowing pipeline end-to-end.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        service: { kind: 'set', values: ['ec2'] },
      });
      const sortableHeader = document.querySelector<HTMLTableCellElement>('th[data-sort="service"]');
      sortableHeader?.click();

      // Card must recompute to cell1's range ($100 – $150) — NOT stay
      // pinned at the loaded-set range ($300 – $400).
      expect(cardValue()).toContain('$100');
      expect(cardValue()).toContain('$150');
      expect(cardValue()).not.toContain('$300');
      expect(cardValue()).not.toContain('$400');
      // #180: the narrowed scope is labeled so it can't be mistaken for
      // Home's all-recommendations figure.
      expect(scopeLine()).toBe('Filtered: 2 of 4 recommendations');

      // Restore the filter mock to empty so the leaked return value
      // doesn't bleed into subsequent tests in this file. jest.clearAllMocks
      // resets mock.calls but not .mockReturnValue, so we have to undo
      // explicitly.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    });

    test('savings card is labeled Filtered when the service filter is pushed to the server (#180)', async () => {
      // A single-value service filter is sent as ?service= (#162) and the
      // backend returns only matching rows, so the visible and loaded counts
      // are equal. The card must still say it is filtered; otherwise it shows
      // a smaller total than Home with no explanation.
      const ec2Recs = [
        { id: 'cell1-cheap',  provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront',  savings: 100, upfront_cost: 0 },
        { id: 'cell1-pricey', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'all-upfront', savings: 150, upfront_cost: 1000 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: { total_count: 2, total_monthly_savings: 250, total_upfront_cost: 1000, avg_payback_months: 1 },
        recommendations: ec2Recs,
        regions: [],
      });
      (state.getRecommendations as jest.Mock).mockReturnValue(ec2Recs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(ec2Recs);
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        service: { kind: 'set', values: ['ec2'] },
      });
      const scopeLine = (): string | null =>
        document.querySelector('#recommendations-summary [data-testid="savings-scope"]')?.textContent ?? null;

      try {
        await loadRecommendations();

        expect(api.getRecommendations).toHaveBeenLastCalledWith(expect.objectContaining({ service: 'ec2' }));
        // #1683: the pushed-down service filter is named like the Service column.
        expect(scopeLine()).toBe('Filtered: 2 recommendations (Service: EC2)');

        // A further client-side filter is counted, not named.
        (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
          service: { kind: 'set', values: ['ec2'] },
          term: { kind: 'set', values: ['1'] },
        });
        document.querySelector<HTMLTableCellElement>('th[data-sort="service"]')?.click();
        expect(scopeLine()).toBe('Filtered: 2 recommendations (Service: EC2, +1 more filter)');
      } finally {
        // Reload unfiltered so the pushed-down scope doesn't leak into later tests.
        (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
        await loadRecommendations();
      }
      expect(scopeLine()).toBeNull();
    });

    test('summary cards narrow to selection in real time (closes #279)', async () => {
      // 4 recs / 2 cells. With no selection: cards reflect the full
      // visible set (savings $300–$400, upfront $0–$3000). After ticking
      // cell1's pricey variant, cards narrow to that single rec
      // ($150 savings / $1000 upfront). Tick the second cell's cheap
      // variant too: cards sum the two selected variants ($350 savings /
      // $1000 upfront). Cards must update on every selection toggle —
      // the selection-toggle handler triggers a list rerender, which
      // calls renderRecommendationsSummary with the new selection.
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

      const cardValue = (titlePattern: RegExp): string => {
        const summary = document.getElementById('recommendations-summary');
        const card = Array.from(summary?.querySelectorAll('.card') ?? [])
          .find((c) => titlePattern.test(c.querySelector('h3')?.textContent ?? ''));
        return card?.querySelector('.value')?.textContent ?? '';
      };
      const cardTitle = (valuePattern: RegExp): string => {
        const summary = document.getElementById('recommendations-summary');
        const card = Array.from(summary?.querySelectorAll('.card') ?? [])
          .find((c) => valuePattern.test(c.querySelector('.value')?.textContent ?? ''));
        return card?.querySelector('h3')?.textContent ?? '';
      };

      // No selection: cards reflect the full visible set.
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
      await loadRecommendations();
      expect(cardValue(/Recommendations/)).toBe('4'); // 4 variants (closes #748: KPI now matches "Showing of")
      expect(cardValue(/Monthly Savings/)).toMatch(/\$300\b/);
      expect(cardValue(/Monthly Savings/)).toMatch(/\$400\b/);
      expect(cardValue(/Upfront/)).toMatch(/\$0\b/);
      expect(cardValue(/Upfront/)).toMatch(/\$3,?000\b/);
      // Card titles reflect the "all visible" mode.
      expect(cardTitle(/^4$/)).toBe('Total Recommendations');

      // Tick one cell's pricey variant: cards narrow to that one rec.
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['cell1-pricey']));
      await loadRecommendations();
      expect(cardValue(/Recommendations/)).toBe('1');
      // Single rec → range collapses to a single value.
      expect(cardValue(/Monthly Savings/)).toMatch(/^\$150$/);
      expect(cardValue(/Upfront/)).toMatch(/^\$1,?000$/);
      // Title switches to "Selected ..." form.
      expect(cardTitle(/^1$/)).toBe('Selected Recommendations');

      // Tick a second cell: cards sum the two selected variants.
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['cell1-pricey', 'cell2-cheap']));
      await loadRecommendations();
      expect(cardValue(/Recommendations/)).toBe('2');
      expect(cardValue(/Monthly Savings/)).toMatch(/^\$350$/); // 150 + 200
      expect(cardValue(/Upfront/)).toMatch(/^\$1,?000$/); // 1000 + 0
    });
  });
});
