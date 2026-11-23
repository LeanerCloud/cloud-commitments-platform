/**
 * Savings History module tests: chart formatting and chart callback functions.
 * Split from savings-history.test.ts to stay under the 500-line ceiling; the
 * module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import './savings-history-harness';
import { loadSavingsHistory } from '../modules/savings-history';
import { getSavingsAnalytics } from '../api';
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

  // Freeze only Date so promise/timer-based helpers (flushPromises) keep working.
  const pinDateOnly = (iso: string): void => {
    jest.useFakeTimers({
      now: new Date(iso),
      doNotFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'setImmediate', 'clearImmediate', 'nextTick', 'queueMicrotask', 'requestAnimationFrame', 'cancelAnimationFrame', 'performance'],
    });
  };

  describe('chart callback functions', () => {
    // Regression tests for QA 2.2 / 2.3 / 2.4 (do not remove)

    test('QA 2.3: Period Savings tooltip has exactly 2 decimal places matching Cumulative', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const chartCall = (Chart as unknown as jest.Mock).mock.calls[0];
      const tooltipCb = chartCall[1].options.plugins.tooltip.callbacks.label;

      // Period Savings: 2 decimal places, not 4
      const periodCtx = { raw: 12.3456, datasetIndex: 0, dataset: { label: 'Period Savings' } };
      const periodLabel = tooltipCb(periodCtx) as string;
      // Must render with exactly 2 decimal places, matching Cumulative and the KPI (QA 2.3)
      expect(periodLabel).toMatch(/\$\d+\.\d{2}(?!\d)/);
      expect(periodLabel).not.toMatch(/\$\d+\.\d{3}/);

      // Cumulative: also 2 decimal places
      const cumulCtx = { raw: 99.9876, datasetIndex: 1, dataset: { label: 'Cumulative Savings' } };
      const cumulLabel = tooltipCb(cumulCtx) as string;
      expect(cumulLabel).toMatch(/\$\d+\.\d{2}(?!\d)/);
      expect(cumulLabel).not.toMatch(/\$\d+\.\d{3}/);
    });

    test('QA 2.2: x-axis is type:linear with numeric min equal to period start', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-03-15T10:00:00Z', total_savings: 50, cumulative_savings: 50, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      const periodSelect = document.getElementById('savings-period') as HTMLSelectElement;
      periodSelect.value = '30d';

      // The 30d window is 30 local calendar days; pin to a date with no DST
      // change in range (jest pins TZ=America/New_York) so it is exactly 30 * 24h.
      pinDateOnly('2026-10-01T12:17:15.123Z');
      const before = Date.now();
      await loadSavingsHistory();
      const after = Date.now();

      const chartCall = (Chart as unknown as jest.Mock).mock.calls[0];
      const xAxis = chartCall[1].options.scales.x;

      // Must be linear, not category
      expect(xAxis.type).toBe('linear');

      // min must be a number (the period start timestamp)
      expect(typeof xAxis.min).toBe('number');
      // 30d ago: the min should be roughly 30 days before 'after'
      const thirtyDaysMs = 30 * 24 * 60 * 60 * 1000;
      expect(xAxis.min).toBeGreaterThanOrEqual(before - thirtyDaysMs - 1000);
      expect(xAxis.min).toBeLessThanOrEqual(after - thirtyDaysMs + 1000);

      // max should be close to now
      expect(xAxis.max).toBeGreaterThanOrEqual(before);
      expect(xAxis.max).toBeLessThanOrEqual(after + 1000);

      // No top-level labels array (data is in {x,y} format)
      expect(chartCall[1].data.labels).toBeUndefined();
    });

    test('QA 2.4: the y-axis has maxTicksLimit to prevent tick instability', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const chartCall = (Chart as unknown as jest.Mock).mock.calls[0];
      const yTicks = chartCall[1].options.scales.y.ticks;

      expect(typeof yTicks.maxTicksLimit).toBe('number');
      expect(yTicks.maxTicksLimit).toBeGreaterThan(0);
    });

    test('QA 2.5: the y-axis formatter does not collapse distinct floats to the same integer label', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const chartCall = (Chart as unknown as jest.Mock).mock.calls[0];
      const y1Cb = chartCall[1].options.scales.y.ticks.callback;

      // Two floats that differ in decimal but share the same integer part must
      // produce distinct labels (QA 2.5).
      const label1 = y1Cb(0.1) as string;
      const label2 = y1Cb(0.2) as string;
      expect(label1).not.toBe(label2);

      // True integers should not carry trailing decimals
      expect(y1Cb(5)).toBe('$5');
      expect(y1Cb(0)).toBe('$0');
    });
  });

  describe('chart callback functions', () => {
    test('y-axis tick callback formats currency for number values', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const chartCall = (Chart as unknown as jest.Mock).mock.calls[0];
      const yAxisCallback = chartCall[1].options.scales.y.ticks.callback;

      // Compact shared formatter, not `$${x.toFixed(2)}` (issue #749)
      expect(yAxisCallback(50)).toBe('$50');
      expect(yAxisCallback(123.456)).toBe('$123.46');
      expect(yAxisCallback(1500)).toBe('$1.5K');
      expect(yAxisCallback(12500)).toBe('$12.5K');
    });

    test('y-axis tick callback formats currency for string values', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const chartCall = (Chart as unknown as jest.Mock).mock.calls[0];
      const yAxisCallback = chartCall[1].options.scales.y.ticks.callback;

      // Test with string value
      expect(yAxisCallback('75.5')).toBe('$75.5');
      expect(yAxisCallback('100')).toBe('$100');
      expect(yAxisCallback('2500')).toBe('$2.5K');
    });

    test('tooltip label callback formats period savings with unit suffix (default monthly)', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const chartCall = (Chart as unknown as jest.Mock).mock.calls[0];
      const tooltipLabelCallback = chartCall[1].options.plugins.tooltip.callbacks.label;

      // Test period savings (datasetIndex 0) -- must be 2 decimal places (QA 2.3)
      const periodContext = {
        raw: 25.5678,
        datasetIndex: 0,
        dataset: { label: 'Period Savings' }
      };
      expect(tooltipLabelCallback(periodContext)).toBe('Period Savings: $25.57/mo');
    });

    test('tooltip label callback formats cumulative savings without unit suffix', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);
      (document.getElementById('savings-series') as HTMLSelectElement).value = 'cumulative';

      await loadSavingsHistory();

      const chartCall = (Chart as unknown as jest.Mock).mock.calls[0];
      const tooltipLabelCallback = chartCall[1].options.plugins.tooltip.callbacks.label;

      const cumulativeContext = {
        raw: 1234.56,
        datasetIndex: 0,
        dataset: { label: 'Cumulative Savings' }
      };
      expect(tooltipLabelCallback(cumulativeContext)).toBe('Cumulative Savings: $1,234.56');
    });

    test('tooltip label callback handles null/undefined raw value', async () => {
      const mockData = {
        data_points: [
          { timestamp: '2024-01-01T00:00:00Z', total_savings: 10, cumulative_savings: 10, total_upfront: 100, purchase_count: 1 }
        ]
      };
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      await loadSavingsHistory();

      const chartCall = (Chart as unknown as jest.Mock).mock.calls[0];
      const tooltipLabelCallback = chartCall[1].options.plugins.tooltip.callbacks.label;

      // Test with null/undefined raw value -- 2 decimal places for period savings (QA 2.3)
      const nullContext = {
        raw: null,
        datasetIndex: 0,
        dataset: { label: 'Period Savings' }
      };
      expect(tooltipLabelCallback(nullContext)).toBe('Period Savings: $0.00/mo');

      const undefinedContext = {
        raw: undefined,
        datasetIndex: 0,
        dataset: { label: 'Period Savings' }
      };
      expect(tooltipLabelCallback(undefinedContext)).toBe('Period Savings: $0.00/mo');
    });
  });
});
