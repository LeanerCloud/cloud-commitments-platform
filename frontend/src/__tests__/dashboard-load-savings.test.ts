/**
 * Dashboard module tests: loadDashboard savings card, coverage KPI and recommendation shape tests.
 * Split from dashboard.test.ts to stay under the 500-line ceiling; the
 * module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, mockConfirmDialog, mockGroupRecsByCell, mockPageLevelRange, mockFormatSavingsRange } from './dashboard-harness';
import { loadDashboard } from '../dashboard';
import * as api from '../api';

describe('Dashboard Module', () => {
  beforeEach(() => {
    // Reset DOM
    document.body.innerHTML = `
      <div id="summary"></div>
      <section id="savings-by-service-section"><h3>Potential savings range per service</h3>
        <canvas id="savings-by-service-chart"></canvas>
        <p id="savings-by-service-empty" class="empty hidden"></p>
      </section>
      <div id="upcoming-list"></div>
    `;

    jest.clearAllMocks();
    window.alert = jest.fn();
    window.confirm = jest.fn().mockReturnValue(true);
    mockShowToast.mockClear();
    mockConfirmDialog.mockReset();
    mockConfirmDialog.mockImplementation(() => Promise.resolve(true));
    // Re-initialize recommendation mocks after clearAllMocks().
    mockGroupRecsByCell.mockImplementation((recs: unknown[]) => new Map(recs.length ? [['cell-1', recs]] : []));
    mockPageLevelRange.mockImplementation((groups: Map<string, unknown[]>) => {
      if (groups.size === 0) return { savingsMin: 0, savingsMax: 0, cellCount: 0 };
      return { savingsMin: 300, savingsMax: 400, cellCount: groups.size };
    });
    mockFormatSavingsRange.mockImplementation((min: number, max: number) => min === max ? `$${min}` : `$${min} – $${max}`);
    // Default: empty recs so existing tests are unaffected by the range.
    (api.getRecommendations as jest.Mock).mockResolvedValue([]);
  });

  describe('loadDashboard', () => {
    // #293: Potential Monthly Savings card renders per-cell range, not
    // the flat summary.potential_monthly_savings value.
    test('Potential Monthly Savings card renders per-cell range (#293)', async () => {
      // Two cells, four variants — flat sum would be 700 but per-cell range
      // is $300 – $400. The mock helpers are already configured to return
      // savingsMin=300, savingsMax=400 when recs is non-empty.
      const mockRecs = [
        { id: 'r1', provider: 'aws', service: 'ec2', region: 'us-east-1', resource_type: 't3.medium', term: 1, savings: 150, upfront_cost: 0, count: 1 },
        { id: 'r2', provider: 'aws', service: 'ec2', region: 'us-east-1', resource_type: 't3.medium', term: 3, savings: 200, upfront_cost: 100, count: 1 },
        { id: 'r3', provider: 'aws', service: 'rds', region: 'us-east-1', resource_type: 'db.t3.medium', term: 1, savings: 100, upfront_cost: 0, count: 1 },
        { id: 'r4', provider: 'aws', service: 'rds', region: 'us-east-1', resource_type: 'db.t3.medium', term: 3, savings: 250, upfront_cost: 200, count: 1 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue(mockRecs);
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 700, // flat (overcounted) sum — must NOT appear
        total_recommendations: 4,
        active_commitments: 0,
        committed_monthly: 0,
        current_coverage: 0,
        target_coverage: 80,
        ytd_savings: 0,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadDashboard();

      const savingsCard = document.querySelector('#summary .card');
      expect(savingsCard?.textContent).toContain('$300');
      expect(savingsCard?.textContent).toContain('$400');
      // The overcounted flat sum must NOT appear in the savings card.
      expect(savingsCard?.textContent).not.toContain('$700');
    });

    // #180: Home and Opportunities must sum the same rows, so Home applies the
    // same Enabled Providers filter and counts the rows it summed.
    describe('Enabled Providers scope parity with Opportunities (#180)', () => {
      const scopedRecs = [
        { id: 'a1', provider: 'aws', service: 'ec2', region: 'us-east-1', resource_type: 't3.medium', term: 1, savings: 150, upfront_cost: 0, count: 1 },
        { id: 'a2', provider: 'aws', service: 'rds', region: 'us-east-1', resource_type: 'db.t3.medium', term: 1, savings: 100, upfront_cost: 0, count: 1 },
        { id: 'z1', provider: 'azure', service: 'aks', region: 'eastus', resource_type: 'Standard_D2_v3', term: 1, savings: 90, upfront_cost: 0, count: 1 },
      ];
      const summaryFor = (total: number) => ({
        potential_monthly_savings: 0, total_recommendations: total, active_commitments: 0,
        committed_monthly: 0, current_coverage: 0, target_coverage: 80, ytd_savings: 0, by_service: {},
      });
      const savingsTile = (): Element | null => document.querySelector('[data-kpi="savings"]');
      const rangeInputIds = (): string[] =>
        (mockGroupRecsByCell.mock.calls[0]![0] as Array<{ id: string }>).map((r) => r.id);

      beforeEach(() => {
        (api.getRecommendations as jest.Mock).mockResolvedValue(scopedRecs);
        (api.getDashboardSummary as jest.Mock).mockResolvedValue(summaryFor(3));
        (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      });

      test('drops recommendations for disabled providers before computing the range', async () => {
        (api.getConfig as jest.Mock).mockResolvedValueOnce({ global: { enabled_providers: ['aws'] } });

        await loadDashboard();

        expect(rangeInputIds()).toEqual(['a1', 'a2']);
        // The count matches the rows summed, not the backend total (3).
        expect(savingsTile()?.querySelector('.kpi-tile-detail')?.textContent).toBe('2 recommendations');
      });

      test('headline tile says the range is unscaled by the coverage cap (#1946)', async () => {
        await loadDashboard();

        expect(savingsTile()?.getAttribute('title'))
          .toBe("Unscaled by the coverage cap: sums each cell's known-savings variants.");
      });

      test('empty enabled_providers keeps every recommendation (permissive default)', async () => {
        (api.getConfig as jest.Mock).mockResolvedValueOnce({ global: { enabled_providers: [] } });

        await loadDashboard();

        expect(rangeInputIds()).toEqual(['a1', 'a2', 'z1']);
        expect(savingsTile()?.querySelector('.kpi-tile-detail')?.textContent).toBe('3 recommendations');
      });

      test('a config fetch failure does not block the dashboard and applies no filter', async () => {
        (api.getConfig as jest.Mock).mockRejectedValueOnce(new Error('403 Forbidden'));

        await loadDashboard();

        expect(rangeInputIds()).toEqual(['a1', 'a2', 'z1']);
        expect(savingsTile()?.querySelector('.kpi-tile-value')?.textContent).toBe('$300 – $400');
      });

      test('falls back to the backend count when the recommendations fetch fails', async () => {
        (api.getRecommendations as jest.Mock).mockRejectedValueOnce(new Error('boom'));
        (api.getDashboardSummary as jest.Mock).mockResolvedValue(summaryFor(7));

        await loadDashboard();

        expect(savingsTile()?.querySelector('.kpi-tile-value')?.textContent).toBe('\u2014');
        expect(savingsTile()?.querySelector('.kpi-tile-detail')?.textContent).toBe('7 recommendations');
      });
    });

    // #978: Current Coverage KPI must render with exactly 1 decimal place so it
    // cannot overflow the tile box at narrower screen widths.
    test('#978: Current Coverage KPI rounds to 1 decimal place', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 0,
        total_recommendations: 3,
        active_commitments: 2,
        committed_monthly: 100,
        // A long-fractional value that previously overflowed the tile box.
        current_coverage: 72.3456789012,
        target_coverage: 80,
        ytd_savings: 0,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadDashboard();

      const coverageTile = document.querySelector('[data-kpi="coverage"]');
      const valueEl = coverageTile?.querySelector('.kpi-tile-value');
      // Must display exactly 1 decimal place (e.g. "72.3%"), not the raw float.
      expect(valueEl?.textContent).toBe('72.3%');
      // The raw long-decimal form must not appear.
      expect(valueEl?.textContent).not.toContain('72.3456');
    });

    // Integer coverage values must still format cleanly with one decimal place.
    test('#978: integer Current Coverage KPI renders with one decimal place', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 0,
        total_recommendations: 1,
        active_commitments: 1,
        committed_monthly: 50,
        current_coverage: 70,
        target_coverage: 80,
        ytd_savings: 0,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadDashboard();

      const coverageTile = document.querySelector('[data-kpi="coverage"]');
      const valueEl = coverageTile?.querySelector('.kpi-tile-value');
      expect(valueEl?.textContent).toBe('70.0%');
    });
  });
});
