/**
 * Dashboard module tests: renderSavingsByService service stats tests.
 * Split from dashboard.test.ts to stay under the 500-line ceiling; the
 * module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, mockConfirmDialog, mockGroupRecsByCell, mockPageLevelRange, mockFormatSavingsRange, resetDashboardState } from './dashboard-harness';
import * as api from '../api';

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

  // Issue #765: per-service savings-range bar chart.
  describe('renderSavingsByService (issue #769)', () => {
    // Import the public helpers from dashboard. The module is already
    // loaded above via the jest.mock chain, so we can import directly.
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    const { computeServiceStats, computeServiceStatsFromRecs } = require('../dashboard') as {
      computeServiceStats: (dataPoints: unknown[]) => Map<string, { min: number; max: number; sum: number; count: number; samples: number[] }>;
      computeServiceStatsFromRecs: (recs: unknown[]) => Map<string, { min: number; max: number; sum: number; count: number; samples: number[]; minLabel?: string; maxLabel?: string; unknownCells?: number }>;
    };

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

    // computeServiceStatsFromRecs unit tests.
    describe('computeServiceStatsFromRecs', () => {
      test('returns empty map for empty recommendations', () => {
        expect(computeServiceStatsFromRecs([]).size).toBe(0);
      });

      test('single rec produces min === max (zero upside)', () => {
        const result = computeServiceStatsFromRecs([rec('ec2', 100)]);
        const ec2 = result.get('ec2');
        expect(ec2?.min).toBe(100);
        expect(ec2?.max).toBe(100);
        expect(ec2?.count).toBe(1);
      });

      test('two recs for same service: min is lower value, max is higher value', () => {
        const result = computeServiceStatsFromRecs([
          rec('ec2', 200, 1, 'no_upfront'),
          rec('ec2', 500, 3, 'all_upfront'),
        ]);
        const ec2 = result.get('ec2');
        expect(ec2?.min).toBe(200);
        expect(ec2?.max).toBe(500);
        expect(ec2?.count).toBe(2);
      });

      test('tracks minLabel and maxLabel from term/payment option', () => {
        const result = computeServiceStatsFromRecs([
          rec('ec2', 200, 1, 'no_upfront'),
          rec('ec2', 500, 3, 'all_upfront'),
        ]);
        const ec2 = result.get('ec2');
        expect(ec2?.minLabel).toBe('1yr no_upfront');
        expect(ec2?.maxLabel).toBe('3yr all_upfront');
      });

      test('uses "unspecified" label when payment is undefined or empty string', () => {
        const recNoPayment = { ...rec('ec2', 100) as Record<string, unknown> };
        delete recNoPayment['payment'];
        const recEmptyPayment = { ...rec('ec2', 200) as Record<string, unknown>, payment: '' };
        const result = computeServiceStatsFromRecs([recNoPayment, recEmptyPayment] as unknown as Parameters<typeof computeServiceStatsFromRecs>[0]);
        const ec2 = result.get('ec2');
        expect(ec2?.minLabel).toContain('unspecified');
        expect(ec2?.maxLabel).toContain('unspecified');
        expect(ec2?.minLabel).not.toContain('undefined');
        expect(ec2?.maxLabel).not.toContain('undefined');
      });

      test('accumulates stats for multiple services independently', () => {
        const result = computeServiceStatsFromRecs([
          rec('ec2', 100, 1, 'no_upfront'),
          rec('ec2', 400, 3, 'all_upfront'),
          rec('rds', 50, 1, 'no_upfront'),
          rec('rds', 80, 3, 'all_upfront'),
        ]);
        expect(result.size).toBe(2);
        expect(result.get('ec2')?.min).toBe(100);
        expect(result.get('ec2')?.max).toBe(400);
        expect(result.get('rds')?.min).toBe(50);
        expect(result.get('rds')?.max).toBe(80);
      });

      test('stores raw sample values', () => {
        const result = computeServiceStatsFromRecs([
          rec('ec2', 100),
          rec('ec2', 300),
          rec('ec2', 200),
        ]);
        const ec2 = result.get('ec2');
        expect(ec2?.samples).toHaveLength(3);
        expect(ec2?.samples).toContain(100);
        expect(ec2?.samples).toContain(200);
        expect(ec2?.samples).toContain(300);
      });
      describe('per-cell aggregation (#1770)', () => {
        // The suite mocks groupRecsByCell as one-cell-for-all; these cases need real cells.
        beforeEach(() => {
          // eslint-disable-next-line @typescript-eslint/no-var-requires
          const actual = jest.requireActual('../recommendations') as { groupRecsByCell: (r: unknown[]) => Map<string, unknown[]> };
          mockGroupRecsByCell.mockImplementation((recs: unknown[]) => actual.groupRecsByCell(recs));
        });
        const cell = (svc: string, region: string, savings: unknown, term = 1, payment = 'no_upfront'): unknown =>
          ({ ...(rec(svc, 0, term, payment) as Record<string, unknown>), region, savings });

        test('two cells with two variants each: min/max are per-cell sums', () => {
          const ec2 = computeServiceStatsFromRecs([
            cell('ec2', 'us-east-1', 100, 1, 'no_upfront'),
            cell('ec2', 'us-east-1', 150, 3, 'all_upfront'),
            cell('ec2', 'eu-west-1', 400, 1, 'no_upfront'),
            cell('ec2', 'eu-west-1', 500, 3, 'all_upfront'),
          ]).get('ec2');
          // Per-row min/max would give 100 / 500.
          expect(ec2?.min).toBe(500);
          expect(ec2?.max).toBe(650);
          expect(ec2?.count).toBe(4);
        });

        test('labels name the variant when every cell picks the same one', () => {
          const ec2 = computeServiceStatsFromRecs([
            cell('ec2', 'us-east-1', 100, 1, 'no_upfront'),
            cell('ec2', 'us-east-1', 150, 3, 'all_upfront'),
            cell('ec2', 'eu-west-1', 400, 1, 'no_upfront'),
            cell('ec2', 'eu-west-1', 500, 3, 'all_upfront'),
          ]).get('ec2');
          expect(ec2?.minLabel).toBe('1yr no_upfront');
          expect(ec2?.maxLabel).toBe('3yr all_upfront');
        });

        test('labels are neutral when cells pick different variants', () => {
          const ec2 = computeServiceStatsFromRecs([
            cell('ec2', 'us-east-1', 100, 1, 'no_upfront'),
            cell('ec2', 'us-east-1', 150, 3, 'all_upfront'),
            cell('ec2', 'eu-west-1', 400, 3, 'partial_upfront'),
            cell('ec2', 'eu-west-1', 500, 1, 'all_upfront'),
          ]).get('ec2');
          expect(ec2?.minLabel).toBe('varies by account/region');
          expect(ec2?.maxLabel).toBe('varies by account/region');
        });

        test('single-cell service is unchanged', () => {
          const ec2 = computeServiceStatsFromRecs([
            cell('ec2', 'us-east-1', 200, 1, 'no_upfront'),
            cell('ec2', 'us-east-1', 500, 3, 'all_upfront'),
          ]).get('ec2');
          expect([ec2?.min, ec2?.max]).toEqual([200, 500]);
          expect([ec2?.minLabel, ec2?.maxLabel]).toEqual(['1yr no_upfront', '3yr all_upfront']);
        });

        test('unknown savings are skipped, never counted as 0', () => {
          const ec2 = computeServiceStatsFromRecs([
            cell('ec2', 'us-east-1', 300, 1, 'no_upfront'),
            cell('ec2', 'us-east-1', undefined, 3, 'all_upfront'),
            cell('ec2', 'eu-west-1', null),
            cell('ec2', 'ap-south-1', NaN),
          ]).get('ec2');
          expect([ec2?.min, ec2?.max]).toEqual([300, 300]);
          expect(ec2?.unknownCells).toBe(2);
        });

        test('service whose cells are all unknown has no entry', () => {
          expect(computeServiceStatsFromRecs([cell('rds', 'us-east-1', undefined)]).has('rds')).toBe(false);
        });
      });
    });
  });
});
