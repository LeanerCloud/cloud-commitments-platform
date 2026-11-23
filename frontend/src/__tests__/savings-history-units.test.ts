/**
 * Savings History module tests: unit dropdown and unit conversion helpers.
 * Split from savings-history.test.ts to stay under the 500-line ceiling; the
 * module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import './savings-history-harness';
import { loadSavingsHistory, initSavingsHistory } from '../modules/savings-history';
import { getSavingsAnalytics } from '../api';
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

  // Freeze only Date so promise/timer-based helpers (flushPromises) keep working.
  const pinDateOnly = (iso: string): void => {
    jest.useFakeTimers({
      now: new Date(iso),
      doNotFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'setImmediate', 'clearImmediate', 'nextTick', 'queueMicrotask', 'requestAnimationFrame', 'cancelAnimationFrame', 'performance'],
    });
  };

  // Issue #750: unit dropdown wires + KPI display per unit
  describe('unit dropdown (issue #750)', () => {
    // Windows are local calendar days (getPeriodDates) and the average divides
    // by the actual elapsed count, so a window spanning a DST change (jest pins
    // TZ=America/New_York; DST ends 2026-11-01) is not exactly 90 * 24h. Pin the
    // clock to a date whose 90d window has no DST change.
    beforeEach(() => { pinDateOnly('2026-10-01T12:17:15.123Z'); });
    const mockData = {
      summary: { total_monthly_savings: 730, total_annual_savings: 8760 }, // $730/mo cumulative total
      data_points: [
        { timestamp: '2024-01-01T00:00:00Z', total_savings: 730, cumulative_savings: 730, total_upfront: 100, purchase_count: 1 }
      ]
    };
    // 90 daily buckets of $730/mo over the default 90d window: the window
    // average (total / 90 days) is exactly $730/mo.
    const windowAvgData = {
      summary: { total_monthly_savings: 730 * 90, total_annual_savings: 730 * 90 * 12 },
      data_points: Array.from({ length: 90 }, (_, i) => ({
        timestamp: new Date(Date.UTC(2024, 0, 1 + i)).toISOString(),
        total_savings: 730, cumulative_savings: 730 * (i + 1), total_upfront: 100, purchase_count: 1,
      })),
    };

    test('default unit is monthly -- avg and peak show /mo suffix', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);
      await loadSavingsHistory();

      const avgEl = document.getElementById('avg-hourly-savings');
      const peakEl = document.getElementById('peak-savings');
      expect(avgEl?.textContent).toContain('/mo');
      expect(peakEl?.textContent).toContain('/mo');
    });

    test('hourly unit -- avg and peak show /hr suffix and value is monthly/730', async () => {
      const unitSelect = document.getElementById('savings-unit') as HTMLSelectElement;
      unitSelect.value = 'hourly';
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(windowAvgData);
      await loadSavingsHistory();

      const avgEl = document.getElementById('avg-hourly-savings');
      expect(avgEl?.textContent).toContain('/hr');
      // 730 / 730 = $1.00/hr
      expect(avgEl?.textContent).toContain('$1.00');
    });

    test('yearly unit -- avg and peak show /yr suffix and value is monthly*12', async () => {
      const unitSelect = document.getElementById('savings-unit') as HTMLSelectElement;
      unitSelect.value = 'yearly';
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(windowAvgData);
      await loadSavingsHistory();

      const avgEl = document.getElementById('avg-hourly-savings');
      expect(avgEl?.textContent).toContain('/yr');
      // 730 * 12 = $8,760.00
      expect(avgEl?.textContent).toContain('$8,760.00');
    });

    test('avg-savings-label heading changes to reflect selected unit', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      // Monthly (default)
      await loadSavingsHistory();
      expect(document.getElementById('avg-savings-label')?.textContent).toBe('Avg monthly savings');

      // Switch to hourly
      const unitSelect = document.getElementById('savings-unit') as HTMLSelectElement;
      unitSelect.value = 'hourly';
      await loadSavingsHistory();
      expect(document.getElementById('avg-savings-label')?.textContent).toBe('Avg hourly savings');

      // Switch to yearly
      unitSelect.value = 'yearly';
      await loadSavingsHistory();
      expect(document.getElementById('avg-savings-label')?.textContent).toBe('Avg yearly savings');
    });

    test('unit dropdown change triggers loadSavingsHistory via initSavingsHistory', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });
      initSavingsHistory();

      const unitSelect = document.getElementById('savings-unit') as HTMLSelectElement;
      unitSelect.value = 'hourly';
      unitSelect.dispatchEvent(new Event('change'));

      await flushPromises();
      expect(getSavingsAnalytics).toHaveBeenCalled();
    });

    test('initSavingsHistory called twice does not stack duplicate unit-change handlers', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });
      initSavingsHistory();
      initSavingsHistory();

      const unitSelect = document.getElementById('savings-unit') as HTMLSelectElement;
      unitSelect.value = 'hourly';
      (getSavingsAnalytics as jest.Mock).mockClear();
      unitSelect.dispatchEvent(new Event('change'));

      await flushPromises();
      // Should fire exactly once (no stacked duplicate listeners)
      expect(getSavingsAnalytics).toHaveBeenCalledTimes(1);
    });

    test('period savings (cumulative total) also converts with unit toggle', async () => {
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);

      // Monthly: value is clean dollar total; sub-line reflects view mode
      await loadSavingsHistory();
      const periodElMonthly = document.getElementById('period-savings');
      expect(periodElMonthly?.textContent).toBe('$730.00/mo');
      expect(document.getElementById('period-savings-unit')?.textContent).toContain('monthly');

      // Hourly: 730 / 730 = 1.00; sub-line updates to 'hourly'
      const unitSelect = document.getElementById('savings-unit') as HTMLSelectElement;
      unitSelect.value = 'hourly';
      await loadSavingsHistory();
      expect(document.getElementById('period-savings')?.textContent).toBe('$1.00/hr');
      expect(document.getElementById('period-savings-unit')?.textContent).toContain('hourly');

      // Yearly: 730 * 12 = 8760 -> $8,760.00; sub-line updates to 'yearly'
      unitSelect.value = 'yearly';
      await loadSavingsHistory();
      expect(document.getElementById('period-savings')?.textContent).toBe('$8,760.00/yr');
      expect(document.getElementById('period-savings-unit')?.textContent).toContain('yearly');
    });

    test('chart tooltip uses selected unit suffix for period savings dataset', async () => {
      const unitSelect = document.getElementById('savings-unit') as HTMLSelectElement;
      unitSelect.value = 'hourly';
      (getSavingsAnalytics as jest.Mock).mockResolvedValue(mockData);
      await loadSavingsHistory();

      const chartCall = (Chart as unknown as jest.Mock).mock.calls[0];
      const tooltipCb = chartCall[1].options.plugins.tooltip.callbacks.label;
      const ctx = { raw: 1.0, datasetIndex: 0, dataset: { label: 'Period Savings' } };
      expect(tooltipCb(ctx)).toContain('/hr');
    });
  });
});
