/**
 * Savings History module tests
 */

// The harness registers the module mocks, so it must be imported first.
import { mockChartInstance } from './savings-history-harness';
// Now import after mocking
import { loadSavingsHistory, initSavingsHistory } from '../modules/savings-history';
import { getSavingsAnalytics } from '../api';
import * as state from '../state';
import { Chart } from 'chart.js';

describe('Savings History Module', () => {
  beforeEach(() => {
    // Reset DOM
    document.body.innerHTML = `
      <select id="savings-period">
        <option value="24h">Last 24 Hours</option>
        <option value="7d">Last 7 Days</option>
        <option value="30d">Last 30 Days</option>
        <option value="90d" selected>Last 90 Days</option>
      </select>
      <select id="savings-unit">
        <option value="hourly">Hourly</option>
        <option value="monthly" selected>Monthly</option>
        <option value="yearly">Yearly</option>
      </select>
      <select id="savings-series">
        <option value="period" selected>Savings per period</option>
        <option value="cumulative">Cumulative savings</option>
      </select>
      <button id="refresh-savings-btn">Refresh</button>
      <div id="savings-history-container">
        <canvas id="savings-history-chart"></canvas>
      </div>
      <div id="savings-history-empty" class="hidden">
        <p>No savings history data available yet.</p>
        <p class="help-text">Data will be collected hourly once you have active purchases.</p>
      </div>
      <div id="savings-stats">
        <h4 id="period-savings-label">Period savings</h4>
        <span id="period-savings">$0</span>
        <p id="period-savings-unit">shown in monthly equivalents</p>
        <h4 id="avg-savings-label">Avg monthly savings</h4>
        <span id="avg-hourly-savings">$0/mo</span>
        <span id="peak-savings">$0/mo</span>
      </div>
    `;

    jest.clearAllMocks();
    (Chart as unknown as jest.Mock).mockClear();
  });

  afterEach(() => { jest.useRealTimers(); });

  describe('requested-window average', () => {
    const now = new Date('2026-10-01T12:17:15.123Z');

    beforeEach(() => { jest.useFakeTimers().setSystemTime(now); });
    afterEach(() => { jest.useRealTimers(); });

    test.each([
      ['24h', 'hourly', 24], ['7d', 'hourly', 168],
      ['30d', 'daily', 30], ['90d', 'daily', 90],
    ] as const)('%s includes empty intervals in the average', async (period, interval, count) => {
      (document.getElementById('savings-period') as HTMLSelectElement).value = period;
      for (const mode of ['sparse', 'zero-filled', 'full']) {
        (getSavingsAnalytics as jest.Mock).mockImplementation(({ start, end }) => ({
          start, end, interval,
          summary: { total_monthly_savings: mode === 'full' ? count * 10 : 1680 },
          data_points: Array.from({ length: mode === 'sparse' ? 3 : count }, (_, i) => ({
            timestamp: new Date(Date.parse(start) + i * (interval === 'hourly' ? 3600000 : 86400000)).toISOString(),
            total_savings: mode === 'full' ? 10 : i < 3 ? 560 : 0,
          })),
        }));
        await loadSavingsHistory();
        expect(document.getElementById('avg-hourly-savings')?.textContent)
          .toBe('$' + (mode === 'full' ? 10 : 1680 / count).toFixed(2) + '/mo');
        const calls = (getSavingsAnalytics as jest.Mock).mock.calls;
        const request = calls[calls.length - 1]?.[0];
        expect(request.end).toBe(now.toISOString());
        expect(Date.parse(request.end) - Date.parse(request.start))
          .toBe(count * (interval === 'hourly' ? 3600000 : 86400000));
        expect(request.interval).toBe(interval);
      }
    });

    test.each([
      ['hourly', '$0.01/hr'], ['monthly', '$10.00/mo'], ['yearly', '$120.00/yr'],
    ])('converts the window average to %s', async (unit, expected) => {
      (document.getElementById('savings-period') as HTMLSelectElement).value = '7d';
      (document.getElementById('savings-unit') as HTMLSelectElement).value = unit;
      (getSavingsAnalytics as jest.Mock).mockImplementation(({ start, end, interval }) => ({
        start, end, interval,
        summary: { total_monthly_savings: 1680, total_annual_savings: 20160 },
        data_points: [start, new Date(Date.parse(start) + 72 * 3600000).toISOString(), end].map(timestamp => ({
          timestamp: new Date(Math.floor(Date.parse(timestamp) / 3600000) * 3600000).toISOString(),
          total_savings: 560,
        })),
      }));
      await loadSavingsHistory();
      expect(document.getElementById('avg-hourly-savings')?.textContent).toBe(expected);
    });

    test('keeps a zero-valued populated window visible', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({
        summary: { total_monthly_savings: 0 },
        data_points: [{ timestamp: now.toISOString(), total_savings: 0 }],
      });
      await loadSavingsHistory();
      expect(document.getElementById('avg-hourly-savings')?.textContent).toBe('$0.00/mo');
      expect(document.getElementById('savings-stats')?.classList.contains('hidden')).toBe(false);
    });
  });

  describe('loadSavingsHistory', () => {
    test('loads and renders savings data for default 90d period', async () => {
      const mockData = {
        start: '2024-01-01T00:00:00Z',
        end: '2024-04-01T00:00:00Z',
        interval: 'daily',
        summary: {
          total_purchases: 2,
          total_upfront: 250,
          total_monthly_savings: 25,
          total_annual_savings: 300
        },
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 },
          { timestamp: '2024-01-02T00:00:00Z', total_savings: 15, cumulative_savings: 25, total_upfront: 150, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      // 90d is the new default (multi-month trend signal); switch case in
      // savings-history.ts maps it to the 'daily' interval.
      expect(getSavingsAnalytics).toHaveBeenCalledWith(expect.objectContaining({
        interval: 'daily'
      }));
      expect(Chart).toHaveBeenCalled();
    });

    // #744: the canvas gets a value summary and a "View as table" disclosure
    // placed after (not inside) the chart's sizing container.
    test('adds a summary label and a data table for the plotted points (#744)', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({
        summary: { total_period_savings: 25, total_upfront_spent: 0, purchase_count: 2, average_savings_per_period: 12.5, peak_savings: 15 },
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 0, purchase_count: 1 },
          { timestamp: '2024-01-02T00:00:00Z', total_savings: 15, cumulative_savings: 25, total_upfront: 0, purchase_count: 1 },
        ],
      });

      await loadSavingsHistory();

      const canvas = document.getElementById('savings-history-chart');
      expect(canvas?.getAttribute('aria-label')).toContain('2 data points');
      expect(canvas?.getAttribute('aria-label')).toContain('$25.00');
      const table = document.getElementById('savings-history-table');
      expect(document.getElementById('savings-history-container')?.nextElementSibling).toBe(table);
      const headers = Array.from(table?.querySelectorAll('thead th') ?? []).map((th) => th.textContent);
      expect(headers).toEqual(['Date', 'Period savings (/mo)', 'Cumulative savings']);
      const last = Array.from(table?.querySelectorAll('tbody tr') ?? []).pop();
      expect(Array.from(last?.querySelectorAll('td') ?? []).map((td) => td.textContent)).toEqual(['$15.00', '$25.00']);
    });

    test('removes the data table when the chart is replaced by the empty state (#744)', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValueOnce({
        data_points: [{ timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 0, purchase_count: 1 }],
      });
      await loadSavingsHistory();
      expect(document.getElementById('savings-history-table')).not.toBeNull();

      (getSavingsAnalytics as jest.Mock).mockResolvedValueOnce({ data_points: [] });
      await loadSavingsHistory();
      expect(document.getElementById('savings-history-table')).toBeNull();
    });

    test('shows empty state when no data points', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: []
      });

      await loadSavingsHistory();

      const emptyEl = document.getElementById('savings-history-empty');
      expect(emptyEl?.classList.contains('hidden')).toBe(false);
    });

    test('shows empty state when data_points is null', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: null
      });

      await loadSavingsHistory();

      const emptyEl = document.getElementById('savings-history-empty');
      expect(emptyEl?.classList.contains('hidden')).toBe(false);
    });

    test('shows empty state on API error', async () => {
      (getSavingsAnalytics as jest.Mock).mockRejectedValue(new Error('API Error'));
      console.error = jest.fn();

      await loadSavingsHistory();

      const emptyEl = document.getElementById('savings-history-empty');
      expect(emptyEl?.classList.contains('hidden')).toBe(false);
    });

    test('hides chart container when showing empty state', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: []
      });

      await loadSavingsHistory();

      const chartContainer = document.getElementById('savings-history-chart')?.parentElement;
      expect(chartContainer?.classList.contains('hidden')).toBe(true);
    });

    test('hides stats when showing empty state', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: []
      });

      await loadSavingsHistory();

      const statsEl = document.getElementById('savings-stats');
      expect(statsEl?.classList.contains('hidden')).toBe(true);
    });

    test('uses 24h period with hourly interval', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      const periodSelect = document.getElementById('savings-period') as HTMLSelectElement;
      periodSelect.value = '24h';

      await loadSavingsHistory();

      expect(getSavingsAnalytics).toHaveBeenCalledWith(expect.objectContaining({
        interval: 'hourly'
      }));
    });

    test('uses 30d period with daily interval', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      const periodSelect = document.getElementById('savings-period') as HTMLSelectElement;
      periodSelect.value = '30d';

      await loadSavingsHistory();

      expect(getSavingsAnalytics).toHaveBeenCalledWith(expect.objectContaining({
        interval: 'daily'
      }));
    });

    test('uses 90d period with daily interval', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      const periodSelect = document.getElementById('savings-period') as HTMLSelectElement;
      periodSelect.value = '90d';

      await loadSavingsHistory();

      expect(getSavingsAnalytics).toHaveBeenCalledWith(expect.objectContaining({
        interval: 'daily'
      }));
    });

    test('defaults to 7d period with hourly interval for unknown value', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      const periodSelect = document.getElementById('savings-period') as HTMLSelectElement;
      periodSelect.value = 'unknown';

      await loadSavingsHistory();

      expect(getSavingsAnalytics).toHaveBeenCalledWith(expect.objectContaining({
        interval: 'hourly'
      }));
    });

    test('renders the period total from the wire summary key (issue #276)', async () => {
      // The server total diverges from the bucket sum (e.g. it excludes
      // revoked rows); the wire value must win over the client-side sum.
      const mockData = {
        summary: {
          total_purchases: 1,
          total_upfront: 500,
          total_monthly_savings: 1800,
          total_annual_savings: 21600
        },
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 2000, cumulative_savings: 2000, total_upfront: 500, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);
      // Pin "now" away from DST changes so the 90d window is exactly 90 days.
      jest.useFakeTimers().setSystemTime(new Date('2026-10-01T12:00:00Z'));
      try {
        await loadSavingsHistory();
      } finally {
        jest.useRealTimers();
      }

      expect(document.getElementById('period-savings')?.textContent).toBe('$1,800.00/mo');
      // 90d window: the average spreads the authoritative total over 90 days.
      expect(document.getElementById('avg-hourly-savings')?.textContent).toBe('$20.00/mo');
      // Peak has no wire counterpart, so it comes from the buckets.
      expect(document.getElementById('peak-savings')?.textContent).toBe('$2,000.00/mo');
    });

    test('ignores the never-sent legacy summary keys (issue #276)', async () => {
      const mockData = {
        summary: { total_period_savings: 9999, average_savings_per_period: 9999, peak_savings: 9999 },
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 100, cumulative_savings: 100, total_upfront: 500, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      expect(document.getElementById('period-savings')?.textContent).toBe('$100.00/mo');
      expect(document.getElementById('peak-savings')?.textContent).toBe('$100.00/mo');
    });

    test('calculates stats from data points when summary missing', async () => {
      const mockData = {
        summary: null,
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 100, cumulative_savings: 100, total_upfront: 500, purchase_count: 1 },
          { timestamp: '2024-01-02T00:00:00Z', total_savings: 200, cumulative_savings: 300, total_upfront: 1000, purchase_count: 2 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const periodSavingsEl = document.getElementById('period-savings');
      expect(periodSavingsEl?.textContent).toContain('$300.00');
    });

    test('handles missing period select gracefully', async () => {
      document.getElementById('savings-period')?.remove();
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      // Should not throw
      await expect(loadSavingsHistory()).resolves.not.toThrow();
    });

    test('destroys existing chart before creating new one', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      // Load twice to trigger chart recreation
      await loadSavingsHistory();
      await loadSavingsHistory();

      expect(mockChartInstance.destroy).toHaveBeenCalled();
    });

    test('formats currency values over 1000 with the shared formatter (#749)', async () => {
      const mockData = {
        summary: { total_monthly_savings: 5000, total_annual_savings: 60000 },
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 100, cumulative_savings: 100, total_upfront: 500, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const periodSavingsEl = document.getElementById('period-savings');
      expect(periodSavingsEl?.textContent).toBe('$5,000.00/mo');
    });

    test('formats currency values under 1000 without suffix', async () => {
      const mockData = {
        summary: { total_monthly_savings: 500, total_annual_savings: 6000 },
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 100, cumulative_savings: 100, total_upfront: 500, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const periodSavingsEl = document.getElementById('period-savings');
      expect(periodSavingsEl?.textContent).toBe('$500.00/mo');
    });

    test('shows chart container when data is available', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const chartContainer = document.getElementById('savings-history-chart')?.parentElement;
      expect(chartContainer?.classList.contains('hidden')).toBe(false);
    });

    test('hides empty state when data is available', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const emptyEl = document.getElementById('savings-history-empty');
      expect(emptyEl?.classList.contains('hidden')).toBe(true);
    });

    test('shows stats when data is available', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const statsEl = document.getElementById('savings-stats');
      expect(statsEl?.classList.contains('hidden')).toBe(false);
    });

    test('rebuilds the chart with the new token colour on a color-scheme change (#699)', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: [
          { timestamp: '2024-01-01T12:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 },
          { timestamp: '2024-01-02T12:00:00Z', total_savings: 20, cumulative_savings: 30, total_upfront: 200, purchase_count: 1 }
        ]
      });
      await loadSavingsHistory();
      const calls = (Chart as unknown as jest.Mock).mock.calls;
      const callsBefore = calls.length;
      document.documentElement.style.setProperty('--cudly-chart-2', '#81c995');
      document.documentElement.setAttribute('data-theme', 'dark');
      await new Promise((r) => setTimeout(r, 0));
      document.documentElement.removeAttribute('data-theme');
      document.documentElement.removeAttribute('style');
      expect(calls.length).toBeGreaterThan(callsBefore);
      expect(JSON.stringify(calls[calls.length - 1][1].data.datasets)).toContain('#81c995');
    });

    test('creates chart with correct configuration', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T12:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 },
          { timestamp: '2024-01-02T12:00:00Z', total_savings: 20, cumulative_savings: 30, total_upfront: 200, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      // Issue #749: one series (period savings as bars) on one y-axis.
      expect(Chart).toHaveBeenCalledWith(
        expect.any(HTMLCanvasElement),
        expect.objectContaining({
          type: 'bar',
          data: expect.objectContaining({
            datasets: [
              expect.objectContaining({
                type: 'bar',
                label: 'Period Savings'
              })
            ]
          }),
          options: expect.objectContaining({
            responsive: true,
            maintainAspectRatio: false
          })
        })
      );
    });

    test('handles data points with null total_savings', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: null, cumulative_savings: 0, total_upfront: 0, purchase_count: 0 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      // Should still create chart without throwing
      expect(Chart).toHaveBeenCalled();
    });

    test('handles data points with undefined cumulative_savings', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: undefined, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      // Should still create chart without throwing
      expect(Chart).toHaveBeenCalled();
    });

    test('logs error on API failure', async () => {
      console.error = jest.fn();
      (getSavingsAnalytics as jest.Mock).mockRejectedValue(new Error('Network error'));

      await loadSavingsHistory();

      expect(console.error).toHaveBeenCalledWith(
        'Failed to load savings history:',
        'Network error'
      );
    });

    test('handles missing canvas element gracefully', async () => {
      document.getElementById('savings-history-chart')?.remove();
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);
      console.error = jest.fn();

      await loadSavingsHistory();

      expect(console.error).toHaveBeenCalledWith('Canvas element not found: savings-history-chart');
    });
  });

  // Issue #503: Purchases tab subscriber wiring. Chip changes must re-query
  // the Savings History chart with the new filter; inactive-tab guard and
  // microtask coalescing mirror PR #488's recommendations.ts pattern.
  describe('subscriber wiring (issue #503)', () => {
    function addPurchasesTab(active: boolean): void {
      const tab = document.createElement('div');
      tab.id = 'purchases-tab';
      if (active) tab.classList.add('active');
      document.body.appendChild(tab);
    }

    beforeEach(() => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });
      // Reset filter getters to their defaults; individual tests override.
      (state.getCurrentProvider as jest.Mock).mockReturnValue('');
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
    });

    test('registers callbacks with state.subscribeProvider and state.subscribeAccount', () => {
      initSavingsHistory();

      expect(state.subscribeProvider).toHaveBeenCalledTimes(1);
      expect(state.subscribeAccount).toHaveBeenCalledTimes(1);
      expect(typeof (state.subscribeProvider as jest.Mock).mock.calls[0]?.[0]).toBe('function');
      expect(typeof (state.subscribeAccount as jest.Mock).mock.calls[0]?.[0]).toBe('function');
    });

    test('account chip change re-queries the chart when purchases-tab is active', async () => {
      addPurchasesTab(true);
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue(['acct-A']);

      initSavingsHistory();
      const accountCb = (state.subscribeAccount as jest.Mock).mock.calls[0]?.[0] as () => void;
      expect(typeof accountCb).toBe('function');

      (getSavingsAnalytics as jest.Mock).mockClear();
      accountCb();
      // queueMicrotask + the awaited fetch settle within a macrotask flush.
      await new Promise((r) => setTimeout(r, 0));

      expect(getSavingsAnalytics).toHaveBeenCalledTimes(1);
      expect(getSavingsAnalytics).toHaveBeenCalledWith(
        expect.objectContaining({ account_ids: ['acct-A'] })
      );
    });

    test('forwards the full account selection when two or more are selected', async () => {
      addPurchasesTab(true);
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue(['acct-A', 'acct-B']);

      initSavingsHistory();
      const accountCb = (state.subscribeAccount as jest.Mock).mock.calls[0]?.[0] as () => void;
      (getSavingsAnalytics as jest.Mock).mockClear();
      accountCb();
      await new Promise((r) => setTimeout(r, 0));

      expect(getSavingsAnalytics).toHaveBeenCalledWith(
        expect.objectContaining({ account_ids: ['acct-A', 'acct-B'] })
      );
    });

    test('provider chip change re-queries the chart when purchases-tab is active', async () => {
      addPurchasesTab(true);
      (state.getCurrentProvider as jest.Mock).mockReturnValue('aws');

      initSavingsHistory();
      const providerCb = (state.subscribeProvider as jest.Mock).mock.calls[0]?.[0] as () => void;
      expect(typeof providerCb).toBe('function');

      (getSavingsAnalytics as jest.Mock).mockClear();
      providerCb();
      await new Promise((r) => setTimeout(r, 0));

      expect(getSavingsAnalytics).toHaveBeenCalledTimes(1);
      expect(getSavingsAnalytics).toHaveBeenCalledWith(
        expect.objectContaining({ provider: 'aws' })
      );
    });

    test('does NOT re-query when purchases-tab is inactive (active-tab guard)', async () => {
      // #purchases-tab present but no .active class: user is on another tab.
      addPurchasesTab(false);

      initSavingsHistory();
      const providerCb = (state.subscribeProvider as jest.Mock).mock.calls[0]?.[0] as () => void;
      const accountCb = (state.subscribeAccount as jest.Mock).mock.calls[0]?.[0] as () => void;

      (getSavingsAnalytics as jest.Mock).mockClear();
      providerCb();
      accountCb();
      await new Promise((r) => setTimeout(r, 0));

      expect(getSavingsAnalytics).not.toHaveBeenCalled();
    });

    test('coalesces back-to-back provider+account fires into one reload', async () => {
      addPurchasesTab(true);

      initSavingsHistory();
      const providerCb = (state.subscribeProvider as jest.Mock).mock.calls[0]?.[0] as () => void;
      const accountCb = (state.subscribeAccount as jest.Mock).mock.calls[0]?.[0] as () => void;

      (getSavingsAnalytics as jest.Mock).mockClear();
      // Simulate the topbar provider-change handler firing both subscribers
      // synchronously (clear accounts, then set provider, per #185).
      accountCb();
      providerCb();
      await new Promise((r) => setTimeout(r, 0));

      expect(getSavingsAnalytics).toHaveBeenCalledTimes(1);
    });

    test('two consecutive account changes produce two fetches with distinct account_ids', async () => {
      addPurchasesTab(true);

      initSavingsHistory();
      const accountCb = (state.subscribeAccount as jest.Mock).mock.calls[0]?.[0] as () => void;

      (getSavingsAnalytics as jest.Mock).mockClear();

      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue(['acct-A']);
      accountCb();
      await new Promise((r) => setTimeout(r, 0));

      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue(['acct-B']);
      accountCb();
      await new Promise((r) => setTimeout(r, 0));

      expect(getSavingsAnalytics).toHaveBeenCalledTimes(2);
      const firstCall = (getSavingsAnalytics as jest.Mock).mock.calls[0][0];
      const secondCall = (getSavingsAnalytics as jest.Mock).mock.calls[1][0];
      expect(firstCall.account_ids).toEqual(['acct-A']);
      expect(secondCall.account_ids).toEqual(['acct-B']);
    });
  });
});
