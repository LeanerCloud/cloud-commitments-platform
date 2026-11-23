/**
 * Recommendations module tests: cost-period persistence, column visibility and formatCapacity tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import {
  loadColumnVisibility,
  saveColumnVisibility,
  resetColumnVisibilityState,
  TOGGLEABLE_COLUMNS,
  COLUMN_DEFS,
  formatCapacity,
} from '../recommendations';
import { localStorageMock } from './setup';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

describe('Issue #319: localStorage persistence (state.ts getCostPeriod / setCostPeriod)', () => {
  // These tests exercise the actual state module (not the mock).
  // We import directly from the source rather than the mocked version.
  // The global setup.ts replaces window.localStorage with a noop mock
  // (getItem→null, setItem→undefined), so we install a Map-backed shim
  // here to actually test persistence semantics, then reset it.
  let getCostPeriodFn: () => string;
  let setCostPeriodFn: (period: string) => void;

  beforeEach(() => {
    const store = new Map<string, string>();
    (localStorage.getItem as jest.Mock).mockImplementation(
      (key: string) => (store.has(key) ? store.get(key)! : null)
    );
    (localStorage.setItem as jest.Mock).mockImplementation(
      (key: string, value: string) => { store.set(key, value); }
    );
    (localStorage.removeItem as jest.Mock).mockImplementation(
      (key: string) => { store.delete(key); }
    );
    (localStorage.clear as jest.Mock).mockImplementation(() => { store.clear(); });

    // Use requireActual to bypass the jest.mock('../state') and get the real
    // module. This lets us test the actual localStorage persistence logic.
    const stateModule = jest.requireActual('../state') as typeof import('../state');
    getCostPeriodFn = stateModule.getCostPeriod as () => string;
    setCostPeriodFn = stateModule.setCostPeriod as (period: string) => void;
    // Reset in-memory costPeriodMemory to the module default before each
    // test, otherwise it survives the localStorage clear and the
    // "invalid value falls back to default" test sees stale 'hourly' from
    // the prior "setCostPeriod persists" test.
    setCostPeriodFn('monthly');
    store.clear();
  });

  afterEach(() => {
    (localStorage.getItem as jest.Mock).mockReset().mockImplementation(() => null);
    (localStorage.setItem as jest.Mock).mockReset().mockImplementation(() => undefined);
    (localStorage.removeItem as jest.Mock).mockReset().mockImplementation(() => undefined);
    (localStorage.clear as jest.Mock).mockReset().mockImplementation(() => undefined);
  });

  test('default period is monthly when localStorage is empty', () => {
    expect(getCostPeriodFn()).toBe('monthly');
  });

  test('setCostPeriod persists to localStorage', () => {
    setCostPeriodFn('hourly');
    expect(localStorage.getItem('cudly.recs.costPeriod')).toBe('hourly');
  });

  test('getCostPeriod reads back the persisted value', () => {
    localStorage.setItem('cudly.recs.costPeriod', 'hourly');
    expect(getCostPeriodFn()).toBe('hourly');
  });

  test('invalid value in localStorage falls back to static default, not prior in-memory state', () => {
    // CR pass-1 nitpick: seed a non-default value first so this test fails if
    // getCostPeriod() incorrectly leaks a stale in-memory cache instead of
    // re-reading + validating localStorage on each call. Without the seed, a
    // broken implementation that only reads localStorage on first call would
    // still pass.
    setCostPeriodFn('hourly');
    expect(getCostPeriodFn()).toBe('hourly');
    localStorage.setItem('cudly.recs.costPeriod', 'invalid-value');
    expect(getCostPeriodFn()).toBe('monthly');
  });
});

// ============================================================================
// Issue #318: Column visibility — localStorage persistence + toggle layer
// ============================================================================
describe('Column visibility (issue #318)', () => {
  beforeEach(() => {
    resetColumnVisibilityState();
    // localStorageMock is cleared by jest.clearAllMocks() in the global beforeEach
    // (setup.ts). Each test below configures the mock as needed.
  });

  // --- loadColumnVisibility ---

  describe('loadColumnVisibility', () => {
    // Nothing saved (or an unreadable entry): the default hidden set, which
    // keeps Monthly Savings in view at a 1440px window.
    const DEFAULTS = ['capacity', 'effective_savings_pct', 'monthly_cost', 'on_demand_monthly', 'usage_history'];

    test('returns the default hidden columns when localStorage key is absent', () => {
      // Default mock: getItem returns null
      const result = loadColumnVisibility();
      expect([...result].sort()).toEqual(DEFAULTS);
    });

    test('returns the default hidden columns on JSON parse error', () => {
      localStorageMock.getItem.mockReturnValue('{not valid json}');
      const result = loadColumnVisibility();
      expect([...result].sort()).toEqual(DEFAULTS);
    });

    test('returns the default hidden columns when schemaVersion is wrong', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 99, hidden: ['count'] }),
      );
      const result = loadColumnVisibility();
      expect([...result].sort()).toEqual(DEFAULTS);
    });

    test('returns the default hidden columns when hidden is not an array', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 1, hidden: 'count' }),
      );
      const result = loadColumnVisibility();
      expect([...result].sort()).toEqual(DEFAULTS);
    });

    test('a saved "show every column" choice beats the defaults', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 1, hidden: [] }),
      );
      expect(loadColumnVisibility().size).toBe(0);
    });

    test('returns the correct hidden set for valid JSON', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 1, hidden: ['count', 'term'] }),
      );
      const result = loadColumnVisibility();
      expect(result.has('count')).toBe(true);
      expect(result.has('term')).toBe(true);
      expect(result.size).toBe(2);
    });

    test('silently drops unknown column keys (forward-compatibility)', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 1, hidden: ['count', 'future_column_not_yet_added'] }),
      );
      const result = loadColumnVisibility();
      // 'count' is a known toggleable column; 'future_column_not_yet_added' is not
      expect(result.has('count')).toBe(true);
      expect(result.size).toBe(1);
    });

    test('silently drops fixed (non-toggleable) column keys', () => {
      // provider/account/service/resource_type are fixed; should be ignored even if stored
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 1, hidden: ['provider', 'account', 'count'] }),
      );
      const result = loadColumnVisibility();
      expect(result.has('provider')).toBe(false);
      expect(result.has('account')).toBe(false);
      expect(result.has('count')).toBe(true);
      expect(result.size).toBe(1);
    });
  });

  // --- saveColumnVisibility ---

  describe('saveColumnVisibility', () => {
    test('writes correct JSON shape to localStorage via setItem', () => {
      const hidden = new Set<import('../state').RecommendationsColumnId>(['count', 'term']);
      saveColumnVisibility(hidden);
      expect(localStorageMock.setItem).toHaveBeenCalledWith(
        'cudly.recs.columnVisibility.v1',
        expect.stringContaining('"schemaVersion":1'),
      );
      const callArg = localStorageMock.setItem.mock.calls[0]?.[1] as string;
      const parsed = JSON.parse(callArg);
      expect(parsed.schemaVersion).toBe(1);
      expect(parsed.hidden).toContain('count');
      expect(parsed.hidden).toContain('term');
      expect(parsed.hidden.length).toBe(2);
    });

    test('writes empty array for no hidden columns', () => {
      saveColumnVisibility(new Set());
      const callArg = localStorageMock.setItem.mock.calls[0]?.[1] as string;
      const parsed = JSON.parse(callArg);
      expect(parsed.hidden).toEqual([]);
    });
  });

  // --- TOGGLEABLE_COLUMNS and COLUMN_DEFS ---

  describe('COLUMN_DEFS and TOGGLEABLE_COLUMNS', () => {
    test('COLUMN_DEFS contains all 15 column ids (13 data + capacity + usage_history sparkline)', () => {
      const keys = COLUMN_DEFS.map((c) => c.key);
      expect(keys).toContain('provider');
      expect(keys).toContain('account');
      expect(keys).toContain('service');
      expect(keys).toContain('resource_type');
      expect(keys).toContain('capacity');
      expect(keys).toContain('region');
      expect(keys).toContain('count');
      expect(keys).toContain('term');
      expect(keys).toContain('payment');
      expect(keys).toContain('savings');
      expect(keys).toContain('upfront_cost');
      expect(keys).toContain('monthly_cost');
      expect(keys).toContain('on_demand_monthly');
      expect(keys).toContain('effective_savings_pct');
      // issue #239: usage_history sparkline column added
      expect(keys).toContain('usage_history');
      expect(COLUMN_DEFS.length).toBe(15);
    });

    test('capacity column appears immediately after resource_type', () => {
      const keys = COLUMN_DEFS.map((c) => c.key);
      expect(keys.indexOf('capacity')).toBe(keys.indexOf('resource_type') + 1);
    });

    test('TOGGLEABLE_COLUMNS excludes fixed identity columns', () => {
      const keys = TOGGLEABLE_COLUMNS.map((c) => c.key);
      expect(keys).not.toContain('provider');
      expect(keys).not.toContain('account');
      expect(keys).not.toContain('service');
      expect(keys).not.toContain('resource_type');
      // All other 11 columns (9 original + capacity + usage_history) should be toggleable.
      expect(keys).toContain('capacity');
      expect(keys).toContain('region');
      expect(keys).toContain('count');
      expect(keys).toContain('term');
      expect(keys).toContain('payment');
      expect(keys).toContain('savings');
      expect(keys).toContain('upfront_cost');
      expect(keys).toContain('monthly_cost');
      expect(keys).toContain('on_demand_monthly');
      expect(keys).toContain('effective_savings_pct');
      expect(keys).toContain('usage_history');
      expect(keys.length).toBe(11);
    });
  });
});

//
// formatCapacity (closes #219)
//
describe('formatCapacity', () => {
  test('returns formatted string when both vcpu and memory_gb are populated', () => {
    expect(formatCapacity(8, 32)).toBe('8 vCPU / 32 GB');
  });

  test('handles fractional memory without trailing zeros', () => {
    expect(formatCapacity(2, 0.5)).toBe('2 vCPU / 0.5 GB');
  });

  test('returns null when vcpu is null', () => {
    expect(formatCapacity(null, 32)).toBeNull();
  });

  test('returns null when memory_gb is null', () => {
    expect(formatCapacity(8, null)).toBeNull();
  });

  test('returns null when vcpu is undefined', () => {
    expect(formatCapacity(undefined, 32)).toBeNull();
  });

  test('returns null when memory_gb is undefined', () => {
    expect(formatCapacity(8, undefined)).toBeNull();
  });

  test('returns null when vcpu is 0 (unknown)', () => {
    expect(formatCapacity(0, 32)).toBeNull();
  });

  test('returns null when memory_gb is 0 (unknown)', () => {
    expect(formatCapacity(8, 0)).toBeNull();
  });
});
