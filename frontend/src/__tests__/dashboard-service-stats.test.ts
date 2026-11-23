/**
 * Dashboard module tests: renderSavingsByService service stats tests.
 * Split from dashboard.test.ts to stay under the 500-line ceiling; the
 * module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, mockConfirmDialog, mockGroupRecsByCell, mockPageLevelRange, mockFormatSavingsRange } from './dashboard-harness';
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
    const { computeServiceStats } = require('../dashboard') as {
      computeServiceStats: (dataPoints: unknown[]) => Map<string, { min: number; max: number; sum: number; count: number; samples: number[] }>;
    };

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

    // computeServiceStats unit tests (retained: function still exported for legacy compatibility).
    describe('computeServiceStats (legacy data-points path)', () => {
      test('returns empty map for empty data points', () => {
        const result = computeServiceStats([]);
        expect(result.size).toBe(0);
      });

      test('returns empty map when all data points have no by_service', () => {
        const result = computeServiceStats([
          { timestamp: 't1', total_savings: 100, total_upfront: 0, purchase_count: 1, cumulative_savings: 100 },
        ]);
        expect(result.size).toBe(0);
      });

      test('accumulates min/max/sum/count correctly for a single service', () => {
        const points = [
          { timestamp: 't1', total_savings: 0, total_upfront: 0, purchase_count: 0, cumulative_savings: 0, by_service: { ec2: 50 } },
          { timestamp: 't2', total_savings: 0, total_upfront: 0, purchase_count: 0, cumulative_savings: 0, by_service: { ec2: 200 } },
          { timestamp: 't3', total_savings: 0, total_upfront: 0, purchase_count: 0, cumulative_savings: 0, by_service: { ec2: 100 } },
        ];
        const result = computeServiceStats(points);
        expect(result.size).toBe(1);
        const ec2 = result.get('ec2');
        expect(ec2?.min).toBe(50);
        expect(ec2?.max).toBe(200);
        expect(ec2?.sum).toBe(350);
        expect(ec2?.count).toBe(3);
      });

      test('accumulates stats for multiple services independently', () => {
        const points = [
          { timestamp: 't1', total_savings: 0, total_upfront: 0, purchase_count: 0, cumulative_savings: 0, by_service: { ec2: 100, rds: 50 } },
          { timestamp: 't2', total_savings: 0, total_upfront: 0, purchase_count: 0, cumulative_savings: 0, by_service: { ec2: 300, rds: 80 } },
        ];
        const result = computeServiceStats(points);
        expect(result.size).toBe(2);
        expect(result.get('ec2')?.min).toBe(100);
        expect(result.get('ec2')?.max).toBe(300);
        expect(result.get('rds')?.min).toBe(50);
        expect(result.get('rds')?.max).toBe(80);
      });

      test('skips data points with missing by_service (omitempty)', () => {
        const points = [
          { timestamp: 't1', total_savings: 0, total_upfront: 0, purchase_count: 0, cumulative_savings: 0 },
          { timestamp: 't2', total_savings: 0, total_upfront: 0, purchase_count: 0, cumulative_savings: 0, by_service: { ec2: 200 } },
        ];
        const result = computeServiceStats(points);
        expect(result.get('ec2')?.count).toBe(1);
      });

      test('stores raw sample values for median computation', () => {
        const points = [
          { timestamp: 't1', total_savings: 0, total_upfront: 0, purchase_count: 0, cumulative_savings: 0, by_service: { ec2: 50 } },
          { timestamp: 't2', total_savings: 0, total_upfront: 0, purchase_count: 0, cumulative_savings: 0, by_service: { ec2: 200 } },
          { timestamp: 't3', total_savings: 0, total_upfront: 0, purchase_count: 0, cumulative_savings: 0, by_service: { ec2: 100 } },
        ];
        const result = computeServiceStats(points);
        const ec2 = result.get('ec2');
        expect(ec2?.samples).toHaveLength(3);
        expect(ec2?.samples).toContain(50);
        expect(ec2?.samples).toContain(100);
        expect(ec2?.samples).toContain(200);
      });
    });
  });
});
