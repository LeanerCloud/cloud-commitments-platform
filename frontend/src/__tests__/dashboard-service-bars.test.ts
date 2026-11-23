/**
 * Dashboard module tests: renderSavingsByService bar, tooltip and empty state tests.
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
    const { renderSavingsByService } = require('../dashboard') as {
      renderSavingsByService: (recs: unknown[], byService?: Record<string, { potential_savings: number; current_savings: number }>, filterDesc?: string) => void;
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
    });
  });
});
