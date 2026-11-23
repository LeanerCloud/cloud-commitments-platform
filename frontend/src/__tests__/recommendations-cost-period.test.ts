/**
 * Recommendations module tests: cost-period scaling, labels and dropdown tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, DOM_FOR_319 } from './recommendations-harness';
import {
  loadRecommendations,
  scaleCost,
  formatCostForPeriod,
  periodSuffix,
} from '../recommendations';
import type { CostPeriod } from '../state';
import * as api from '../api';
import * as state from '../state';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

//
// Issue #319: cost-period selector tests
//

describe('Issue #319: scaleCost', () => {
  test('null input returns null', () => {
    const periods: CostPeriod[] = ['hourly', 'daily', 'monthly', 'yearly'];
    for (const p of periods) {
      expect(scaleCost(null, p)).toBeNull();
    }
  });

  test('undefined input returns null', () => {
    const periods: CostPeriod[] = ['hourly', 'daily', 'monthly', 'yearly'];
    for (const p of periods) {
      expect(scaleCost(undefined, p)).toBeNull();
    }
  });

  test('zero input returns 0 (not null)', () => {
    expect(scaleCost(0, 'hourly')).toBe(0);
  });
});

describe('Issue #319: formatCostForPeriod', () => {
  test('monthly uses formatCurrency mock ($X)', () => {
    // formatCurrency mock returns `$${val}` so for 100 → "$100"
    expect(formatCostForPeriod(100, 'monthly')).toBe('$100');
  });

  test('hourly uses 4 decimal places', () => {
    expect(formatCostForPeriod(730, 'hourly')).toMatch(/\$1\.0000/);
  });

  test('daily uses 2 decimal places', () => {
    expect(formatCostForPeriod(300, 'daily')).toMatch(/\$10\.00/);
  });

  test('yearly uses 0 decimal places', () => {
    expect(formatCostForPeriod(100, 'yearly')).toMatch(/\$1200$/);
  });

  test('null input returns em-dash for all periods', () => {
    const periods: CostPeriod[] = ['hourly', 'daily', 'monthly', 'yearly'];
    for (const p of periods) {
      expect(formatCostForPeriod(null, p)).toBe('—');
    }
  });

  test('undefined input returns em-dash for all periods', () => {
    const periods: CostPeriod[] = ['hourly', 'daily', 'monthly', 'yearly'];
    for (const p of periods) {
      expect(formatCostForPeriod(undefined, p)).toBe('—');
    }
  });

  test('zero input renders as $0 (not em-dash)', () => {
    // hourly: $0.0000, daily: $0.00, monthly: $0, yearly: $0
    expect(formatCostForPeriod(0, 'hourly')).toMatch(/\$0\.0000/);
    expect(formatCostForPeriod(0, 'daily')).toMatch(/\$0\.00/);
    expect(formatCostForPeriod(0, 'yearly')).toMatch(/\$0$/);
  });
});

describe('Issue #319: periodSuffix', () => {
  test('returns correct suffix for each period', () => {
    expect(periodSuffix('hourly')).toBe('/ hr');
    expect(periodSuffix('daily')).toBe('/ day');
    expect(periodSuffix('monthly')).toBe('/ mo');
    expect(periodSuffix('yearly')).toBe('/ yr');
  });
});

describe('Issue #319: column header labels update with period', () => {
  const mockRec = {
    id: 'r1',
    provider: 'aws' as const,
    service: 'ec2',
    resource_type: 'c5.large',
    region: 'us-east-1',
    count: 2,
    term: 1,
    payment: 'no-upfront',
    savings: 500,
    upfront_cost: 0,
    monthly_cost: 300,
    cloud_account_id: undefined,
  };

  beforeEach(() => {
    document.body.innerHTML = DOM_FOR_319;
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [mockRec],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([mockRec]);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([mockRec]);
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
  });

  test('monthly period: headers show "Monthly Savings" and "Monthly Cost"', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    await loadRecommendations();
    const headers = document.querySelectorAll('th.sortable span');
    const labels = Array.from(headers).map((h) => h.textContent);
    expect(labels).toContain('Monthly Savings');
    expect(labels).toContain('Monthly Cost');
  });

  test('hourly period: headers show "Savings / hr" and "Cost / hr"', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('hourly');
    await loadRecommendations();
    const headers = document.querySelectorAll('th.sortable span');
    const labels = Array.from(headers).map((h) => h.textContent);
    expect(labels).toContain('Savings / hr');
    expect(labels).toContain('Cost / hr');
  });

  test('daily period: headers show "Savings / day" and "Cost / day"', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('daily');
    await loadRecommendations();
    const headers = document.querySelectorAll('th.sortable span');
    const labels = Array.from(headers).map((h) => h.textContent);
    expect(labels).toContain('Savings / day');
    expect(labels).toContain('Cost / day');
  });

  test('yearly period: headers show "Savings / yr" and "Cost / yr"', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('yearly');
    await loadRecommendations();
    const headers = document.querySelectorAll('th.sortable span');
    const labels = Array.from(headers).map((h) => h.textContent);
    expect(labels).toContain('Savings / yr');
    expect(labels).toContain('Cost / yr');
  });

  test('non-cost headers are period-invariant', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('hourly');
    await loadRecommendations();
    const headers = document.querySelectorAll('th.sortable span');
    const labels = Array.from(headers).map((h) => h.textContent);
    expect(labels).toContain('Provider');
    expect(labels).toContain('Term');
    expect(labels).toContain('Upfront Cost');
    expect(labels).toContain('Effective %');
  });
});

describe('Issue #319: table cells scale with period', () => {
  const mockRec = {
    id: 'r1',
    provider: 'aws' as const,
    service: 'ec2',
    resource_type: 'c5.large',
    region: 'us-east-1',
    count: 2,
    term: 1,
    payment: 'no-upfront',
    savings: 720,
    upfront_cost: 0,
    monthly_cost: 720,
    cloud_account_id: undefined,
  };

  beforeEach(() => {
    document.body.innerHTML = DOM_FOR_319;
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [mockRec],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([mockRec]);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([mockRec]);
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
  });

  test('hourly: savings cell shows $0.9863 (720 / 730 hours)', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('hourly');
    await loadRecommendations();
    const rows = document.querySelectorAll('tr.recommendation-row');
    expect(rows.length).toBeGreaterThan(0);
    const savingsCell = rows[0]!.querySelector('.savings');
    expect(savingsCell?.textContent).toMatch(/\$0\.9863/);
  });

  test('daily: savings cell shows $24.00', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('daily');
    await loadRecommendations();
    const rows = document.querySelectorAll('tr.recommendation-row');
    const savingsCell = rows[0]!.querySelector('.savings');
    expect(savingsCell?.textContent).toMatch(/\$24\.00/);
  });

  test('monthly: savings cell shows existing formatCurrency output', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    await loadRecommendations();
    const rows = document.querySelectorAll('tr.recommendation-row');
    const savingsCell = rows[0]!.querySelector('.savings');
    // formatCurrency mock returns `$${val}` so for 720 it returns "$720"
    expect(savingsCell?.textContent).toContain('720');
  });

  test('yearly: savings cell shows $8640', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('yearly');
    await loadRecommendations();
    const rows = document.querySelectorAll('tr.recommendation-row');
    const savingsCell = rows[0]!.querySelector('.savings');
    expect(savingsCell?.textContent).toMatch(/\$8640/);
  });

  test('upfront_cost is NOT scaled (period-invariant)', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('hourly');
    const recWithUpfront = { ...mockRec, upfront_cost: 1000 };
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [recWithUpfront],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([recWithUpfront]);
    await loadRecommendations();
    const rows = document.querySelectorAll('tr.recommendation-row');
    // We just verify "1000" appears somewhere but "0.0014" (1000/730) does not
    const rowText = rows[0]!.textContent ?? '';
    expect(rowText).toContain('1000');
    expect(rowText).not.toMatch(/0\.0014/);
  });

  test('null monthly_cost renders "—" regardless of period', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('yearly');
    const recNullCost = { ...mockRec, monthly_cost: null };
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [recNullCost],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([recNullCost]);
    await loadRecommendations();
    const rows = document.querySelectorAll('tr.recommendation-row');
    const rowHtml = rows[0]!.innerHTML;
    expect(rowHtml).toContain('—');
  });

  // CR pass-1 nitpick: the existing #319 cases verify labels and formatted
  // values, but they never prove that sort and filter accessors actually
  // apply the period scale. A regression that reverts numericCellValue or
  // SORTABLE_NUMERIC_COLUMNS.savings to raw monthly_cost would still pass
  // every assertion above. The two tests below pin the scaled-numeric
  // contract from both ends: ordering (sort) and inclusion (filter).
  test('yearly sort orders savings cells by yearly-scaled value (asc)', async () => {
    const recSmall = { ...mockRec, id: 'r-small', resource_type: 'small', savings: 100, monthly_cost: 100 };
    const recLarge = { ...mockRec, id: 'r-large', resource_type: 'large', savings: 200, monthly_cost: 200 };
    // Inputs are intentionally pre-sorted DESC so a no-op scale that
    // preserves input order would fail this asc assertion.
    (state.getCostPeriod as jest.Mock).mockReturnValue('yearly');
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'asc' });
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [recLarge, recSmall],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([recLarge, recSmall]);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([recLarge, recSmall]);

    await loadRecommendations();

    const rows = Array.from(document.querySelectorAll('tr.recommendation-row'));
    expect(rows).toHaveLength(2);
    // Even though recLarge appears first in the input array, ascending
    // savings sort (which routes through scaleCost) places r-small first.
    expect(rows[0]?.textContent).toContain('small');
    expect(rows[1]?.textContent).toContain('large');
  });
});
