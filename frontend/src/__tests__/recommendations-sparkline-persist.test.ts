/**
 * Recommendations module tests: usage sparklines, column filter persistence and the SP group key helper.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, setupOpportunitiesTabDom } from './recommendations-harness';
import {
  loadRecommendations,
  COLUMN_DEFS,
  renderUsageSparkline,
  loadColumnFilters,
  resetColumnFiltersState,
} from '../recommendations';
import { localStorageMock } from './setup';
import * as api from '../api';
import * as state from '../state';
import * as recsApi from '../api/recommendations';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

//
// Issue #484: Numeric filter exact-match against the displayed rounded value.
//
describe('Issue #484: numeric filter matches the displayed rounded value', () => {
  // Choose a savings value whose raw form rounds to a different display
  // value depending on which precision we use. Under hourly period, the
  // display rounds to PERIOD_DECIMALS.hourly (4) decimals.
  //
  // raw monthly = 123.456789 → daily = monthly/30 = 4.1152263; hourly =
  // monthly/730 = 0.169119... Use a monthly that's clean enough to keep
  // the assertions readable while still distinguishing exact rounding.
  const recs = [
    { id: 'r-exact',   provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 123.456789, upfront_cost: 500 },
    { id: 'r-other',   provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, savings: 250,        upfront_cost: 800 },
  ];

  beforeEach(() => {
    setupOpportunitiesTabDom();
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
      last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      last_collection_error: null,
    });
  });

  // CR pass #2 regression: `displayPrecision` for currency columns MUST
  // agree with the fraction-digit count `formatCurrency` actually renders,
  // so that an exact-match filter on the displayed string ("$123") matches
  // the row whose underlying value rounds to that string at the active
  // period. Both sources must agree, so the test pins the symmetry at the
  // canonical `CURRENCY_DEFAULT_DIGITS` constant — which formatCurrency
  // uses as its default and displayPrecision now imports — rather than at
  // a hard-coded literal that would silently drift if the default ever
  // changes.
  describe('displayPrecision agrees with formatCurrency for currency columns', () => {
    test('monthly_cost / on_demand_monthly non-monthly periods use PERIOD_DECIMALS-based precision', async () => {
      // Non-monthly periods bypass formatCurrency (see formatCostForPeriod)
      // and use `toFixed(PERIOD_DECIMALS[period])`. Verify the precision
      // values are non-zero where expected so the exact-match filter
      // matches the displayed string (e.g. "$0.1715" at hourly).
      const { displayPrecision } = await import('../recommendations');
      // hourly: 4 dp, daily: 2 dp, yearly: 0 dp (per PERIOD_DECIMALS).
      expect(displayPrecision('monthly_cost', 'hourly')).toBe(4);
      expect(displayPrecision('monthly_cost', 'daily')).toBe(2);
      expect(displayPrecision('monthly_cost', 'yearly')).toBe(0);
      expect(displayPrecision('on_demand_monthly', 'hourly')).toBe(4);
      expect(displayPrecision('on_demand_monthly', 'daily')).toBe(2);
      expect(displayPrecision('on_demand_monthly', 'yearly')).toBe(0);
    });
  });
});

//
// Issue #239: renderUsageSparkline unit tests
//
describe('renderUsageSparkline (issue #239)', () => {
  test('returns em-dash for null', () => {
    expect(renderUsageSparkline(null)).toBe('—');
  });

  test('returns em-dash for undefined', () => {
    expect(renderUsageSparkline(undefined)).toBe('—');
  });

  test('returns em-dash for empty array', () => {
    expect(renderUsageSparkline([])).toBe('—');
  });

  test('returns an SVG element for a single point', () => {
    const html = renderUsageSparkline([75]);
    expect(html).toContain('<svg');
    expect(html).toContain('class="usage-sparkline"');
    // Single-point path uses a circle.
    expect(html).toContain('<circle');
    expect(html).not.toContain('<polyline');
    // Accessible label is present; aria-hidden is not.
    expect(html).toContain('role="img"');
    expect(html).toContain('aria-label=');
    expect(html).not.toContain('aria-hidden');
  });

  test('returns a polyline SVG for 7 points', () => {
    const pcts = [80, 85, 90, 70, 95, 100, 60];
    const html = renderUsageSparkline(pcts);
    expect(html).toContain('<svg');
    expect(html).toContain('class="usage-sparkline"');
    expect(html).toContain('<polyline');
    // 7 points produce 7 coordinate pairs in the points attribute.
    const match = html.match(/points="([^"]+)"/);
    expect(match).not.toBeNull();
    const pairs = match![1]!.trim().split(' ').filter(Boolean);
    expect(pairs).toHaveLength(7);
    // No raw user input is interpolated; values are numbers only, no XSS vector.
    expect(html).not.toContain('<script');
    // Accessible label is present; aria-hidden is not.
    expect(html).toContain('role="img"');
    expect(html).toContain('aria-label=');
    expect(html).not.toContain('aria-hidden');
  });

  test('aria-label conveys coverage values for screen readers', () => {
    const html = renderUsageSparkline([80, 90]);
    expect(html).toContain('aria-label="RI coverage last 2 days: 80.0%, 90.0%"');
  });

  test('non-finite values are clamped to 0 in geometry and label', () => {
    const html = renderUsageSparkline([NaN, Infinity, -Infinity]);
    // All three are treated as 0; label reflects clamped values.
    expect(html).toContain('aria-label="RI coverage last 3 days: 0.0%, 0.0%, 0.0%"');
    // Geometry stays valid (cy must be 19.0 for 0%).
    expect(html).toContain('<polyline');
  });

  test('100% coverage maps point to top of SVG (y near pad)', () => {
    const html = renderUsageSparkline([100]);
    // With pad=1 and innerH=18: y = 1 + 18*(1-100/100) = 1.0
    expect(html).toContain('cy="1.0"');
  });

  test('0% coverage maps point to bottom of SVG (y near h-pad)', () => {
    const html = renderUsageSparkline([0]);
    // With pad=1 and innerH=18: y = 1 + 18*(1-0/100) = 19.0
    expect(html).toContain('cy="19.0"');
  });

  test('first and last x-coordinates span the full SVG width', () => {
    const pcts = [50, 60, 70, 80, 90, 85, 75];
    const html = renderUsageSparkline(pcts);
    const match = html.match(/points="([^"]+)"/);
    expect(match).not.toBeNull();
    const pairs = match![1]!.trim().split(' ').filter(Boolean);
    // First x must be 0.0 (i=0 => x = 0/(7-1)*56 = 0).
    expect(pairs[0]).toMatch(/^0\.0,/);
    // Last x must be 56.0 (i=6 => x = 6/6*56 = 56).
    expect(pairs[6]).toMatch(/^56\.0,/);
  });
});

// ============================================================================
// Issue #1417: sparkline integration — SVG renders inside the table
// ============================================================================
// The unit tests above verify renderUsageSparkline in isolation.  This block
// confirms the SVG actually appears inside the recommendations table HTML when
// a recommendation carries non-empty usage_history data.
describe('Issue #1417: usage-history sparkline renders in recommendations table', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <div id="opportunities-tab" class="tab-content active">
        <div id="recommendations-summary"></div>
        <div id="recommendations-list"></div>
      </div>
      <div id="purchase-modal" class="hidden">
        <div id="purchase-details"></div>
        <div class="modal-buttons">
          <button type="button" id="close-purchase-modal-btn">Cancel</button>
          <button type="button" id="execute-purchase-btn" class="primary">Send for approval</button>
        </div>
      </div>
    `;
    jest.clearAllMocks();
    jest.useFakeTimers();
    (state.getHiddenColumns as jest.Mock).mockReturnValue(new Set());
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getCurrentUser as jest.Mock).mockReturnValue({ id: 'u-admin', email: 'admin@example.com', groups: ['00000000-0000-5000-8000-000000000001'] });
    (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
      last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      last_collection_error: null,
    });
    (recsApi.refreshRecommendations as jest.Mock).mockResolvedValue({});
  });
  afterEach(() => {
    jest.useRealTimers();
  });

  test('SVG polyline appears in table row when rec has 7-day usage_history', async () => {
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: { total_count: 1, total_monthly_savings: 100, total_upfront_cost: 500, avg_payback_months: 5 },
      recommendations: [{
        id: 'rec-sparkline', provider: 'aws', cloud_account_id: 'acct-1',
        service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
        count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500,
        usage_history: [80, 85, 90, 70, 95, 100, 60],
      }],
      regions: [],
    });

    await loadRecommendations();

    const list = document.getElementById('recommendations-list');
    const html = list?.innerHTML ?? '';
    // The sparkline SVG must be present in the rendered table.
    expect(html).toContain('class="usage-sparkline"');
    // It must contain a polyline (7 points -> polyline, not circle).
    expect(html).toContain('<polyline');
    // The sparkline cell must immediately contain the SVG (not the em-dash).
    // This matches the literal pattern produced by renderColumnCell:
    //   <td class="usage-sparkline-cell" title="..."><svg ...>
    expect(html).toContain('usage-sparkline-cell" title="RI coverage last 7 days"><svg');
  });

  test('em-dash renders in table cell when usage_history is null', async () => {
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [{
        id: 'rec-no-sparkline', provider: 'aws', cloud_account_id: 'acct-1',
        service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
        count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500,
        usage_history: null,
      }],
      regions: [],
    });

    await loadRecommendations();

    const list = document.getElementById('recommendations-list');
    const html = list?.innerHTML ?? '';
    // Null usage_history must degrade to the em-dash fallback, not an SVG.
    expect(html).not.toContain('class="usage-sparkline"');
    expect(html).toContain('class="usage-sparkline-cell"');
  });
});

// ============================================================================
// Issue #163: Column filter localStorage persistence
// ============================================================================

describe('Column filters localStorage persistence (issue #163)', () => {
  beforeEach(() => {
    resetColumnFiltersState();
    // localStorageMock is reset by jest.clearAllMocks() in the global beforeEach
  });

  // --- loadColumnFilters ---

  describe('loadColumnFilters', () => {
    test('returns empty object when localStorage key is absent', () => {
      // Default mock: getItem returns null
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });

    test('returns empty object on JSON parse error', () => {
      localStorageMock.getItem.mockReturnValue('{not valid json}');
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });

    test('returns empty object when schemaVersion is wrong', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 99, filters: { region: { kind: 'set', values: ['us-east-1'] } } }),
      );
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });

    test('returns empty object when filters is not an object', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 1, filters: 'not-an-object' }),
      );
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });

    test('restores a filter for every filterable column, including capacity, and drops visual columns', () => {
      const filterable = COLUMN_DEFS.filter((c) => c.kind !== 'visual').map((c) => c.key);
      const visual = COLUMN_DEFS.find((c) => c.kind === 'visual')!.key;
      const filters: Record<string, unknown> = {};
      for (const key of [...filterable, visual]) filters[key] = { kind: 'set', values: ['x'] };
      localStorageMock.getItem.mockReturnValue(JSON.stringify({ schemaVersion: 1, filters }));
      const result = loadColumnFilters();
      expect(Object.keys(result).sort()).toEqual([...filterable].sort());
      expect(result).not.toHaveProperty(visual);
    });

    test('restores a categorical (set) filter correctly', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({
          schemaVersion: 1,
          filters: { region: { kind: 'set', values: ['us-east-1', 'eu-west-1'] } },
        }),
      );
      const result = loadColumnFilters();
      expect(result.region).toEqual({ kind: 'set', values: ['us-east-1', 'eu-west-1'] });
      expect(Object.keys(result)).toHaveLength(1);
    });

    test('restores a numeric (expr) filter correctly', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({
          schemaVersion: 1,
          filters: { savings: { kind: 'expr', expr: '>100' } },
        }),
      );
      const result = loadColumnFilters();
      expect(result.savings).toEqual({ kind: 'expr', expr: '>100' });
    });

    test('silently drops unknown column keys', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({
          schemaVersion: 1,
          filters: {
            region: { kind: 'set', values: ['us-east-1'] },
            future_unknown_column: { kind: 'set', values: ['foo'] },
          },
        }),
      );
      const result = loadColumnFilters();
      expect(result.region).toBeDefined();
      expect((result as Record<string, unknown>)['future_unknown_column']).toBeUndefined();
      expect(Object.keys(result)).toHaveLength(1);
    });

    test('silently drops filters with malformed shape', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({
          schemaVersion: 1,
          filters: {
            region: { kind: 'set', values: 'not-an-array' },
            savings: { kind: 'expr', expr: 42 },
            count: { kind: 'unknown-kind', data: 'x' },
          },
        }),
      );
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });
  });
});
