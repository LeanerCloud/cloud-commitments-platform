/**
 * Recommendations module tests: cost-period scaling, labels and dropdown tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import { scaleCost, formatCostForPeriod, periodSuffix } from '../recommendations';
import type { CostPeriod } from '../state';

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
