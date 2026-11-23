/**
 * Dashboard module tests: savings-trend chart empty, error and stale-response tests.
 * Split from dashboard.test.ts to stay under the 500-line ceiling; the
 * module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, mockConfirmDialog, mockGroupRecsByCell, mockPageLevelRange, mockFormatSavingsRange, resetDashboardState } from './dashboard-harness';
import { loadDashboard, loadSavingsTrendChart, setupSavingsTrendHandlers } from '../dashboard';
import { Chart } from 'chart.js';
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

  describe('savings-trend chart', () => {
    beforeEach(() => {
      const canvas = document.createElement('canvas');
      canvas.id = 'savings-trend-chart';
      const empty = document.createElement('div');
      empty.id = 'savings-trend-empty';
      empty.className = 'hidden';
      const b90 = document.createElement('button');
      b90.className = 'trend-range active';
      b90.dataset['range'] = '90';
      b90.textContent = '90d';
      const b30 = document.createElement('button');
      b30.className = 'trend-range';
      b30.dataset['range'] = '30';
      b30.textContent = '30d';
      document.body.replaceChildren(canvas, empty, b90, b30);
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });
      // Reset the topbar filter mocks to the documented default (no filter)
      // so each test starts isolated. clearAllMocks() resets call history but
      // not mockReturnValue implementations, so a provider/account value set by
      // an earlier test would otherwise leak in (now that the chart reads the
      // provider chip, issue #498).
      (state.getCurrentProvider as jest.Mock).mockReturnValue('');
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
    });

    // #1686: the empty state is about the selected range, not the account's
    // whole history, and a failed fetch must not read as "no history".
    describe('empty and error states name what happened (#1686)', () => {
      const selectRange = async (range: string): Promise<void> => {
        const btn = document.createElement('button');
        btn.className = 'trend-range';
        btn.dataset['range'] = range;
        document.body.appendChild(btn);
        setupSavingsTrendHandlers();
        btn.click();
        await new Promise(r => setTimeout(r, 0));
      };
      const emptyText = (): string => document.getElementById('savings-trend-empty')?.textContent ?? '';

      test('a short range with no points names the range and points to 1y', async () => {
        await selectRange('90');
        expect(emptyText()).toContain('No savings recorded in the last 90 days.');
        expect(emptyText()).toContain('1y');
        expect(emptyText()).not.toContain('No purchase history yet');
      });

      test('the 1y range with no points names the year and offers no longer range', async () => {
        await selectRange('all');
        expect(emptyText()).toContain('No savings recorded in the last year.');
        expect(emptyText()).not.toContain('1y');
      });

      test('an active filter is named alongside the range', async () => {
        (state.getCurrentAccountIDs as jest.Mock).mockReturnValue(['uuid-acct-3']);
        await selectRange('30');
        expect(emptyText()).toContain('No savings recorded in the last 30 days for');
        expect(emptyText()).toContain('uuid-acct-3');
      });

      test('a fetch error shows an error, not an empty or "not yet" state', async () => {
        (api.getSavingsAnalytics as jest.Mock).mockRejectedValue(new Error('503'));
        await loadSavingsTrendChart();
        expect(emptyText()).toBe('Savings history could not be loaded. Reload the page to try again.');
      });

      test('shows a loading skeleton until the fetch settles', async () => {
        const loading = document.createElement('div');
        loading.id = 'savings-trend-loading';
        document.body.appendChild(loading);
        let resolve!: (v: { data_points: [] }) => void;
        (api.getSavingsAnalytics as jest.Mock).mockReturnValue(new Promise(r => { resolve = r; }));

        const done = loadSavingsTrendChart();
        expect(loading.querySelector('.skeleton')).not.toBeNull();
        // The skeleton stands in for the canvas rather than stacking above it.
        expect(document.getElementById('savings-trend-chart')?.classList.contains('hidden')).toBe(true);

        resolve({ data_points: [] });
        await done;
        expect(loading.querySelector('.skeleton')).toBeNull();
      });

      const addLoading = (): HTMLElement => {
        const loading = document.createElement('div');
        loading.id = 'savings-trend-loading';
        document.body.appendChild(loading);
        return loading;
      };
      const pending = (): ((v: unknown) => void) => {
        let resolve!: (v: unknown) => void;
        (api.getSavingsAnalytics as jest.Mock).mockReturnValueOnce(new Promise(r => { resolve = r; }));
        return (v) => resolve(v);
      };
      const points = [
        { timestamp: '2026-01-01T00:00:00Z', cumulative_savings: 100 },
        { timestamp: '2026-01-02T00:00:00Z', cumulative_savings: 150 },
      ];

      test('a reload with a chart already drawn keeps the chart and shows no skeleton', async () => {
        const loading = addLoading();
        (api.getSavingsAnalytics as jest.Mock).mockResolvedValueOnce({ data_points: points });
        await loadSavingsTrendChart();
        expect(document.getElementById('savings-trend-chart')?.classList.contains('hidden')).toBe(false);

        const resolve = pending();
        const done = loadSavingsTrendChart();
        expect(loading.querySelector('.skeleton')).toBeNull();
        expect(document.getElementById('savings-trend-chart')?.classList.contains('hidden')).toBe(false);
        resolve({ data_points: points });
        await done;
      });

      test('an older load settling after a newer one leaves the newer load\'s skeleton', async () => {
        const loading = addLoading();
        await loadSavingsTrendChart(); // empty: no chart drawn
        const resolveOld = pending();
        const older = loadSavingsTrendChart();
        const resolveNew = pending();
        const newer = loadSavingsTrendChart();

        resolveOld({ data_points: [] });
        await older;
        expect(loading.querySelector('.skeleton')).not.toBeNull();

        resolveNew({ data_points: [] });
        await newer;
        expect(loading.querySelector('.skeleton')).toBeNull();
      });

      test('the previous range\'s empty text is hidden while the next range loads', async () => {
        addLoading();
        await loadSavingsTrendChart(); // empty state shown
        expect(document.getElementById('savings-trend-empty')?.classList.contains('hidden')).toBe(false);

        const resolve = pending();
        const done = loadSavingsTrendChart();
        expect(document.getElementById('savings-trend-empty')?.classList.contains('hidden')).toBe(true);
        resolve({ data_points: [] });
        await done;
      });

      test('a failed dashboard load clears the trend skeleton and says the load failed', async () => {
        const loading = addLoading();
        await loadSavingsTrendChart(); // no chart drawn
        (api.getDashboardSummary as jest.Mock).mockRejectedValue(new Error('boom'));
        (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        const summary = document.createElement('div');
        summary.id = 'summary';
        document.body.appendChild(summary);

        await loadDashboard();

        expect(loading.querySelector('.skeleton')).toBeNull();
        expect(emptyText()).toBe('Savings history could not be loaded. Reload the page to try again.');
      });
    });

    // #747: a slower, older response must not overwrite a newer one.
    describe('stale-response guard (#747)', () => {
      type Deferred = { resolve: (v: unknown) => void; reject: (e: unknown) => void };
      const deferAnalytics = (): Deferred => {
        const d = {} as Deferred;
        (api.getSavingsAnalytics as jest.Mock).mockImplementationOnce(() => new Promise((resolve, reject) => {
          d.resolve = resolve; d.reject = reject;
        }));
        return d;
      };
      const plotted = (): number[] => {
        const calls = (Chart as unknown as jest.Mock).mock.calls;
        const cfg = calls[calls.length - 1]?.[1] as { data: { datasets: { data: { y: number }[] }[] } } | undefined;
        return cfg?.data.datasets[0]?.data.map((p) => p.y) ?? [];
      };
      // Two points: one point is the sparse-data state (#188), which draws no chart.
      const point = (y: number) => ({ data_points: [
        { timestamp: '2025-12-31T00:00:00Z', cumulative_savings: 0 },
        { timestamp: '2026-01-01T00:00:00Z', cumulative_savings: y },
      ] });

      test('an older response landing last is ignored', async () => {
        const older = deferAnalytics();
        const newer = deferAnalytics();
        const first = loadSavingsTrendChart();
        const second = loadSavingsTrendChart();

        newer.resolve(point(200));
        await second;
        older.resolve(point(100));
        await first;

        expect(Chart).toHaveBeenCalledTimes(1);
        expect(plotted()).toEqual([0, 200]);
      });

      test('an older request failing last does not replace the newer chart with an error', async () => {
        const older = deferAnalytics();
        const newer = deferAnalytics();
        const first = loadSavingsTrendChart();
        const second = loadSavingsTrendChart();

        newer.resolve(point(200));
        await second;
        older.reject(new Error('503'));
        await first;

        expect(document.getElementById('savings-trend-chart')?.classList.contains('hidden')).toBe(false);
        expect(document.getElementById('savings-trend-empty')?.classList.contains('hidden')).toBe(true);
      });
    });

    // #747: a range change updates the existing chart instead of rebuilding it.
    test('a second load updates the trend chart in place without animation (#747)', async () => {
      const update = jest.fn();
      const destroy = jest.fn();
      (Chart as unknown as jest.Mock).mockImplementationOnce((canvas: HTMLCanvasElement, config: { type: string }) => ({ canvas, config, update, destroy }));
      (api.getSavingsAnalytics as jest.Mock)
        .mockResolvedValueOnce({ data_points: [
          { timestamp: '2025-12-31T00:00:00Z', cumulative_savings: 0 },
          { timestamp: '2026-01-01T00:00:00Z', cumulative_savings: 100 },
        ] })
        .mockResolvedValueOnce({ data_points: [
          { timestamp: '2025-12-31T00:00:00Z', cumulative_savings: 0 },
          { timestamp: '2026-01-01T00:00:00Z', cumulative_savings: 300 },
        ] });

      await loadSavingsTrendChart();
      await loadSavingsTrendChart();

      expect(Chart).toHaveBeenCalledTimes(1);
      expect(destroy).not.toHaveBeenCalled();
      expect(update).toHaveBeenCalledWith('none');
      // The trend chart is module state; drop it so later tests start fresh.
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValueOnce({ data_points: [] });
      await loadSavingsTrendChart();
    });

    // #744: summary label + table for the trend chart.
    test('describes the plotted series and lists every point as a table (#744)', async () => {
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [
        { timestamp: '2026-01-01T00:00:00Z', cumulative_savings: 100 },
        { timestamp: '2026-01-02T00:00:00Z', cumulative_savings: 250 },
      ] });

      await loadSavingsTrendChart();

      const label = document.getElementById('savings-trend-chart')?.getAttribute('aria-label') ?? '';
      expect(label).toContain('cumulative savings over the last');
      expect(label).toContain('$250');
      expect(label).toContain('2 data points');
      const rows = document.querySelectorAll('#savings-trend-table tbody tr');
      expect(rows).toHaveLength(2);
      expect(rows[1]?.querySelector('td')?.textContent).toBe('$250');
    });

    // Two points: one point is the sparse-data state (#1334), which draws
    // no chart and so has no table.
    const twoTrendPoints = [
      { timestamp: '2026-01-01T00:00:00Z', cumulative_savings: 100 },
      { timestamp: '2026-01-02T00:00:00Z', cumulative_savings: 150 },
    ];

    test('removes the trend table when there is no data to plot (#744)', async () => {
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValueOnce({ data_points: twoTrendPoints });
      await loadSavingsTrendChart();
      expect(document.getElementById('savings-trend-table')).not.toBeNull();

      (api.getSavingsAnalytics as jest.Mock).mockResolvedValueOnce({ data_points: [] });
      await loadSavingsTrendChart();
      expect(document.getElementById('savings-trend-table')).toBeNull();
    });

    test('removes the trend table in the sparse-data state (#744, #1334)', async () => {
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValueOnce({ data_points: twoTrendPoints });
      await loadSavingsTrendChart();
      expect(document.getElementById('savings-trend-table')).not.toBeNull();

      (api.getSavingsAnalytics as jest.Mock).mockResolvedValueOnce({ data_points: [twoTrendPoints[0]] });
      await loadSavingsTrendChart();
      expect(document.getElementById('savings-trend-table')).toBeNull();
    });
  });
});
