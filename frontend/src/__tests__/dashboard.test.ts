/**
 * Dashboard module tests
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, mockConfirmDialog, mockGroupRecsByCell, mockPageLevelRange, mockFormatSavingsRange } from './dashboard-harness';
import { loadDashboard, loadYtdSparkline, setupSavingsTrendHandlers, formatTrendAxisTick } from '../dashboard';
import { formatTrendTooltipTitle } from '../modules/chart-utils';
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

  // Issue #765: per-service savings-range bar chart.
  describe('renderSavingsByService (issue #769)', () => {
    // Import the public helpers from dashboard. The module is already
    // loaded above via the jest.mock chain, so we can import directly.
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    const { renderSavingsByService, savingsLayerColors } = require('../dashboard') as {
      renderSavingsByService: (recs: unknown[], byService?: Record<string, { potential_savings: number; current_savings: number }>, filterDesc?: string) => void;
      savingsLayerColors: () => { current: string; lowestOption: string; upside: string };
    };

    function buildDOM(): void {
      const canvas = document.createElement('canvas');
      canvas.id = 'savings-by-service-chart';
      const empty = document.createElement('p');
      empty.id = 'savings-by-service-empty';
      empty.className = 'empty hidden';
      const section = document.createElement('section');
      section.id = 'savings-by-service-section';
      const h3 = document.createElement('h3');
      h3.textContent = 'Potential savings range per service';
      section.appendChild(h3);
      section.appendChild(canvas);
      section.appendChild(empty);
      document.body.appendChild(section);
    }

    /** Build a minimal recommendation fixture. */
    function rec(service: string, savings: number, term = 1, payment = 'no_upfront'): unknown {
      return {
        id: `${service}-${savings}`,
        provider: 'aws',
        service,
        region: 'us-east-1',
        resource_type: 'Standard',
        count: 1,
        term,
        payment,
        upfront_cost: 0,
        monthly_cost: 0,
        savings,
        selected: false,
        purchased: false,
      };
    }

    beforeEach(() => {
      document.body.innerHTML = '';
      jest.clearAllMocks();
      // Re-apply the recommendation mock resets from the outer beforeEach.
      mockGroupRecsByCell.mockImplementation((recs: unknown[]) => new Map(recs.length ? [['cell-1', recs]] : []));
      mockPageLevelRange.mockImplementation((groups: Map<string, unknown[]>) => {
        if (groups.size === 0) return { savingsMin: 0, savingsMax: 0, cellCount: 0 };
        return { savingsMin: 300, savingsMax: 400, cellCount: groups.size };
      });
      mockFormatSavingsRange.mockImplementation((min: number, max: number) => min === max ? `$${min}` : `$${min} – $${max}`);
      (api.getRecommendations as jest.Mock).mockResolvedValue([]);
    });

    // renderSavingsByService DOM behaviour tests (now driven by recommendation fixtures).
    describe('DOM behaviour', () => {
      test('shows empty state and hides canvas when no recommendations', () => {
        buildDOM();
        renderSavingsByService([]);
        const canvas = document.getElementById('savings-by-service-chart');
        const empty = document.getElementById('savings-by-service-empty');
        expect(canvas?.classList.contains('hidden')).toBe(true);
        expect(empty?.classList.contains('hidden')).toBe(false);
      });

      test('shows empty state when all recommendations have zero savings', () => {
        buildDOM();
        renderSavingsByService([rec('ec2', 0)]);
        expect(document.getElementById('savings-by-service-chart')?.classList.contains('hidden')).toBe(true);
        expect(document.getElementById('savings-by-service-empty')?.classList.contains('hidden')).toBe(false);
      });

      test('resets heading text to default when dataset becomes empty after a truncated render', () => {
        buildDOM();
        renderSavingsByService([rec('ec2', 100), rec('rds', 50)]);
        const h3 = document.querySelector('#savings-by-service-section h3') as HTMLElement;
        h3.textContent = 'Potential savings range per service (+3 more)'; // simulate stale suffix
        // Second render with empty data -- heading must be reset.
        renderSavingsByService([]);
        expect(h3.textContent).toBe('Potential savings range per service');
      });

      test('renders chart with exactly two services when two services have positive savings', () => {
        buildDOM();
        renderSavingsByService([
          rec('ec2', 100, 1, 'no_upfront'),
          rec('ec2', 300, 3, 'all_upfront'),
          rec('rds', 50, 1, 'no_upfront'),
          rec('rds', 80, 3, 'all_upfront'),
        ]);
        // Chart must be constructed (canvas visible, empty hidden).
        expect(document.getElementById('savings-by-service-chart')?.classList.contains('hidden')).toBe(false);
        expect(document.getElementById('savings-by-service-empty')?.classList.contains('hidden')).toBe(true);
        // Chart.js was called with both services as labels.
        const chartCtor = Chart as unknown as jest.Mock;
        const lastCall = chartCtor.mock.calls[chartCtor.mock.calls.length - 1];
        const chartData = lastCall?.[1] as { data: { labels: string[] } };
        expect(chartData.data.labels).toHaveLength(2);
        expect(chartData.data.labels).toContain('ec2');
        expect(chartData.data.labels).toContain('rds');
      });

      test('three stacked datasets: Current/Committed (bottom), Lowest option, Upside (top)', () => {
        buildDOM();
        // ec2: min rec=100, max rec=400, current=0 -> lowestOption=100, upside=300.
        renderSavingsByService([
          rec('ec2', 100, 1, 'no_upfront'),
          rec('ec2', 400, 3, 'all_upfront'),
        ]);
        const chartCtor = Chart as unknown as jest.Mock;
        const lastCall = chartCtor.mock.calls[chartCtor.mock.calls.length - 1];
        const datasets = (lastCall?.[1] as { data: { datasets: { label: string; data: number[]; stack: string }[] } }).data.datasets;
        const currentDs = datasets.find((d) => d.label === 'Current / Committed');
        const lowestDs = datasets.find((d) => d.label === 'Lowest option');
        const upsideDs = datasets.find((d) => d.label === 'Upside');
        // All three datasets must be present and in the same stack.
        expect(currentDs).toBeDefined();
        expect(lowestDs).toBeDefined();
        expect(upsideDs).toBeDefined();
        expect(currentDs?.stack).toBe('savings');
        expect(lowestDs?.stack).toBe('savings');
        expect(upsideDs?.stack).toBe('savings');
        // Values: current=0 (no byService supplied), lowestOption=100-0=100, upside=400-100=300.
        expect(currentDs?.data[0]).toBe(0);
        expect(lowestDs?.data[0]).toBe(100);
        expect(upsideDs?.data[0]).toBe(300);
        // Tooltip Total must equal the sum of the three visible bar layers (0+100+300=400).
        const tooltipLabel = (lastCall?.[1] as {
          options: { plugins: { tooltip: { callbacks: { label: (ctx: { label: string }) => string[] } } } };
        }).options.plugins.tooltip.callbacks.label({ label: 'ec2' });
        const totalLine = tooltipLabel.find((l) => l.startsWith('Total:'));
        expect(totalLine).toMatch(/\$400/);
      });

      describe('one colour per layer, matching the legend (#189)', () => {
        type ChartCfg = {
          data: { labels: string[]; datasets: { label: string; backgroundColor: unknown }[] };
          options: {
            plugins: { legend: { labels: { generateLabels?: unknown } } };
            scales: { x: { title?: { display?: boolean } } };
          };
        };
        const lastCfg = (): ChartCfg => {
          const calls = (Chart as unknown as jest.Mock).mock.calls;
          return calls[calls.length - 1]?.[1] as ChartCfg;
        };

        test('each layer is one colour on every bar, so the default legend swatch matches it', () => {
          buildDOM();
          renderSavingsByService(
            [rec('ec2', 100), rec('ec2', 500, 3), rec('rds', 300), rec('elasticache', 80)],
            { ec2: { potential_savings: 500, current_savings: 50 } },
          );
          const cfg = lastCfg();
          const colours = Object.fromEntries(cfg.data.datasets.map((d) => [d.label, d.backgroundColor]));
          expect(colours).toEqual({
            'Current / Committed': savingsLayerColors().current,
            'Lowest option': savingsLayerColors().lowestOption,
            Upside: savingsLayerColors().upside,
          });
          // A single string (not a per-bar array) is what Chart.js's default
          // legend draws, so swatch and bars cannot diverge.
          for (const d of cfg.data.datasets) expect(typeof d.backgroundColor).toBe('string');
          expect(cfg.options.plugins.legend.labels.generateLabels).toBeUndefined();
        });

        test('rebuilds with the new layer colours when the color scheme changes (#699)', async () => {
          buildDOM();
          renderSavingsByService([rec('ec2', 500)]);
          const before = lastCfg().data.datasets.map((d) => d.backgroundColor);
          const callsBefore = (Chart as unknown as jest.Mock).mock.calls.length;
          document.documentElement.style.setProperty('--cudly-chart-layer-current', '#6ea8fe');
          document.documentElement.setAttribute('data-theme', 'dark');
          await new Promise((r) => setTimeout(r, 0));
          document.documentElement.removeAttribute('data-theme');
          document.documentElement.removeAttribute('style');
          expect((Chart as unknown as jest.Mock).mock.calls.length).toBeGreaterThan(callsBefore);
          const rebuilt = (Chart as unknown as jest.Mock).mock.calls[callsBefore]?.[1] as ChartCfg;
          const current = rebuilt.data.datasets.find((d) => d.label === 'Current / Committed');
          expect(current?.backgroundColor).toBe('#6ea8fe');
          expect(before).not.toContain('#6ea8fe');
        });

        test('the three layers use three distinct colours', () => {
          expect(new Set(Object.values(savingsLayerColors())).size).toBe(3);
        });

        test('colours do not change when a service changes rank', () => {
          buildDOM();
          renderSavingsByService([rec('ec2', 500), rec('rds', 300)]);
          const before = lastCfg().data.datasets.map((d) => d.backgroundColor);
          renderSavingsByService([rec('ec2', 120), rec('rds', 900)]);
          const after = lastCfg();
          expect(after.data.labels[0]).toBe('rds');
          expect(after.data.datasets.map((d) => d.backgroundColor)).toEqual(before);
        });

        test('drops the redundant "Service" x-axis title', () => {
          buildDOM();
          renderSavingsByService([rec('ec2', 500)]);
          expect(lastCfg().options.scales.x.title?.display).not.toBe(true);
        });
      });

      test('tooltip shows the committed value for a service with a commitment but no recommendation (#752)', () => {
        buildDOM();
        renderSavingsByService(
          [rec('ec2', 100, 1, 'no_upfront')],
          { sqldb: { potential_savings: 0, current_savings: 250 } },
        );
        const chartCtor = Chart as unknown as jest.Mock;
        const lastCall = chartCtor.mock.calls[chartCtor.mock.calls.length - 1];
        const label = (lastCall?.[1] as {
          options: { plugins: { tooltip: { callbacks: { label: (ctx: { label: string; dataset: { label: string } }) => string } } } };
        }).options.plugins.tooltip.callbacks.label;
        expect(label({ label: 'sqldb', dataset: { label: 'Current / Committed' } })).toMatch(/Current \/ Committed: \$250/);
      });

      // Issue #908: merged chart draws a current-savings bottom layer per service.
      test('renders Current / Committed layer from byService.current_savings', () => {
        buildDOM();
        // current=250, min rec=100, max rec=400
        // lowestOption = max(0, 100-250) = 0 (committed already exceeds min rec)
        // upside = max(0, 400-100) = 300
        renderSavingsByService(
          [rec('ec2', 100, 1, 'no_upfront'), rec('ec2', 400, 3, 'all_upfront')],
          { ec2: { potential_savings: 400, current_savings: 250 } },
        );
        const chartCtor = Chart as unknown as jest.Mock;
        const lastCall = chartCtor.mock.calls[chartCtor.mock.calls.length - 1];
        const datasets = (lastCall?.[1] as {
          data: { datasets: { label: string; data: number[]; stack: string }[] };
        }).data.datasets;
        const currentDs = datasets.find((d) => d.label === 'Current / Committed');
        expect(currentDs).toBeDefined();
        expect(currentDs?.data[0]).toBe(250);
        // All three datasets share one stack so Current renders below the
        // potential range (dataset order = bottom-to-top in Chart.js stacked bars).
        expect(currentDs?.stack).toBe('savings');
      });

      test('Lowest option clamps to 0 when committed savings exceed min rec', () => {
        buildDOM();
        // current=300, min rec=100 -> lowestOption = max(0, 100-300) = 0
        renderSavingsByService(
          [rec('ec2', 100, 1, 'no_upfront'), rec('ec2', 400, 3, 'all_upfront')],
          { ec2: { potential_savings: 400, current_savings: 300 } },
        );
        const chartCtor = Chart as unknown as jest.Mock;
        const lastCall = chartCtor.mock.calls[chartCtor.mock.calls.length - 1];
        const datasets = (lastCall?.[1] as {
          data: { datasets: { label: string; data: number[] }[] };
        }).data.datasets;
        const lowestDs = datasets.find((d) => d.label === 'Lowest option');
        expect(lowestDs?.data[0]).toBe(0);
      });

      test('current layer defaults to 0 for a service absent from byService', () => {
        buildDOM();
        renderSavingsByService([rec('rds', 300)], {}); // no rds entry
        const chartCtor = Chart as unknown as jest.Mock;
        const lastCall = chartCtor.mock.calls[chartCtor.mock.calls.length - 1];
        const datasets = (lastCall?.[1] as {
          data: { datasets: { label: string; data: number[] }[] };
        }).data.datasets;
        const currentDs = datasets.find((d) => d.label === 'Current / Committed');
        expect(currentDs?.data[0]).toBe(0);
      });

      // Issue #769 follow-up (2x height + stacking): all three datasets must
      // share one stack so Current renders visually BELOW the potential range.
      test('all datasets share one stack so Current renders under Potential', () => {
        buildDOM();
        renderSavingsByService(
          [rec('ec2', 100), rec('ec2', 400)],
          { ec2: { potential_savings: 400, current_savings: 150 } },
        );
        const chartCtor = Chart as unknown as jest.Mock;
        const lastCall = chartCtor.mock.calls[chartCtor.mock.calls.length - 1];
        const config = lastCall?.[1] as {
          data: { datasets: { label: string; stack: string }[] };
          options: { scales: { x: { stacked: boolean }; y: { stacked: boolean } } };
        };
        const datasets = config.data.datasets;
        // Every dataset must share the same stack name.
        const stacks = new Set(datasets.map((d) => d.stack));
        expect(stacks.size).toBe(1);
        // Both axes must have stacked: true.
        expect(config.options.scales.x.stacked).toBe(true);
        expect(config.options.scales.y.stacked).toBe(true);
      });

      test('dataset order: Current / Committed is first so it renders at the base', () => {
        buildDOM();
        renderSavingsByService(
          [rec('ec2', 200)],
          { ec2: { potential_savings: 200, current_savings: 80 } },
        );
        const chartCtor = Chart as unknown as jest.Mock;
        const lastCall = chartCtor.mock.calls[chartCtor.mock.calls.length - 1];
        const datasets = (lastCall?.[1] as {
          data: { datasets: { label: string }[] };
        }).data.datasets;
        // Use find to avoid direct index access (TS strict mode).
        const labels = datasets.map((d) => d.label);
        expect(labels[0]).toBe('Current / Committed');
        expect(labels[1]).toBe('Lowest option');
        expect(labels[2]).toBe('Upside');
      });

      test('service present in byService but absent from recs renders Current-only bar', () => {
        buildDOM();
        // No recs for 'lambda'; only byService entry -> single Current band, no Lowest/Upside.
        renderSavingsByService(
          [],
          { lambda: { potential_savings: 0, current_savings: 120 } },
        );
        const canvas = document.getElementById('savings-by-service-chart');
        expect(canvas?.classList.contains('hidden')).toBe(false);
        const chartCtor = Chart as unknown as jest.Mock;
        const lastCall = chartCtor.mock.calls[chartCtor.mock.calls.length - 1];
        const labels = (lastCall?.[1] as { data: { labels: string[] } }).data.labels;
        expect(labels).toContain('lambda');
        const datasets = (lastCall?.[1] as {
          data: { datasets: { label: string; data: number[] }[] };
        }).data.datasets;
        const currentDs = datasets.find((d) => d.label === 'Current / Committed');
        const lowestDs = datasets.find((d) => d.label === 'Lowest option');
        const upsideDs = datasets.find((d) => d.label === 'Upside');
        expect(currentDs?.data[0]).toBe(120);
        expect(lowestDs?.data[0]).toBe(0);
        expect(upsideDs?.data[0]).toBe(0);
      });

      test('services are sorted by visible total (current + lowestOption + upside) descending', () => {
        buildDOM();
        // ec2: current=0, minRec=200, maxRec=200 -> lowestOption=200, upside=0, visibleTotal=200
        // rds: current=150, minRec=500, maxRec=500 -> lowestOption=350, upside=0, visibleTotal=500
        // lambda: current=0, minRec=50, maxRec=50 -> lowestOption=50, upside=0, visibleTotal=50
        // Expected order: rds (500), ec2 (200), lambda (50).
        renderSavingsByService(
          [rec('ec2', 200), rec('rds', 500), rec('lambda', 50)],
          { rds: { potential_savings: 500, current_savings: 150 } },
        );
        const chartCtor = Chart as unknown as jest.Mock;
        const lastCall = chartCtor.mock.calls[chartCtor.mock.calls.length - 1];
        const labels = (lastCall?.[1] as { data: { labels: string[] } }).data.labels;
        expect(labels[0]).toBe('rds');
        expect(labels[1]).toBe('ec2');
        expect(labels[2]).toBe('lambda');
      });

      test('destroys existing chart instance before re-rendering', () => {
        buildDOM();
        const mockDestroyA = jest.fn();
        (Chart as unknown as jest.Mock).mockImplementationOnce(() => ({ destroy: mockDestroyA }));

        const recs = [rec('ec2', 100)];
        renderSavingsByService(recs);
        // Second call must destroy the first chart.
        renderSavingsByService(recs);
        expect(mockDestroyA).toHaveBeenCalled();
      });

      test('no-ops gracefully when canvas is missing from DOM', () => {
        // No buildDOM() call — canvas absent.
        expect(() => renderSavingsByService([rec('ec2', 100)])).not.toThrow();
      });

      // Issue #867: filter-aware empty state.
      test('empty state shows generic text when no filter is active', () => {
        buildDOM();
        renderSavingsByService([], {}, '');
        const empty = document.getElementById('savings-by-service-empty');
        expect(empty?.classList.contains('hidden')).toBe(false);
        expect(empty?.textContent).toBe('No positive potential savings found for current recommendations.');
      });

      test('empty state mentions filter when provider chip is active and result is empty', () => {
        buildDOM();
        renderSavingsByService([], {}, 'AWS');
        const empty = document.getElementById('savings-by-service-empty');
        expect(empty?.classList.contains('hidden')).toBe(false);
        expect(empty?.textContent).toContain('AWS');
        expect(empty?.textContent).toContain('selected filter');
      });

      test('empty state mentions filter when account chip is active and result is empty', () => {
        buildDOM();
        renderSavingsByService([], {}, 'uuid-acct-1');
        const empty = document.getElementById('savings-by-service-empty');
        expect(empty?.classList.contains('hidden')).toBe(false);
        expect(empty?.textContent).toContain('uuid-acct-1');
      });

      test('empty state text updates when filter changes between renders', () => {
        buildDOM();
        // First render with data -- chart shown, empty hidden.
        renderSavingsByService([rec('ec2', 100)]);
        // Second render with filter-narrowed empty result.
        renderSavingsByService([], {}, 'AWS, uuid-acct-2');
        const empty = document.getElementById('savings-by-service-empty');
        expect(empty?.classList.contains('hidden')).toBe(false);
        expect(empty?.textContent).toContain('AWS, uuid-acct-2');
      });

      test('loadDashboard wires range bars to recommendations, not trend data', async () => {
        // The range bar chart must receive the recs from getRecommendations,
        // not from getSavingsAnalytics. Verify by providing recs but no analytics.
        buildDOM();
        const summaryEl = document.createElement('section');
        summaryEl.id = 'summary';
        const upcomingEl = document.createElement('div');
        upcomingEl.id = 'upcoming-list';
        document.body.appendChild(summaryEl);
        document.body.appendChild(upcomingEl);

        (api.getDashboardSummary as jest.Mock).mockResolvedValue({
          potential_monthly_savings: 0,
          total_recommendations: 1, active_commitments: 0, committed_monthly: 0,
          target_coverage: 80, ytd_savings: 0,
          by_service: {},
        });
        (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        // Provide a rec so the bar chart should render (not show empty state).
        (api.getRecommendations as jest.Mock).mockResolvedValue([rec('ec2', 150)]);
        // Analytics deliberately absent/empty -- bar chart should still render from recs.
        (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

        await loadDashboard();

        expect(document.getElementById('savings-by-service-chart')?.classList.contains('hidden')).toBe(false);
        expect(document.getElementById('savings-by-service-empty')?.classList.contains('hidden')).toBe(true);
      });

      // Issue #908: the merged chart must keep honoring the topbar chips.
      // loadDashboard re-runs on every chip change (via the state subscribers
      // wired in setupDashboardHandlers), so a second load with new filter
      // results must re-render the chart with the new data.
      test('re-renders the merged chart when the filter changes between loads', async () => {
        buildDOM();
        const summaryEl = document.createElement('section');
        summaryEl.id = 'summary';
        const upcomingEl = document.createElement('div');
        upcomingEl.id = 'upcoming-list';
        document.body.appendChild(summaryEl);
        document.body.appendChild(upcomingEl);

        (api.getDashboardSummary as jest.Mock).mockResolvedValue({
          potential_monthly_savings: 0, total_recommendations: 1,
          active_commitments: 0, committed_monthly: 0, target_coverage: 80,
          ytd_savings: 0, by_service: { ec2: { potential_savings: 150, current_savings: 90 } },
        });
        (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        (api.getSavingsAnalytics as jest.Mock).mockResolvedValue({ data_points: [] });

        // First load: AWS/ec2 result.
        (api.getRecommendations as jest.Mock).mockResolvedValue([rec('ec2', 150)]);
        await loadDashboard();
        const chartCtor = Chart as unknown as jest.Mock;
        const lastChartData = (): { labels: string[] } =>
          (chartCtor.mock.calls[chartCtor.mock.calls.length - 1]?.[1] as { data: { labels: string[] } }).data;
        expect(lastChartData().labels).toEqual(['ec2']);

        // Filter changes -> new recs -> second load must re-render. rds appears
        // from the new recs (total=220); ec2 still appears from byService
        // current_savings=90 even without a rec in this load. rds sorts first.
        (api.getRecommendations as jest.Mock).mockResolvedValue([rec('rds', 220)]);
        await loadDashboard();
        expect(lastChartData().labels).toContain('rds');
        expect(lastChartData().labels[0]).toBe('rds');
      });
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
