/**
 * Savings History module tests: initSavingsHistory, subscriber wiring and the
 * second half of loadSavingsHistory. Split from savings-history.test.ts to stay
 * under the 500-line ceiling; the module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import './savings-history-harness';
import { loadSavingsHistory, initSavingsHistory } from '../modules/savings-history';
import { getSavingsAnalytics } from '../api';
import * as state from '../state';
import { Chart } from 'chart.js';
import { flushPromises } from './flush-promises';

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

  describe('initSavingsHistory', () => {
    test('adds change listener to period select', () => {
      const periodSelect = document.getElementById('savings-period') as HTMLSelectElement;
      const addEventListenerSpy = jest.spyOn(periodSelect, 'addEventListener');

      initSavingsHistory();

      expect(addEventListenerSpy).toHaveBeenCalledWith('change', expect.any(Function));
    });

    test('adds click listener to refresh button', () => {
      const refreshBtn = document.getElementById('refresh-savings-btn') as HTMLButtonElement;
      const addEventListenerSpy = jest.spyOn(refreshBtn, 'addEventListener');

      initSavingsHistory();

      expect(addEventListenerSpy).toHaveBeenCalledWith('click', expect.any(Function));
    });

    test('handles missing period select gracefully', () => {
      document.getElementById('savings-period')?.remove();

      expect(() => initSavingsHistory()).not.toThrow();
    });

    test('handles missing refresh button gracefully', () => {
      document.getElementById('refresh-savings-btn')?.remove();

      expect(() => initSavingsHistory()).not.toThrow();
    });

    test('period change triggers loadSavingsHistory', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      initSavingsHistory();

      const periodSelect = document.getElementById('savings-period') as HTMLSelectElement;
      periodSelect.value = '30d';
      periodSelect.dispatchEvent(new Event('change'));

      await flushPromises();

      expect(getSavingsAnalytics).toHaveBeenCalled();
    });

    test('refresh button click triggers loadSavingsHistory', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      initSavingsHistory();

      const refreshBtn = document.getElementById('refresh-savings-btn') as HTMLButtonElement;
      refreshBtn.click();

      await flushPromises();

      expect(getSavingsAnalytics).toHaveBeenCalled();
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

  describe('loadSavingsHistory', () => {
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
});
