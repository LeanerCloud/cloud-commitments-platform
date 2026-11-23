/**
 * Dashboard module tests: setupDashboardHandlers filter chip subscription tests.
 * Split from dashboard.test.ts to stay under the 500-line ceiling; the
 * module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, mockConfirmDialog, mockGroupRecsByCell, mockPageLevelRange, mockFormatSavingsRange, resetDashboardState } from './dashboard-harness';
import { loadSavingsTrendChart, setupDashboardHandlers } from '../dashboard';
import * as api from '../api';
import * as state from '../state';

describe('Dashboard Module', () => {
  beforeEach(() => {
    resetDashboardState();
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

  // QA row 384 step 2.3: Home page Savings-over-time chart must honor the
  // global Account filter (issue #701). These tests verify that:
  //   1. setupDashboardHandlers subscribes to both filter chips.
  //   2. An account-chip change re-fetches when Home tab is active.
  //   3. No re-fetch fires when Home tab is inactive.
  //   4. Back-to-back provider+account fires coalesce into one reload.
  //   5. The account_id is forwarded to getSavingsAnalytics.
  //   6. Filter-aware empty-state copy is shown when a filter is active.
  describe('setupDashboardHandlers — filter chip subscriptions (QA 2.3)', () => {
    function addHomeTab(active: boolean): void {
      const div = document.createElement('div');
      div.id = 'home-tab';
      if (active) div.classList.add('active');
      document.body.appendChild(div);
    }

    beforeEach(() => {
      (state.subscribeProvider as jest.Mock).mockClear();
      (state.subscribeAccount as jest.Mock).mockClear();
      (api.getSavingsAnalytics as jest.Mock).mockClear();
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 0, total_recommendations: 0,
        active_commitments: 0, committed_monthly: 0, current_coverage: 0,
        target_coverage: 80, ytd_savings: 0, by_service: {},
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (api.getRecommendations as jest.Mock).mockResolvedValue([]);
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      // Ensure a canvas for savings-trend-chart exists so loadSavingsTrendChart
      // does not bail out at the early-return guard.
      const canvas = document.createElement('canvas');
      canvas.id = 'savings-trend-chart';
      const empty = document.createElement('p');
      empty.id = 'savings-trend-empty';
      empty.className = 'hidden';
      document.body.appendChild(canvas);
      document.body.appendChild(empty);
    });

    test('registers one callback each with subscribeProvider and subscribeAccount', () => {
      setupDashboardHandlers();

      expect(state.subscribeProvider).toHaveBeenCalledTimes(1);
      expect(state.subscribeAccount).toHaveBeenCalledTimes(1);
      expect(typeof (state.subscribeProvider as jest.Mock).mock.calls[0]?.[0]).toBe('function');
      expect(typeof (state.subscribeAccount as jest.Mock).mock.calls[0]?.[0]).toBe('function');
    });

    test('account chip change triggers loadDashboard (and getSavingsAnalytics) when home tab is active', async () => {
      addHomeTab(true);
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue(['uuid-acct-1']);

      setupDashboardHandlers();
      const accountCb = (state.subscribeAccount as jest.Mock).mock.calls[0]?.[0] as () => void;

      (api.getSavingsAnalytics as jest.Mock).mockClear();
      accountCb();
      await new Promise((r) => setTimeout(r, 0));

      // Two scoped series: the trend chart and the YTD tile sparkline (#317).
      expect(api.getSavingsAnalytics).toHaveBeenCalledTimes(2);
      for (const [filters] of (api.getSavingsAnalytics as jest.Mock).mock.calls) {
        expect(filters).toEqual(expect.objectContaining({ account_ids: ['uuid-acct-1'] }));
      }
    });

    test('does NOT fire when home tab is inactive (active-tab guard)', async () => {
      addHomeTab(false);

      setupDashboardHandlers();
      const accountCb = (state.subscribeAccount as jest.Mock).mock.calls[0]?.[0] as () => void;
      const providerCb = (state.subscribeProvider as jest.Mock).mock.calls[0]?.[0] as () => void;

      (api.getSavingsAnalytics as jest.Mock).mockClear();
      (api.getDashboardSummary as jest.Mock).mockClear();
      accountCb();
      providerCb();
      await new Promise((r) => setTimeout(r, 0));

      expect(api.getDashboardSummary).not.toHaveBeenCalled();
    });

    test('back-to-back provider+account fires coalesce into one reload', async () => {
      addHomeTab(true);

      setupDashboardHandlers();
      const providerCb = (state.subscribeProvider as jest.Mock).mock.calls[0]?.[0] as () => void;
      const accountCb = (state.subscribeAccount as jest.Mock).mock.calls[0]?.[0] as () => void;

      (api.getDashboardSummary as jest.Mock).mockClear();
      // Simulate topbar provider-change: clears accounts then sets provider,
      // per the #185 ordering rule — both fire synchronously.
      accountCb();
      providerCb();
      await new Promise((r) => setTimeout(r, 0));

      expect(api.getDashboardSummary).toHaveBeenCalledTimes(1);
    });

    test('loadSavingsTrendChart forwards account_id to the analytics API', async () => {
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue(['uuid-acct-2']);

      await loadSavingsTrendChart();

      expect(api.getSavingsAnalytics).toHaveBeenCalledWith(
        expect.objectContaining({ account_ids: ['uuid-acct-2'] })
      );
    });

    test('loadSavingsTrendChart forwards account_ids for multi-account filter (filter parity with KPI tiles)', async () => {
      // Regression: previously the chart omitted account_ids when length > 1,
      // causing the chart data to diverge from the KPI tiles above it which
      // always forward all selected accounts. The fix passes account_ids
      // unconditionally when any accounts are selected.
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue(['uuid-a', 'uuid-b']);
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: [{ timestamp: new Date().toISOString(), cumulative_savings: 50, total_savings: 5, total_upfront: 100, purchase_count: 1 }],
      });

      await loadSavingsTrendChart();

      expect(api.getSavingsAnalytics).toHaveBeenCalledWith(
        expect.objectContaining({ account_ids: ['uuid-a', 'uuid-b'] })
      );
    });

    test('empty-state shows filter name when account chip is active (QA 2.3)', async () => {
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue(['uuid-acct-3']);
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      await loadSavingsTrendChart();

      const empty = document.getElementById('savings-trend-empty');
      expect(empty?.classList.contains('hidden')).toBe(false);
      expect(empty?.textContent).toContain('uuid-acct-3');
    });

    test('empty-state shows generic message when no filter is active', async () => {
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
      (state.getCurrentProvider as jest.Mock).mockReturnValue('');
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      await loadSavingsTrendChart();

      const empty = document.getElementById('savings-trend-empty');
      expect(empty?.classList.contains('hidden')).toBe(false);
      expect(empty?.textContent).toContain('No savings recorded in the last');
    });

    test('empty-state names the provider when a provider filter is active (issue #498, QA 2.3)', async () => {
      // The analytics endpoint now applies the provider param as a WHERE
      // filter, so the query IS scoped to the selected provider — naming it
      // in the empty-state is accurate. Mirrors the account-filter empty-state.
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
      (state.getCurrentProvider as jest.Mock).mockReturnValue('aws');
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      await loadSavingsTrendChart();

      const empty = document.getElementById('savings-trend-empty');
      expect(empty?.classList.contains('hidden')).toBe(false);
      // Provider name (uppercased by buildFilterDesc) appears in the message.
      expect(empty?.textContent).toContain('AWS');
      expect(empty?.textContent).toMatch(/No savings recorded in the last (\d+ days|year) for /);
    });

    test('loadSavingsTrendChart forwards the provider chip to the analytics API (issue #498, QA 2.3)', async () => {
      // Regression: previously the chart dropped the provider param, so the
      // savings-history series showed the same all-provider data regardless of
      // the selected provider. The fix forwards provider to scope the query.
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
      (state.getCurrentProvider as jest.Mock).mockReturnValue('aws');
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      await loadSavingsTrendChart();

      expect(api.getSavingsAnalytics).toHaveBeenCalledWith(
        expect.objectContaining({ provider: 'aws' })
      );
    });

    test('loadSavingsTrendChart omits provider when no provider filter is active', async () => {
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
      (state.getCurrentProvider as jest.Mock).mockReturnValue('');
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      await loadSavingsTrendChart();

      const call = (api.getSavingsAnalytics as jest.Mock).mock.calls[0]?.[0];
      expect(call).not.toHaveProperty('provider');
    });
  });
});
