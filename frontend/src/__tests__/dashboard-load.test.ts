/**
 * Dashboard module tests: loadDashboard summary, chart and upcoming list tests.
 * Split from dashboard.test.ts to stay under the 500-line ceiling; the
 * module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, mockConfirmDialog, mockGroupRecsByCell, mockPageLevelRange, mockFormatSavingsRange } from './dashboard-harness';
import { loadDashboard } from '../dashboard';
import { Chart } from 'chart.js';
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
    // #1709: committed_monthly sums the active commitments' estimated monthly
    // savings (handler_dashboard.go), not what they cost, so the card must not
    // call it "committed".
    test('Active Commitments detail names the figure as estimated savings', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        total_recommendations: 0, active_commitments: 3, committed_monthly: 500,
        current_coverage: 70, target_coverage: 80, ytd_savings: 0, by_service: {},
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadDashboard();

      const tile = document.querySelector('#summary [data-kpi="commitments"]');
      expect(tile?.textContent).toContain('$500/mo estimated savings');
      expect(tile?.textContent).not.toContain('committed');
    });

    test('fetches dashboard summary and upcoming purchases', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        total_recommendations: 5,
        active_commitments: 3,
        committed_monthly: 500,
        current_coverage: 70,
        target_coverage: 80,
        ytd_savings: 5000,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: []
      });

      await loadDashboard();

      // Issue #344 T2: AppState.currentProvider is `api.Provider | ''`,
      // not a literal 'all' — '' is the "All Providers" sentinel.
      expect(api.getDashboardSummary).toHaveBeenCalledWith('', []);
      expect(api.getUpcomingPurchases).toHaveBeenCalled();
    });

    // #1686: the YTD figure is monthly estimated savings times elapsed months,
    // not recorded savings, so its caption must say so.
    test('labels YTD Savings as an estimate', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        total_recommendations: 0, active_commitments: 1, committed_monthly: 10,
        current_coverage: 50, target_coverage: 80, ytd_savings: 5000, by_service: {},
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadDashboard();

      const ytd = document.querySelector('#summary [data-kpi="ytd"]') ?? document.getElementById('summary');
      expect(ytd?.textContent).toContain('Estimated from active commitments');
      expect(ytd?.textContent).not.toContain('From commitment purchases');
    });

    // #1686: the per-service card shares the KPI cards' loading treatment.
    test('shows a per-service loading skeleton until the chart renders', async () => {
      const loading = document.createElement('div');
      loading.id = 'savings-by-service-loading';
      document.getElementById('savings-by-service-section')?.appendChild(loading);
      let resolve!: (v: unknown) => void;
      (api.getDashboardSummary as jest.Mock).mockReturnValue(new Promise(r => { resolve = r; }));
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      const done = loadDashboard();
      expect(loading.querySelector('.skeleton')).not.toBeNull();

      resolve({ total_recommendations: 0, active_commitments: 0, by_service: {} });
      await done;
      expect(loading.querySelector('.skeleton')).toBeNull();
    });

    test('clears the per-service loading skeleton when the dashboard fails to load', async () => {
      const loading = document.createElement('div');
      loading.id = 'savings-by-service-loading';
      document.getElementById('savings-by-service-section')?.appendChild(loading);
      (api.getDashboardSummary as jest.Mock).mockRejectedValue(new Error('boom'));
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadDashboard();

      expect(loading.querySelector('.skeleton')).toBeNull();
      const empty = document.getElementById('savings-by-service-empty');
      expect(empty?.classList.contains('hidden')).toBe(false);
      expect(empty?.textContent).toBe('Potential savings could not be loaded. Reload the page to try again.');
    });

    test('renders dashboard summary', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        total_recommendations: 5,
        active_commitments: 3,
        committed_monthly: 500,
        current_coverage: 70,
        target_coverage: 80,
        ytd_savings: 5000,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: []
      });

      await loadDashboard();

      const summary = document.getElementById('summary');
      expect(summary?.innerHTML).toContain('Potential Monthly Savings');
      expect(summary?.innerHTML).toContain('Active Commitments');
      expect(summary?.innerHTML).toContain('Current Coverage');
      expect(summary?.innerHTML).toContain('YTD Savings');
    });

    test('formats fractional current_coverage with one decimal (not raw float)', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 0,
        total_recommendations: 1,
        active_commitments: 1,
        committed_monthly: 100,
        current_coverage: 0.14648736342042862,
        target_coverage: 80,
        ytd_savings: 0,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadDashboard();

      const summary = document.getElementById('summary');
      expect(summary?.innerHTML).toContain('0.1%');
      expect(summary?.innerHTML).not.toContain('0.14648736342042862');
    });

    test('renders the merged per-service savings chart', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        by_service: {
          ec2: { potential_savings: 500, current_savings: 200 },
          rds: { potential_savings: 300, current_savings: 100 }
        }
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: []
      });
      // Range chart is driven by recs; supply two ec2 options + one rds.
      (api.getRecommendations as jest.Mock).mockResolvedValue([
        { service: 'ec2', savings: 400, term: 1, payment: 'no-upfront' },
        { service: 'ec2', savings: 600, term: 3, payment: 'all-upfront' },
        { service: 'rds', savings: 300, term: 1, payment: 'no-upfront' },
      ]);

      await loadDashboard();

      expect(Chart).toHaveBeenCalled();
    });

    test('destroys existing merged chart before creating new one', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        by_service: { ec2: { potential_savings: 500, current_savings: 200 } }
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: []
      });
      (api.getRecommendations as jest.Mock).mockResolvedValue([
        { service: 'ec2', savings: 500, term: 1, payment: 'no-upfront' },
      ]);

      // First render builds a chart; second render must destroy it.
      await loadDashboard();
      const results = (Chart as unknown as jest.Mock).mock.results;
      const firstChart = results[results.length - 1]?.value as { destroy: jest.Mock };
      await loadDashboard();

      expect(firstChart.destroy).toHaveBeenCalled();
    });

    test('renders upcoming purchases', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: [
          {
            execution_id: 'exec-1', plan_id: 'plan-1',
            plan_name: 'Test Plan',
            provider: 'aws',
            service: 'ec2',
            step_number: 1,
            total_steps: 4,
            estimated_savings: 100,
            scheduled_date: '2024-02-15'
          }
        ]
      });

      await loadDashboard();

      const upcomingList = document.getElementById('upcoming-list');
      expect(upcomingList?.innerHTML).toContain('Test Plan');
      expect(upcomingList?.innerHTML).toContain('Step 1 of 4');
      expect(upcomingList?.innerHTML).toContain(' EC2 - Step 1 of 4');
      expect(upcomingList?.innerHTML).not.toContain('ec2');
    });

    test('shows an inline error with Retry, not the empty state, when the upcoming fetch fails (#713)', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockRejectedValueOnce(new Error('API error: 503 Service Unavailable'));

      await loadDashboard();

      const upcomingList = document.getElementById('upcoming-list');
      expect(upcomingList?.textContent).not.toContain('No upcoming scheduled purchases');
      expect(upcomingList?.querySelector('.error')?.textContent).toContain('Failed to load upcoming purchases');
      // The summary is unaffected by the upcoming failure.
      expect(document.getElementById('summary')?.querySelector('.error')).toBeNull();

      (api.getUpcomingPurchases as jest.Mock).mockResolvedValueOnce({
        purchases: [{
          execution_id: 'exec-1', plan_id: 'plan-1', plan_name: 'Retry Plan',
          scheduled_date: '2025-01-15', provider: 'aws', service: 'ec2',
          step_number: 1, total_steps: 2, estimated_savings: 120,
        }]
      });
      const retry = upcomingList?.querySelector('button[data-action="retry-upcoming"]') as HTMLButtonElement;
      expect(retry).not.toBeNull();
      retry.click();
      await new Promise(r => setTimeout(r, 0));

      expect(api.getUpcomingPurchases).toHaveBeenCalledTimes(2);
      expect(api.getDashboardSummary).toHaveBeenCalledTimes(1);
      expect(upcomingList?.innerHTML).toContain('Retry Plan');
      expect(upcomingList?.querySelector('.error')).toBeNull();
    });

    test('upcoming error is announced and Retry keeps keyboard focus in the widget (#713)', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({ potential_monthly_savings: 1000, by_service: {} });
      (api.getUpcomingPurchases as jest.Mock).mockRejectedValueOnce(new Error('API error: 503'));
      await loadDashboard();

      const upcomingList = document.getElementById('upcoming-list') as HTMLElement;
      expect(upcomingList.querySelector('.error')?.getAttribute('role')).toBe('alert');

      (api.getUpcomingPurchases as jest.Mock).mockResolvedValueOnce({ purchases: [] });
      const retry = upcomingList.querySelector('button[data-action="retry-upcoming"]') as HTMLButtonElement;
      retry.focus();
      retry.click();
      // Retry replaces its own button; focus must land on the container, not <body>.
      expect(upcomingList.getAttribute('tabindex')).toBe('-1');
      expect(document.activeElement).toBe(upcomingList);
      await new Promise(r => setTimeout(r, 0));
      expect(document.activeElement).toBe(upcomingList);
    });

    test('shows empty message when no upcoming purchases', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: []
      });

      await loadDashboard();

      const upcomingList = document.getElementById('upcoming-list');
      expect(upcomingList?.innerHTML).toContain('No upcoming scheduled purchases');
    });

    // #1189: the Go handler serializes an empty result as `purchases: null`
    // (nil slice), and apiRequest resolves null for an empty 2xx body (#304).
    test.each([
      ['purchases: null', { purchases: null }],
      ['a null response', null],
    ])('shows the empty message for %s', async (_label, value) => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({ potential_monthly_savings: 1000, by_service: {} });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue(value);

      await loadDashboard();

      const upcomingList = document.getElementById('upcoming-list');
      expect(upcomingList?.textContent).toContain('No upcoming scheduled purchases');
      expect(upcomingList?.querySelector('.error')).toBeNull();
    });

    test('shows error on API failure', async () => {
      (api.getDashboardSummary as jest.Mock).mockRejectedValue(new Error('API Error'));
      console.error = jest.fn();

      await loadDashboard();

      const summary = document.getElementById('summary');
      expect(summary?.innerHTML).toContain('Failed to load dashboard');
    });

    test('names an empty summary response instead of throwing a TypeError (#280)', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue(null);
      console.error = jest.fn();

      await loadDashboard();

      const text = document.getElementById('summary')?.textContent ?? '';
      expect(text).toContain('Dashboard summary response was empty');
      expect(text).not.toContain('Cannot read properties of null');
    });

    // Issue #344 T3: skeleton lifecycle — skeleton renders synchronously
    // at fetch start, then is replaced by the success render (clean
    // handoff) or torn down on error so it never sits next to a stale
    // error message.
    test('error path tears down the loading skeleton', async () => {
      (api.getDashboardSummary as jest.Mock).mockRejectedValue(new Error('API Error'));
      console.error = jest.fn();

      await loadDashboard();

      const summary = document.getElementById('summary');
      expect(summary?.querySelector('.skeleton-tile')).toBeNull();
      expect(summary?.dataset['skeletonActive']).toBeUndefined();
    });

    test('success path replaces the loading skeleton with KPI tiles', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        total_recommendations: 5,
        active_commitments: 3,
        committed_monthly: 500,
        current_coverage: 70,
        target_coverage: 80,
        ytd_savings: 5000,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadDashboard();

      const summary = document.getElementById('summary');
      // Real KPI tiles render — skeleton placeholders are gone.
      expect(summary?.querySelectorAll('.kpi-tile').length).toBeGreaterThan(0);
      expect(summary?.querySelector('.skeleton-tile')).toBeNull();
    });
  });
});
