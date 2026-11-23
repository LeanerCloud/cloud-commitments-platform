/**
 * Dashboard module tests
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, mockConfirmDialog, mockGroupRecsByCell, mockPageLevelRange, mockFormatSavingsRange, resetDashboardState } from './dashboard-harness';
import { loadDashboard, loadYtdSparkline, setupSavingsTrendHandlers, formatTrendAxisTick } from '../dashboard';
import { formatTrendTooltipTitle } from '../modules/chart-utils';
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

  // Issue #53: the tooltip title includes the year; the axis tick stays compact.
  describe('formatTrendTooltipTitle (issue #53)', () => {
    test.each(['daily', 'weekly'] as const)('%s titles include the year', (interval) => {
      const ts = new Date('2024-06-15T12:00:00Z').getTime();
      expect(formatTrendTooltipTitle(ts, interval)).toMatch(/^Jun 1[45], 2024$/);
    });

    test('hourly titles include the year and the time', () => {
      const label = formatTrendTooltipTitle(new Date('2024-03-15T14:30:00Z').getTime(), 'hourly');
      expect(label).toMatch(/Mar\s+1[45], 2024/);
      expect(label).toMatch(/\d{2}:30/);
    });

    test('the same calendar day in different years produces different titles', () => {
      const a = formatTrendTooltipTitle(new Date('2024-06-15T12:00:00Z').getTime(), 'weekly');
      const b = formatTrendTooltipTitle(new Date('2025-06-15T12:00:00Z').getTime(), 'weekly');
      expect(a).not.toBe(b);
      // The compact axis tick for the same pair is identical, which is why the
      // tooltip cannot reuse it.
      expect(formatTrendAxisTick(new Date('2024-06-15T12:00:00Z').getTime(), 'weekly'))
        .toBe(formatTrendAxisTick(new Date('2025-06-15T12:00:00Z').getTime(), 'weekly'));
    });
  });

  describe('formatTrendAxisTick (QA 3.1)', () => {
    test('formats hourly ticks with date + time', () => {
      // Use a fixed UTC timestamp: 2024-03-15 14:30 UTC.
      const ts = new Date('2024-03-15T14:30:00Z').getTime();
      const label = formatTrendAxisTick(ts, 'hourly');
      // Expect something like "Mar 15, 14:30" — locale-dependent but must contain the date.
      expect(label).toMatch(/Mar\s+\d+/);
    });

    test('formats daily ticks with short date only', () => {
      const ts = new Date('2024-03-15T00:00:00Z').getTime();
      const label = formatTrendAxisTick(ts, 'daily');
      expect(label).toMatch(/Mar\s+\d+/);
      // No colon (no time component).
      expect(label).not.toMatch(/:/);
    });

    test('formats weekly ticks with short date only', () => {
      const ts = new Date('2024-03-15T00:00:00Z').getTime();
      const label = formatTrendAxisTick(ts, 'weekly');
      expect(label).not.toMatch(/:/);
    });
  });

  // Issue #185 invariant — clear account state BEFORE awaiting the
  // account-list refetch on provider change — moved from dashboard.ts
  // to topbar-filters.ts as part of issue #344 T2. The dashboard no
  // longer owns the provider-change handler; it just reloads in
  // response to state.subscribeProvider. The ordering test now lives
  // alongside the owning module in topbar-filters.test.ts. See
  // src/topbar-filters.ts::initTopbarFilters for the implementation.
  describe.skip('Issue #185: provider switch clears stale account state before reload (moved to topbar-filters.test.ts)', () => {
    test('placeholder — see topbar-filters.test.ts', () => {});
  });

  // KPI tile sparklines (issue #340 T6).
  describe('sparkline helpers', () => {
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    const { __test__ } = require('../dashboard') as { __test__: {
      sparklinePoints: (values: readonly number[], w: number, h: number) => string;
      attachSparkline: (key: string, values: readonly number[]) => void;
    } };

    test('sparklinePoints normalizes values into the viewport', () => {
      const pts = __test__.sparklinePoints([0, 50, 100], 80, 24);
      const tokens = pts.split(' ');
      expect(tokens).toHaveLength(3);
      // First point: x=0, y=height (lowest value).
      expect(tokens[0]).toBe('0.0,24.0');
      // Last point: x=width, y=0 (highest value).
      expect(tokens[2]).toBe('80.0,0.0');
    });

    test('sparklinePoints returns empty string for <2 values', () => {
      expect(__test__.sparklinePoints([], 80, 24)).toBe('');
      expect(__test__.sparklinePoints([42], 80, 24)).toBe('');
    });

    test('sparklinePoints handles flat series (all values equal)', () => {
      const pts = __test__.sparklinePoints([5, 5, 5], 80, 24);
      // Range collapses to 1 so y = height for all points (no division by 0).
      expect(pts.split(' ')).toHaveLength(3);
      expect(pts).not.toContain('NaN');
    });

    test('attachSparkline draws a polyline into the matching svg', () => {
      const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
      svg.classList.add('kpi-tile-spark');
      svg.dataset['sparkKey'] = 'ytd';
      document.body.appendChild(svg);

      __test__.attachSparkline('ytd', [10, 20, 30]);

      const polyline = svg.querySelector('polyline');
      expect(polyline).toBeTruthy();
      expect(polyline?.getAttribute('points')).toMatch(/\d+\.\d+,\d+\.\d+/);
      expect(polyline?.getAttribute('stroke')).toBe('currentColor');

      document.body.removeChild(svg);
    });

    test('attachSparkline no-ops when placeholder is missing', () => {
      // No DOM, no throw.
      expect(() => __test__.attachSparkline('nonexistent', [1, 2, 3])).not.toThrow();
    });

    test('attachSparkline no-ops with <2 values (silent skip)', () => {
      const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
      svg.classList.add('kpi-tile-spark');
      svg.dataset['sparkKey'] = 'savings';
      document.body.appendChild(svg);

      __test__.attachSparkline('savings', [42]);

      expect(svg.querySelector('polyline')).toBeNull();
      document.body.removeChild(svg);
    });
  });

  // H-3 regression: absent API coverage/count fields must render '—', not '$0'/'80%'/'0'.
  // Pre-fix: `data.current_coverage || 0` fabricated '0%'; `data.target_coverage || 80`
  // fabricated '80%'; `data.active_commitments || 0` fabricated '0'; the savings catch
  // block fell back to formatCurrency(0). These tests must FAIL on pre-fix code.
  describe('H-3: absent dashboard KPI fields render the em-dash sentinel, not fabricated values', () => {
    test('absent target_coverage shows an em dash, not hardcoded 80%', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        // target_coverage deliberately absent
        total_recommendations: 1,
        active_commitments: 1,
        committed_monthly: 100,
        current_coverage: 60,
        ytd_savings: 0,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadDashboard();

      const summary = document.getElementById('summary');
      // Must not show the hardcoded 80% fallback
      expect(summary?.textContent).not.toContain('Target: 80.0%');
      expect(summary?.textContent).not.toContain('Target: 80%');
      // Must show the '—' sentinel for absent target
      expect(summary?.textContent).toContain('Target: —');
    });

    test('absent current_coverage shows an em dash, not fabricated 0%', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        // current_coverage deliberately absent
        total_recommendations: 2,
        active_commitments: 1,
        committed_monthly: 100,
        target_coverage: 80,
        ytd_savings: 0,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadDashboard();

      const summaryEl = document.getElementById('summary');
      // Find the Coverage tile specifically to check its value
      const tiles = summaryEl?.querySelectorAll('.kpi-tile');
      const coverageTile = Array.from(tiles ?? []).find(t =>
        t.textContent?.includes('Current Coverage')
      );
      expect(coverageTile).toBeDefined();
      const valueEl = coverageTile?.querySelector('.kpi-tile-value');
      // Must show the em dash (not a fabricated '0.0%') when current_coverage is absent
      expect(valueEl?.textContent).toBe('—');
    });

    test('absent active_commitments shows an em dash, not fabricated 0', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        total_recommendations: 1,
        // active_commitments deliberately absent
        committed_monthly: 0,
        current_coverage: 70,
        target_coverage: 80,
        ytd_savings: 0,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadDashboard();

      const summaryEl = document.getElementById('summary');
      // Find the Active Commitments tile specifically
      const tiles = summaryEl?.querySelectorAll('.kpi-tile');
      const commitmentsTile = Array.from(tiles ?? []).find(t =>
        t.textContent?.includes('Active Commitments')
      );
      expect(commitmentsTile).toBeDefined();
      // Must not show fabricated '0'; must show '—'
      const valueEl = commitmentsTile?.querySelector('.kpi-tile-value');
      expect(valueEl?.textContent).toBe('—');
    });

    test('savings range catch block shows an em dash, not $0 when computation throws', async () => {
      // Force the groupRecsByCell mock to throw so the catch block fires.
      mockGroupRecsByCell.mockImplementationOnce(() => { throw new Error('compute error'); });
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        total_recommendations: 1,
        active_commitments: 1,
        committed_monthly: 100,
        current_coverage: 70,
        target_coverage: 80,
        ytd_savings: 0,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadDashboard();

      const summaryEl = document.getElementById('summary');
      const tiles = summaryEl?.querySelectorAll('.kpi-tile');
      const savingsTile = Array.from(tiles ?? []).find(t =>
        t.textContent?.includes('Potential Monthly Savings')
      );
      expect(savingsTile).toBeDefined();
      const valueEl = savingsTile?.querySelector('.kpi-tile-value');
      // Must show the em dash (not '$0') when the savings computation fails,
      // and not in savings green: it is not a saving (issue #739).
      expect(valueEl?.textContent).toBe('—');
      expect(valueEl?.classList.contains('savings')).toBe(false);
    });
  });
});

describe('YTD Savings tile sparkline (#317)', () => {
  function addYtdTile(): SVGSVGElement {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.classList.add('kpi-tile-spark', 'hidden');
    svg.dataset['sparkKey'] = 'ytd';
    const canvas = document.createElement('canvas');
    canvas.id = 'savings-trend-chart';
    const b7 = document.createElement('button');
    b7.className = 'trend-range';
    b7.dataset['range'] = '7';
    document.body.replaceChildren(svg, canvas, b7);
    return svg;
  }

  beforeEach(() => {
    jest.clearAllMocks();
    (state.getCurrentProvider as jest.Mock).mockReturnValue('aws');
    (state.getCurrentAccountIDs as jest.Mock).mockReturnValue(['uuid-acct-1']);
  });

  test('requests 1 January (UTC) to now, scoped by the chips, whatever the trend range', async () => {
    addYtdTile();
    (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

    await loadYtdSparkline();

    expect(api.getSavingsAnalytics).toHaveBeenCalledTimes(1);
    const filters = (api.getSavingsAnalytics as jest.Mock).mock.calls[0][0];
    const start = new Date(filters.start);
    expect(start.toISOString()).toBe(new Date(Date.UTC(new Date().getUTCFullYear(), 0, 1)).toISOString());
    expect(filters).toEqual(expect.objectContaining({ provider: 'aws', account_ids: ['uuid-acct-1'] }));
  });

  test('draws the YTD series into the tile', async () => {
    const svg = addYtdTile();
    (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({
      data_points: [
        { timestamp: '2026-01-05T00:00:00Z', cumulative_savings: 10 },
        { timestamp: '2026-05-05T00:00:00Z', cumulative_savings: 40 },
      ],
    });

    await loadYtdSparkline();

    expect(svg.classList.contains('hidden')).toBe(false);
    expect(svg.querySelector('polyline')).not.toBeNull();
  });

  test('the trend chart (7-day range) no longer redraws the YTD tile', async () => {
    const svg = addYtdTile();
    (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({
      data_points: [
        { timestamp: new Date(Date.now() - 2 * 86400_000).toISOString(), cumulative_savings: 1 },
        { timestamp: new Date().toISOString(), cumulative_savings: 2 },
      ],
    });
    setupSavingsTrendHandlers();
    (document.querySelector('.trend-range[data-range="7"]') as HTMLButtonElement).click();
    await new Promise((r) => setTimeout(r, 0));

    expect(api.getSavingsAnalytics).toHaveBeenCalledTimes(1);
    expect(svg.querySelector('polyline')).toBeNull();
  });

  test('a failed fetch hides the sparkline and a missing tile skips the request', async () => {
    const svg = addYtdTile();
    const warn = jest.spyOn(console, 'warn').mockImplementation(() => {});
    (api.getSavingsAnalytics as jest.Mock).mockRejectedValue(new Error('503'));
    await loadYtdSparkline();
    expect(svg.classList.contains('hidden')).toBe(true);

    document.body.replaceChildren();
    (api.getSavingsAnalytics as jest.Mock).mockClear();
    await loadYtdSparkline();
    expect(api.getSavingsAnalytics).not.toHaveBeenCalled();
    warn.mockRestore();
  });
});
