/**
 * Recommendations module tests: cost-period persistence, column visibility and formatCapacity tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import {
  loadRecommendations,
  loadColumnVisibility,
  saveColumnVisibility,
  resetColumnVisibilityState,
  TOGGLEABLE_COLUMNS,
  COLUMN_DEFS,
  formatCapacity,
} from '../recommendations';
import type { LocalRecommendation } from '../types';
import { localStorageMock } from './setup';
import * as api from '../api';
import * as state from '../state';

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

//
// Issue #494: deterministic group sort on multi-variant cells.
//
// After PR #195's per-(term, payment) fan-out, every cell has BOTH 1yr and 3yr
// variants. The previous `Math.max(...va.map(numericKey))` cell-score collapses
// to the same value across every cell for Term / Payment / Monthly Cost /
// Effective % / (and many cells for Upfront), producing apparently-random row
// order. These regression tests assert deterministic ordering matching what
// the user sees in the rendered summary row.
//
// PR #491 (closes #480) fixed the default-direction inversion; this PR fixes
// the upstream "every cell ties" symptom.
//
describe('Issue #494: deterministic group sort on multi-variant cells', () => {
  /** Build the minimum DOM loadRecommendations needs, using createElement so
   * no innerHTML assignment is required (the rest of the file uses innerHTML;
   * here we use the safer DOM API to match the test boilerplate pattern in
   * `Bundle B: column header filter triggers`). */
  function buildTestDOM(): void {
    document.body.replaceChildren();
    const recsTab = document.createElement('div');
    recsTab.id = 'opportunities-tab';
    recsTab.className = 'tab-content active';
    const summary = document.createElement('div');
    summary.id = 'recommendations-summary';
    const list = document.createElement('div');
    list.id = 'recommendations-list';
    recsTab.appendChild(summary);
    recsTab.appendChild(list);
    document.body.appendChild(recsTab);

    const purchaseModal = document.createElement('div');
    purchaseModal.id = 'purchase-modal';
    purchaseModal.className = 'hidden';
    const purchaseDetails = document.createElement('div');
    purchaseDetails.id = 'purchase-details';
    purchaseModal.appendChild(purchaseDetails);
    document.body.appendChild(purchaseModal);
  }

  /**
   * Build a single multi-variant cell (2 variants: term=1 + term=3) with the
   * provided per-variant payments / upfront / monthly_cost. Cell identity is
   * controlled by `resource_type` so callers can build N distinct cells in one
   * fixture.
   */
  function multiVariantCell(opts: {
    resourceType: string;
    payment1y: string;
    payment3y: string;
    upfront1y: number;
    upfront3y: number;
    monthly1y?: number | null;
    monthly3y?: number | null;
    onDemand?: number | null;
    savings1y?: number;
    savings3y?: number;
  }): LocalRecommendation[] {
    const slug = opts.resourceType.replace(/\W/g, '');
    return [
      {
        id: `${slug}-1y`,
        provider: 'aws',
        cloud_account_id: 'a1',
        service: 'ec2',
        resource_type: opts.resourceType,
        region: 'us-east-1',
        count: 1,
        term: 1,
        payment: opts.payment1y,
        savings: opts.savings1y ?? 100,
        upfront_cost: opts.upfront1y,
        monthly_cost: opts.monthly1y === undefined ? 50 : opts.monthly1y,
        on_demand_cost: opts.onDemand === undefined ? 200 : opts.onDemand,
      } as unknown as LocalRecommendation,
      {
        id: `${slug}-3y`,
        provider: 'aws',
        cloud_account_id: 'a1',
        service: 'ec2',
        resource_type: opts.resourceType,
        region: 'us-east-1',
        count: 1,
        term: 3,
        payment: opts.payment3y,
        savings: opts.savings3y ?? 300,
        upfront_cost: opts.upfront3y,
        monthly_cost: opts.monthly3y === undefined ? 40 : opts.monthly3y,
        on_demand_cost: opts.onDemand === undefined ? 200 : opts.onDemand,
      } as unknown as LocalRecommendation,
    ];
  }

  /**
   * Read the rendered cell order from the DOM. Multi-variant cells render as
   * `tr.rec-cell-summary-row[data-cell-key]`; the test fixtures here use only
   * multi-variant cells (every cell has 2 variants) so this single selector
   * captures the full ordering.
   */
  function renderedCellOrder(): string[] {
    return Array.from(
      document.querySelectorAll<HTMLTableRowElement>('tr.rec-cell-summary-row'),
    ).map((tr) => tr.getAttribute('data-cell-key') ?? '');
  }

  /**
   * Find a key fragment in the rendered order and assert it is actually
   * present. Returns the index. Prevents false-positive `<` comparisons that
   * would silently pass when `findIndex` returns `-1` for both sides (per CR
   * review on the initial commit).
   */
  function indexOrFail(order: string[], keyFragment: string): number {
    const idx = order.findIndex((k) => k.includes(keyFragment));
    expect(idx).toBeGreaterThanOrEqual(0);
    return idx;
  }

  /** Set up the DOM + standard mocks; caller provides the rec list + sort. */
  function setupTestFixture(
    recs: LocalRecommendation[],
    sort: { column: string; direction: 'asc' | 'desc' },
  ): void {
    buildTestDOM();
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: recs,
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    (state.getRecommendationsSort as jest.Mock).mockReturnValue(sort);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
  }

  //
  // 4.9 - Term
  //
  test('Term asc: orders cells by summary.termMin (1yr-grouped before 3yr-grouped)', async () => {
    const cellMixed = multiVariantCell({
      resourceType: 'aaa-mixed', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0,
    });
    // "1yr-only" cell: both variants term=1, different payments so they
    // produce two distinct rec rows under the same cellKey.
    const cell1yOnly: LocalRecommendation[] = [
      { id: 'bbb-1y-nu', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'bbb-1y-only', region: 'us-east-1', count: 1, term: 1,
        payment: 'no-upfront', savings: 100, upfront_cost: 0, monthly_cost: 50 } as unknown as LocalRecommendation,
      { id: 'bbb-1y-au', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'bbb-1y-only', region: 'us-east-1', count: 1, term: 1,
        payment: 'all-upfront', savings: 110, upfront_cost: 800, monthly_cost: 0 } as unknown as LocalRecommendation,
    ];
    const cell3yOnly: LocalRecommendation[] = [
      { id: 'ccc-3y-nu', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'ccc-3y-only', region: 'us-east-1', count: 1, term: 3,
        payment: 'no-upfront', savings: 300, upfront_cost: 0, monthly_cost: 40 } as unknown as LocalRecommendation,
      { id: 'ccc-3y-au', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'ccc-3y-only', region: 'us-east-1', count: 1, term: 3,
        payment: 'all-upfront', savings: 320, upfront_cost: 2400, monthly_cost: 0 } as unknown as LocalRecommendation,
    ];
    // Intentionally wrong insertion order so a no-op sort would fail.
    const recs = [...cell3yOnly, ...cellMixed, ...cell1yOnly];

    setupTestFixture(recs, { column: 'term', direction: 'asc' });
    await loadRecommendations();

    const order = renderedCellOrder();
    // Expected: 1yr-only first (termMin=1, termMax=1), mixed second (termMin=1,
    // termMax=3), 3yr-only last (termMin=3). The encoded score termMin*100 +
    // termMax distinguishes the 1yr-only and mixed cells via termMax.
    expect(indexOrFail(order, 'bbb-1y-only'))
      .toBeLessThan(indexOrFail(order, 'aaa-mixed'));
    expect(indexOrFail(order, 'aaa-mixed'))
      .toBeLessThan(indexOrFail(order, 'ccc-3y-only'));
  });
});
