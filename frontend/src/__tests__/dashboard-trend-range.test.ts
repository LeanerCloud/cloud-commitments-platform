/**
 * Dashboard module tests: savings-trend chart range, axis and data point tests.
 * Split from dashboard.test.ts to stay under the 500-line ceiling; the
 * module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, mockConfirmDialog, mockGroupRecsByCell, mockPageLevelRange, mockFormatSavingsRange } from './dashboard-harness';
import { loadSavingsTrendChart, setupSavingsTrendHandlers } from '../dashboard';
import { Chart } from 'chart.js';
import * as api from '../api';
import * as state from '../state';

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

    test('uses daily interval for the default 90-day range', async () => {
      await loadSavingsTrendChart();

      expect(api.getSavingsAnalytics).toHaveBeenCalledWith(
        expect.objectContaining({ interval: 'daily' })
      );
    });

    test('clicking a different trend-range button re-fetches with the new bucket', async () => {
      setupSavingsTrendHandlers();
      (api.getSavingsAnalytics as jest.Mock).mockClear();

      (document.querySelector('.trend-range[data-range="30"]') as HTMLButtonElement).click();
      await new Promise(r => setTimeout(r, 0));

      expect(api.getSavingsAnalytics).toHaveBeenCalledWith(
        expect.objectContaining({ interval: 'daily' })
      );
      const call = (api.getSavingsAnalytics as jest.Mock).mock.calls[0]?.[0];
      const span = new Date(call.end).getTime() - new Date(call.start).getTime();
      expect(Math.round(span / 86400_000)).toBe(30);
    });

    // QA 3.1: 'all' range must NOT send the epoch sentinel (1970-01-01) as
    // the start param. The backend caps the date range at 366 days and returns
    // HTTP 400 for a 1970 start, which the catch block rendered as an empty-
    // state error. The fix uses a rolling ~365-day window that fits the cap.
    test('All range sends a start within ~365 days of now, not the epoch sentinel (QA 3.1)', async () => {
      // Add an 'all' button and make it active.
      const bAll = document.createElement('button');
      bAll.className = 'trend-range';
      bAll.dataset['range'] = 'all';
      bAll.textContent = '1y'; // labeled 1y since #52; data-range stays 'all'
      document.body.appendChild(bAll);
      setupSavingsTrendHandlers();
      // Override the beforeEach default of empty data_points so the chart
      // actually renders -- the test asserts the empty-state stays hidden.
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: [
          { timestamp: new Date(Date.now() - 86400_000).toISOString(), cumulative_savings: 450, total_savings: 450, total_upfront: 0, purchase_count: 1 },
          { timestamp: new Date().toISOString(), cumulative_savings: 500, total_savings: 50, total_upfront: 0, purchase_count: 1 },
        ],
      });
      (api.getSavingsAnalytics as jest.Mock).mockClear();

      bAll.click();
      await new Promise(r => setTimeout(r, 0));

      const call = (api.getSavingsAnalytics as jest.Mock).mock.calls[0]?.[0];
      // Must NOT be the epoch sentinel -- that causes HTTP 400 from the backend.
      expect(call.start).not.toBe('1970-01-01T00:00:00Z');
      // Start must be within ~365 days of now (within 366 days to fit the cap).
      const startMs = new Date(call.start as string).getTime();
      const nowMs = Date.now();
      const ageMs = nowMs - startMs;
      expect(ageMs).toBeGreaterThan(0);
      expect(ageMs).toBeLessThanOrEqual(366 * 86400_000 + 5_000); // 5s clock tolerance
      // The chart must render (not show empty-state) when the API returns data.
      const empty = document.getElementById('savings-trend-empty');
      expect(empty?.classList.contains('hidden')).toBe(true);
    });

    // QA row 405, step 3.1 — x-axis windowing behaviour.
    // Policy (aligned with QA 2.3 tests below): a successful-but-empty
    // response shows the empty-state banner, not blank axes. The original
    // QA 3.1 intent was to avoid showing a broken chart widget; QA 2.3
    // superseded that with an explicit "show a friendly message" policy
    // (see tests 'empty-state shows filter name' and 'empty-state shows
    // generic message' at the end of this describe block).

    test('shows empty-state (not canvas) when there are no data points and no filter is active (QA 3.1 / QA 2.3 policy)', async () => {
      // No active account or provider filter (default mock state: [] and '').
      // Expect the empty-state banner with generic text, canvas hidden.
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

      await loadSavingsTrendChart();

      const canvas = document.getElementById('savings-trend-chart');
      const empty = document.getElementById('savings-trend-empty');
      expect(canvas?.classList.contains('hidden')).toBe(true);
      expect(empty?.classList.contains('hidden')).toBe(false);
      expect(empty?.textContent).toContain('No savings recorded in the last');
    });

    test('x-axis min/max spans the selected window regardless of data point dates (QA 3.1)', async () => {
      // Add a 7d button before wiring handlers so setupSavingsTrendHandlers
      // attaches a click listener to it. Drive it to '7' for a deterministic
      // window size independent of prior test state.
      const b7 = document.createElement('button');
      b7.className = 'trend-range';
      b7.dataset['range'] = '7';
      b7.textContent = '7d';
      document.body.appendChild(b7);
      setupSavingsTrendHandlers();

      // Two purchases near the start of the 7-day window.
      const now = Date.now();
      const purchaseTs = new Date(now - 6 * 86400_000).toISOString();
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: [
          { timestamp: purchaseTs, cumulative_savings: 100, total_savings: 5, total_upfront: 200, purchase_count: 1 },
          { timestamp: new Date(now - 5 * 86400_000).toISOString(), cumulative_savings: 105, total_savings: 5, total_upfront: 200, purchase_count: 1 },
        ],
      });

      (Chart as unknown as jest.Mock).mockClear();
      b7.click();
      await new Promise(r => setTimeout(r, 0));

      const chartCalls = (Chart as unknown as jest.Mock).mock.calls;
      expect(chartCalls.length).toBeGreaterThan(0);
      const chartCall = chartCalls[chartCalls.length - 1];
      const xScale = chartCall[1].options.scales.x;
      // min must be ~7 days before max; allow 60-second clock skew in tests.
      expect(xScale.max - xScale.min).toBeGreaterThanOrEqual(6 * 86400_000);
      expect(xScale.max - xScale.min).toBeLessThanOrEqual(8 * 86400_000);
    });

    test('data points use {x: timestamp_ms, y: value} so they are positioned by real date (QA 3.1)', async () => {
      const purchaseTs = '2024-06-15T12:00:00Z';
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: [
          { timestamp: purchaseTs, cumulative_savings: 250, total_savings: 10, total_upfront: 500, purchase_count: 1 },
          { timestamp: '2024-06-16T12:00:00Z', cumulative_savings: 260, total_savings: 10, total_upfront: 500, purchase_count: 1 },
        ],
      });

      await loadSavingsTrendChart();

      const chartCall = (Chart as unknown as jest.Mock).mock.calls[0];
      const dataset = chartCall[1].data.datasets[0];
      expect(dataset.data[0]).toMatchObject({ x: new Date(purchaseTs).getTime(), y: 250 });
    });

    // Issue #188: one data point would paint a lone dot on a blank canvas.
    test('a single data point shows the sparse-data message instead of a chart (#188)', async () => {
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: [{ timestamp: '2026-10-20T12:00:00Z', cumulative_savings: 1.1, total_savings: 1.1, total_upfront: 0, purchase_count: 1 }],
      });
      (Chart as unknown as jest.Mock).mockClear();

      await loadSavingsTrendChart();

      const canvas = document.getElementById('savings-trend-chart');
      const empty = document.getElementById('savings-trend-empty');
      expect(canvas?.classList.contains('hidden')).toBe(true);
      expect(empty?.classList.contains('hidden')).toBe(false);
      // formatCurrency is mocked in this file; the exact copy is pinned in chart-utils.test.ts.
      expect(empty?.textContent).toContain('Not enough history yet to show a trend.');
      expect(empty?.textContent).toContain('Oct 20, 2026');
      expect(Chart).not.toHaveBeenCalled();
    });

    test('going from one data point back to two redraws the chart and hides the sparse message (#188)', async () => {
      const one = { timestamp: '2026-10-20T12:00:00Z', cumulative_savings: 1.1, total_savings: 1.1, total_upfront: 0, purchase_count: 1 };
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [one] });
      await loadSavingsTrendChart();
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: [one, { ...one, timestamp: '2026-10-21T12:00:00Z', cumulative_savings: 2.2 }],
      });
      (Chart as unknown as jest.Mock).mockClear();

      await loadSavingsTrendChart();

      expect(Chart).toHaveBeenCalledTimes(1);
      expect(document.getElementById('savings-trend-chart')?.classList.contains('hidden')).toBe(false);
      expect(document.getElementById('savings-trend-empty')?.classList.contains('hidden')).toBe(true);
    });

    test('fetch error shows error stub and hides canvas (not the empty-axes path) (QA 3.1)', async () => {
      (api.getSavingsAnalytics as jest.Mock).mockRejectedValue(new Error('503'));

      await loadSavingsTrendChart();

      const canvas = document.getElementById('savings-trend-chart');
      const empty = document.getElementById('savings-trend-empty');
      expect(canvas?.classList.contains('hidden')).toBe(true);
      expect(empty?.classList.contains('hidden')).toBe(false);
    });

    // QA 3.2: tooltip title callback must return a formatted date string, not
    // the raw 13-digit millisecond timestamp that Chart.js renders by default
    // when no title callback is configured.
    test('tooltip title callback returns a formatted date string, not a raw ms timestamp (QA 3.2)', async () => {
      const purchaseTs = '2024-06-15T12:00:00Z';
      (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({
        data_points: [
          { timestamp: purchaseTs, cumulative_savings: 250, total_savings: 10, total_upfront: 500, purchase_count: 1 },
          { timestamp: '2024-06-16T12:00:00Z', cumulative_savings: 260, total_savings: 10, total_upfront: 500, purchase_count: 1 },
        ],
      });

      await loadSavingsTrendChart();

      const chartCall = (Chart as unknown as jest.Mock).mock.calls[0];
      const titleCb = chartCall[1].options.plugins.tooltip.callbacks.title as
        (items: Array<{ raw: { x: number; y: number } }>) => string;

      const xMs = new Date(purchaseTs).getTime();
      const result = titleCb([{ raw: { x: xMs, y: 250 } }]);

      // Must be a non-empty string.
      expect(typeof result).toBe('string');
      expect(result.length).toBeGreaterThan(0);
      // Must NOT be the raw numeric millisecond value.
      expect(result).not.toBe(String(xMs));
      expect(result).not.toMatch(/^\d{13}$/);
      // Must contain recognizable date text (month abbreviation).
      expect(result).toMatch(/Jun\s+\d+/);
      // Must carry the year so points a year apart are distinguishable (#53).
      expect(result).toMatch(/2024/);
    });
  });
});
