/**
 * Dashboard module tests: renderSavingsByService DOM, text alternative and layer colour tests.
 * Split from dashboard.test.ts to stay under the 500-line ceiling; the
 * module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, mockConfirmDialog, mockGroupRecsByCell, mockPageLevelRange, mockFormatSavingsRange } from './dashboard-harness';
import { Chart } from 'chart.js';
import * as api from '../api';

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
      // #747: re-rendering with new data updates the chart in place.
      test('re-rendering updates the existing chart instead of rebuilding it (#747)', () => {
        buildDOM();
        const update = jest.fn();
        const destroy = jest.fn();
        (Chart as unknown as jest.Mock).mockImplementationOnce((canvas: HTMLCanvasElement, config: { type: string }) => ({ canvas, config, update, destroy }));
        renderSavingsByService([rec('ec2', 100)]);
        renderSavingsByService([rec('ec2', 100), rec('rds', 50)]);

        expect(Chart).toHaveBeenCalledTimes(1);
        expect(destroy).not.toHaveBeenCalled();
        expect(update).toHaveBeenCalledWith('none');

        // The empty state still tears the chart down.
        renderSavingsByService([]);
        expect(destroy).toHaveBeenCalledTimes(1);
      });

      // #744: the role="img" canvas gets a value summary and a table.
      describe('text alternative (#744)', () => {
        const tableRows = (): string[][] =>
          Array.from(document.querySelectorAll('#savings-by-service-table tbody tr'))
            .map((tr) => Array.from((tr as HTMLTableRowElement).cells).map((c) => c.textContent ?? ''));

        test('summarizes the plotted values in the canvas aria-label', () => {
          buildDOM();
          renderSavingsByService(
            [rec('ec2', 100), rec('ec2', 300, 3), rec('rds', 50)],
            { ec2: { potential_savings: 300, current_savings: 40 } },
          );
          const label = document.getElementById('savings-by-service-chart')?.getAttribute('aria-label') ?? '';
          // ec2: 40 current + 60 lowest option + 200 upside = 300; rds: 50. Total 350.
          expect(label).toContain('2 services');
          // #1649: the text alternative uses the same names as the axis.
          expect(label).toContain('Largest: EC2 at $300');
          // #1686: the bar total includes committed savings and each service's
          // highest option, so say so instead of a bare "in total" that reads
          // as a third "potential savings" figure next to the KPI range.
          expect(label).toContain('The bars add up to $350');
          expect(label).toContain('not the Potential Monthly Savings range');
        });

        test('chart note says the range is unscaled by the coverage cap (#1946)', () => {
          buildDOM();
          const note = document.createElement('p');
          note.id = 'savings-by-service-note';
          document.getElementById('savings-by-service-section')?.appendChild(note);
          renderSavingsByService([rec('ec2', 100)], {});
          expect(note.textContent).toContain("Unscaled by the coverage cap: sums each cell's known-savings variants.");
        });

        test('lists every layer value per service in a "View as table" disclosure', () => {
          buildDOM();
          renderSavingsByService(
            [rec('ec2', 100), rec('ec2', 300, 3), rec('rds', 50)],
            { ec2: { potential_savings: 300, current_savings: 40 } },
          );
          expect(document.querySelector('#savings-by-service-table summary')?.textContent).toBe('View as table');
          const headers = Array.from(document.querySelectorAll('#savings-by-service-table thead th')).map((th) => th.textContent);
          expect(headers).toEqual(['Service', 'Current / Committed', 'Lowest option', 'Upside', 'Total']);
          // #1649: display names, matching the chart axis.
          expect(tableRows()).toEqual([
            ['EC2', '$40', '$60', '$200', '$300'],
            ['RDS', '$0', '$50', '$0', '$50'],
          ]);
        });

        test('the table includes services beyond the bars drawn', () => {
          buildDOM();
          const many = Array.from({ length: 12 }, (_, i) => rec(`svc-${i}`, 100 + i));
          renderSavingsByService(many);
          expect(tableRows()).toHaveLength(12);
        });

        test('the table is removed when the chart switches to its empty state', () => {
          buildDOM();
          renderSavingsByService([rec('ec2', 100)]);
          expect(document.getElementById('savings-by-service-table')).not.toBeNull();
          renderSavingsByService([]);
          expect(document.getElementById('savings-by-service-table')).toBeNull();
        });

        test('stacked layers are separated by a 2px gap, not only by shade', () => {
          buildDOM();
          renderSavingsByService([rec('ec2', 100), rec('ec2', 300, 3)], { ec2: { potential_savings: 300, current_savings: 40 } });
          const calls = (Chart as unknown as jest.Mock).mock.calls;
          // The bars are horizontal, so each segment's gap is on its right (end) edge.
          const datasets = (calls[calls.length - 1]?.[1] as { data: { datasets: { borderWidth: { right: number }; borderColor: string }[] } }).data.datasets;
          for (const d of datasets) {
            expect(d.borderWidth.right).toBe(2);
            expect(d.borderColor).toBe('#ffffff');
          }
        });

        test('the segment gap follows a themed --cudly-surface (#699)', () => {
          buildDOM();
          document.documentElement.style.setProperty('--cudly-surface', '#1f2937');
          try {
            renderSavingsByService([rec('ec2', 100), rec('ec2', 300, 3)], { ec2: { potential_savings: 300, current_savings: 40 } });
            const calls = (Chart as unknown as jest.Mock).mock.calls;
            const datasets = (calls[calls.length - 1]?.[1] as { data: { datasets: { borderColor: string }[] } }).data.datasets;
            for (const d of datasets) expect(d.borderColor).toBe('#1f2937');
          } finally {
            document.documentElement.style.removeProperty('--cudly-surface');
          }
        });
      });

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
    });
  });
});
