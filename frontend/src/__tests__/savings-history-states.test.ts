/**
 * Savings History module tests: empty-state copy, edge cases, filter
 * descriptions and the one-series chart. Split from savings-history.test.ts to
 * stay under the 500-line ceiling; the module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockChartInstance } from './savings-history-harness';
import { loadSavingsHistory } from '../modules/savings-history';
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

  // Issue #701: when the chart returns no data because a filter is active, the
  // empty-state message must distinguish "nothing in this scope" from "no data
  // at all". Without a filter the original message applies; with a filter the
  // message should name the active filter so the user understands why the chart
  // is blank.
  describe('empty-state copy with active filter (issue #701)', () => {
    test('shows filter name in empty-state heading when provider chip is set', async () => {
      (state.getCurrentProvider as jest.Mock).mockReturnValue('aws');
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      await loadSavingsHistory();

      const emptyEl = document.getElementById('savings-history-empty');
      const heading = emptyEl?.querySelector('p:first-child');
      expect(heading?.textContent).toContain('AWS');
      expect(heading?.textContent).not.toContain('available yet');
    });

    test('shows help text asking to broaden filter when a filter is active', async () => {
      (state.getCurrentProvider as jest.Mock).mockReturnValue('gcp');
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      await loadSavingsHistory();

      const emptyEl = document.getElementById('savings-history-empty');
      const help = emptyEl?.querySelector('p.help-text');
      expect(help?.textContent).toMatch(/broaden/i);
    });

    test('shows original message when no filter is active', async () => {
      (state.getCurrentProvider as jest.Mock).mockReturnValue('');
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      await loadSavingsHistory();

      const emptyEl = document.getElementById('savings-history-empty');
      const heading = emptyEl?.querySelector('p:first-child');
      expect(heading?.textContent).toContain('available yet');
    });

    test('shows filter name in empty-state heading when account chip is set', async () => {
      const testUUID = 'aabbccdd-1234-5678-abcd-aabbccddee00';
      (state.getCurrentProvider as jest.Mock).mockReturnValue('');
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([testUUID]);
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      await loadSavingsHistory();

      const emptyEl = document.getElementById('savings-history-empty');
      const heading = emptyEl?.querySelector('p:first-child');
      expect(heading?.textContent).toContain(testUUID);
    });

    test('shows provider and account in heading when both chips are set', async () => {
      const testUUID = 'aabbccdd-1234-5678-abcd-aabbccddee00';
      (state.getCurrentProvider as jest.Mock).mockReturnValue('azure');
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([testUUID]);
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      await loadSavingsHistory();

      const emptyEl = document.getElementById('savings-history-empty');
      const heading = emptyEl?.querySelector('p:first-child');
      expect(heading?.textContent).toContain('AZURE');
      expect(heading?.textContent).toContain(testUUID);
    });

    test('shows distinct error copy on API failure (not the filter-aware empty state)', async () => {
      // CR feedback on PR #741: a fetch failure used to fall through to the
      // "no data for the selected filter" empty state, which masked real
      // outages (network/5xx/auth). The error path must now render its own
      // copy so the user can tell "filter matched nothing" from "the fetch
      // never completed".
      (state.getCurrentProvider as jest.Mock).mockReturnValue('aws');
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
      (getSavingsAnalytics as jest.Mock).mockRejectedValue(new Error('network failure'));
      console.error = jest.fn();

      await loadSavingsHistory();

      const emptyEl = document.getElementById('savings-history-empty');
      const heading = emptyEl?.querySelector('p:first-child');
      const help = emptyEl?.querySelector('p.help-text');
      expect(heading?.textContent).toBe('Failed to load savings history.');
      expect(help?.textContent).toContain('network failure');
      // Error copy is NOT the empty-state copy — it does not name the filter.
      expect(heading?.textContent).not.toContain('AWS');
      expect(heading?.textContent).not.toContain('available yet');
    });

    test('error state hides chart container and stats', async () => {
      (getSavingsAnalytics as jest.Mock).mockRejectedValue(new Error('boom'));
      console.error = jest.fn();

      await loadSavingsHistory();

      const chartContainer = document.getElementById('savings-history-chart')?.parentElement;
      const statsEl = document.getElementById('savings-stats');
      expect(chartContainer?.classList.contains('hidden')).toBe(true);
      expect(statsEl?.classList.contains('hidden')).toBe(true);
    });

    test('error state handles non-Error rejection with "Unknown error"', async () => {
      // Generic reject value (e.g. a thrown string from a non-Error catch path)
      // must still surface a helpful message rather than `[object Object]`.
      (getSavingsAnalytics as jest.Mock).mockRejectedValue('something broke');
      console.error = jest.fn();

      await loadSavingsHistory();

      const help = document.getElementById('savings-history-empty')?.querySelector('p.help-text');
      expect(help?.textContent).toContain('Unknown error');
    });
  });

  describe('edge cases', () => {
    test('handles empty summary object', async () => {
      const mockData = {
        summary: {},
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 100, cumulative_savings: 100, total_upfront: 500, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      // Should calculate from data points
      const periodSavingsEl = document.getElementById('period-savings');
      expect(periodSavingsEl?.textContent).toBe('$100.00/mo');
    });

    test('handles missing stats elements', async () => {
      document.getElementById('period-savings')?.remove();
      document.getElementById('avg-hourly-savings')?.remove();
      document.getElementById('peak-savings')?.remove();

      const mockData = {
        summary: { total_monthly_savings: 1500, total_annual_savings: 18000 },
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 100, cumulative_savings: 100, total_upfront: 500, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      // Should not throw
      await expect(loadSavingsHistory()).resolves.not.toThrow();
    });

    test('handles missing chart container parent', async () => {
      const canvas = document.getElementById('savings-history-chart');
      if (canvas && canvas.parentElement) {
        canvas.parentElement.removeChild(canvas);
        document.body.appendChild(canvas);
      }

      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      // Should not throw
      await expect(loadSavingsHistory()).resolves.not.toThrow();
    });

    test('handles zero values in data points', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 0, cumulative_savings: 0, total_upfront: 0, purchase_count: 0 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      expect(Chart).toHaveBeenCalled();
    });

    test('calculates peak savings from data points', async () => {
      const mockData = {
        summary: null,
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 50, cumulative_savings: 50, total_upfront: 500, purchase_count: 1 },
          { timestamp: '2024-01-02T00:00:00Z', total_savings: 150, cumulative_savings: 200, total_upfront: 1000, purchase_count: 2 },
          { timestamp: '2024-01-03T00:00:00Z', total_savings: 100, cumulative_savings: 300, total_upfront: 1500, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const peakSavingsEl = document.getElementById('peak-savings');
      // Peak should be 150
      expect(peakSavingsEl?.textContent).toBe('$150.00/mo');
    });

    test('destroys chart when showing empty state', async () => {
      // First load with data to create chart
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      });
      await loadSavingsHistory();

      // Then load with no data
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: []
      });
      await loadSavingsHistory();

      expect(mockChartInstance.destroy).toHaveBeenCalled();
    });
  });
});
