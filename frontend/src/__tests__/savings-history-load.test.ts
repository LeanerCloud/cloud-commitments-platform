/**
 * Savings History module tests: initSavingsHistory, subscriber wiring and the
 * second half of loadSavingsHistory. Split from savings-history.test.ts to stay
 * under the 500-line ceiling; the module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import './savings-history-harness';
import { initSavingsHistory } from '../modules/savings-history';
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
});
