/**
 * Recommendations module tests
 */
// The harness registers the module mocks, so it must be imported first.
import {
  mockFactoryDefaults,
  mkRec,
  setupOpportunitiesTabDom,
  mkSpRec,
} from './recommendations-harness';
import {
  loadRecommendations,
  displaySavingsPct,
  groupRecsByCell,
  resetExpandedCells,
  COLUMN_DEFS,
  renderUsageSparkline,
  loadColumnFilters,
  saveColumnFilters,
  resetColumnFiltersState,
  spGroupKey,
  groupSpCellKeys,
  scaleRecForCapacity,
  weightedDisplaySavingsPct,
} from '../recommendations';
import type { CostPeriod } from '../state';
import type { LocalRecommendation } from '../types';
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
// Issue #483: Scrolling inside the popover does NOT dismiss it.
//
describe('Issue #483: popover stays open while user scrolls its contents', () => {
  const recs = [
    { id: 'r1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
  ];

  beforeEach(() => {
    setupOpportunitiesTabDom();
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
      last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      last_collection_error: null,
    });
  });

  test('a scroll event outside the popover still dismisses it', async () => {
    await loadRecommendations();
    document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="resource_type"]')!.click();
    expect(document.querySelector('.column-filter-popover')).not.toBeNull();
    // Scroll the document body (outside the popover) — should still close.
    const scrollEvt = new Event('scroll', { bubbles: true });
    document.body.dispatchEvent(scrollEvt);
    expect(document.querySelector('.column-filter-popover')).toBeNull();
  });

  test('a commit that re-renders the table keeps the new trigger expanded', async () => {
    await loadRecommendations();
    const oldTrigger = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="resource_type"]')!;
    oldTrigger.click();
    const popover = document.querySelector<HTMLElement>('.column-filter-popover')!;
    const box = popover.querySelector<HTMLInputElement>('input[data-value="t3.medium"]')!;
    box.checked = false;
    box.dispatchEvent(new Event('change', { bubbles: true }));
    const newTrigger = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="resource_type"]')!;
    expect(newTrigger).not.toBe(oldTrigger);
    expect(document.querySelector('.column-filter-popover')).toBe(popover);
    expect(newTrigger.getAttribute('aria-expanded')).toBe('true');
  });

  test('a scroll while the trigger stays on screen keeps the popover open and re-anchors it', async () => {
    // Clicking a funnel at the table scroller's clipped edge scrolls the
    // table (and the mirrored strip a frame later); those scroll events
    // must not close the popover the click just opened.
    await loadRecommendations();
    const trigger = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="resource_type"]')!;
    let left = 400;
    trigger.getBoundingClientRect = () => ({
      left, top: 300, width: 24, height: 24, right: left + 24, bottom: 324, x: left, y: 300, toJSON: () => ({}),
    }) as DOMRect;
    trigger.click();
    const popover = document.querySelector<HTMLElement>('.column-filter-popover');
    expect(popover).not.toBeNull();
    expect(popover!.style.left).toBe('400px');
    left = 250;
    document.body.dispatchEvent(new Event('scroll', { bubbles: true }));
    expect(document.querySelector('.column-filter-popover')).toBe(popover);
    expect(popover!.style.left).toBe('250px');
  });
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

  test('typing the rounded-display value of a row matches that row at daily period', async () => {
    // Under daily period (PERIOD_DECIMALS.daily = 2), raw monthly
    // 123.456789 / 30 = 4.1152263 → displays as "$4.12" (2 dp). Filter
    // expression "4.12" must match the row even though the raw scaled
    // value (4.1152263) doesn't equal 4.12.
    (state.getCostPeriod as jest.Mock).mockReturnValue('daily');
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      savings: { kind: 'expr', expr: '4.12' },
    });
    const { applyColumnFilters } = await import('../recommendations');
    // Cast through unknown to match the LocalRecommendation shape used
    // by the function; the test recs include all the fields the filter
    // actually reads (savings).
    const filtered = applyColumnFilters(recs as unknown as Parameters<typeof applyColumnFilters>[0], state.getRecommendationsColumnFilters());
    expect(filtered.map((r) => r.id)).toEqual(['r-exact']);
  });

  test('typing a value that does NOT match the rounded display excludes the row', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('daily');
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      savings: { kind: 'expr', expr: '4.11' },
    });
    const { applyColumnFilters } = await import('../recommendations');
    const filtered = applyColumnFilters(recs as unknown as Parameters<typeof applyColumnFilters>[0], state.getRecommendationsColumnFilters());
    expect(filtered.map((r) => r.id)).toEqual([]);
  });

  test('comparison operators apply against the rounded value too (>5 with a raw 4.999 stays excluded)', async () => {
    // Raw monthly 4.999 rounds to 5.00 at monthly period (0 dp → 5).
    // Under "monthly" period the display precision for savings is 0
    // decimals, so 4.999 displays as "$5" → ">4" includes it, ">5"
    // does not (since rounded = 5 and 5 > 5 is false).
    const sub = [
      { id: 'r-near-5', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 4.999, upfront_cost: 0 },
    ];
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      savings: { kind: 'expr', expr: '>4' },
    });
    const { applyColumnFilters } = await import('../recommendations');
    expect(applyColumnFilters(sub as unknown as Parameters<typeof applyColumnFilters>[0], state.getRecommendationsColumnFilters()).map((r) => r.id)).toEqual(['r-near-5']);

    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      savings: { kind: 'expr', expr: '>5' },
    });
    expect(applyColumnFilters(sub as unknown as Parameters<typeof applyColumnFilters>[0], state.getRecommendationsColumnFilters()).map((r) => r.id)).toEqual([]);
  });

  test('integer columns continue to match exact integer values', async () => {
    const sub = [
      { id: 'r-5',  provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 5,  term: 1, savings: 100, upfront_cost: 0 },
      { id: 'r-10', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 10, term: 1, savings: 100, upfront_cost: 0 },
    ];
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      count: { kind: 'expr', expr: '5' },
    });
    const { applyColumnFilters } = await import('../recommendations');
    expect(applyColumnFilters(sub as unknown as Parameters<typeof applyColumnFilters>[0], state.getRecommendationsColumnFilters()).map((r) => r.id)).toEqual(['r-5']);
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
    function fractionDigitsOf(s: string): number {
      // Strip the leading currency symbol and formatCurrency's en-US comma
      // group separators (#1728), then count digits after a decimal point.
      // Returns 0 for "$123", 2 for "$123.45", etc.
      const stripped = s.replace(/[^0-9.]/g, '');
      const dot = stripped.indexOf('.');
      return dot < 0 ? 0 : stripped.length - dot - 1;
    }

    test('upfront_cost: displayPrecision matches formatCurrency at every period', async () => {
      const recsMod = await import('../recommendations');
      // utils is jest.mock()'d at the top of this file (so the
      // recommendations module sees a stubbed formatCurrency). For this
      // test we want the REAL formatCurrency to assert the fraction-digit
      // contract holds against actual production behaviour, so we pull it
      // via jest.requireActual rather than the mocked import.
      const utilsActual = jest.requireActual<typeof import('../utils')>('../utils');
      const displayPrecision = recsMod.displayPrecision;
      const formatCurrency = utilsActual.formatCurrency;
      // upfront_cost is always rendered via plain formatCurrency (no
      // period scaling). The fraction digit count must therefore be
      // period-independent and equal to formatCurrency's own output.
      const sample = 123.456789;
      const renderedDigits = fractionDigitsOf(formatCurrency(sample));
      const periods: CostPeriod[] = ['hourly', 'daily', 'monthly', 'yearly'];
      for (const period of periods) {
        expect(displayPrecision('upfront_cost', period)).toBe(renderedDigits);
      }
    });

    test('monthly_cost: displayPrecision at monthly period matches formatCurrency', async () => {
      const recsMod = await import('../recommendations');
      // utils is jest.mock()'d at the top of this file (so the
      // recommendations module sees a stubbed formatCurrency). For this
      // test we want the REAL formatCurrency to assert the fraction-digit
      // contract holds against actual production behaviour, so we pull it
      // via jest.requireActual rather than the mocked import.
      const utilsActual = jest.requireActual<typeof import('../utils')>('../utils');
      const displayPrecision = recsMod.displayPrecision;
      const formatCurrency = utilsActual.formatCurrency;
      const sample = 123.456789;
      const renderedDigits = fractionDigitsOf(formatCurrency(sample));
      // At monthly period, formatCostForPeriod delegates to formatCurrency
      // → displayPrecision must agree with formatCurrency's digit count.
      expect(displayPrecision('monthly_cost', 'monthly')).toBe(renderedDigits);
    });

    test('on_demand_monthly: displayPrecision at monthly period matches formatCurrency', async () => {
      const recsMod = await import('../recommendations');
      // utils is jest.mock()'d at the top of this file (so the
      // recommendations module sees a stubbed formatCurrency). For this
      // test we want the REAL formatCurrency to assert the fraction-digit
      // contract holds against actual production behaviour, so we pull it
      // via jest.requireActual rather than the mocked import.
      const utilsActual = jest.requireActual<typeof import('../utils')>('../utils');
      const displayPrecision = recsMod.displayPrecision;
      const formatCurrency = utilsActual.formatCurrency;
      const sample = 123.456789;
      const renderedDigits = fractionDigitsOf(formatCurrency(sample));
      expect(displayPrecision('on_demand_monthly', 'monthly')).toBe(renderedDigits);
    });

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

    test('silently drops expr filter with empty string expr', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({
          schemaVersion: 1,
          filters: { savings: { kind: 'expr', expr: '' } },
        }),
      );
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });
  });

  // --- saveColumnFilters ---

  describe('saveColumnFilters', () => {
    test('writes correct JSON shape to localStorage', () => {
      saveColumnFilters({ region: { kind: 'set', values: ['us-east-1'] } });
      expect(localStorageMock.setItem).toHaveBeenCalledWith(
        'cudly.recs.columnFilters.v1',
        expect.stringContaining('"schemaVersion":1'),
      );
      const callArg = localStorageMock.setItem.mock.calls[0]?.[1] as string;
      const parsed = JSON.parse(callArg);
      expect(parsed.schemaVersion).toBe(1);
      expect(parsed.filters.region).toEqual({ kind: 'set', values: ['us-east-1'] });
    });

    test('writes empty filters object when no filters active', () => {
      saveColumnFilters({});
      const callArg = localStorageMock.setItem.mock.calls[0]?.[1] as string;
      const parsed = JSON.parse(callArg);
      expect(parsed.filters).toEqual({});
    });

    test('round-trips a numeric expr filter', () => {
      saveColumnFilters({ savings: { kind: 'expr', expr: '>500' } });
      const callArg = localStorageMock.setItem.mock.calls[0]?.[1] as string;
      const payload = JSON.parse(callArg);
      expect(payload.filters.savings).toEqual({ kind: 'expr', expr: '>500' });
    });
  });

  // --- round-trip and guard tests ---

  describe('persistence round-trip', () => {
    test('save then load restores same filter state', () => {
      const filters = {
        region: { kind: 'set' as const, values: ['ap-southeast-1'] },
        savings: { kind: 'expr' as const, expr: '>200' },
      };
      const store = new Map<string, string>();
      localStorageMock.getItem.mockImplementation((k: string) => store.get(k) ?? null);
      localStorageMock.setItem.mockImplementation((k: string, v: string) => { store.set(k, v); });
      saveColumnFilters(filters);
      const restored = loadColumnFilters();
      expect(restored.region).toEqual({ kind: 'set', values: ['ap-southeast-1'] });
      expect(restored.savings).toEqual({ kind: 'expr', expr: '>200' });
      expect(Object.keys(restored)).toHaveLength(2);
    });

    test('stored key with unknown column is silently dropped, no error', () => {
      const store = new Map<string, string>([
        ['cudly.recs.columnFilters.v1', JSON.stringify({
          schemaVersion: 1,
          filters: {
            region: { kind: 'set', values: ['us-east-1'] },
            deleted_account_col: { kind: 'set', values: ['123456789012'] },
          },
        })],
      ]);
      localStorageMock.getItem.mockImplementation((k: string) => store.get(k) ?? null);
      expect(() => loadColumnFilters()).not.toThrow();
      const result = loadColumnFilters();
      expect(result.region).toBeDefined();
      expect((result as Record<string, unknown>)['deleted_account_col']).toBeUndefined();
    });

    test('corrupted JSON in localStorage falls back to empty object, no error', () => {
      localStorageMock.getItem.mockReturnValue('}{invalid json][');
      expect(() => loadColumnFilters()).not.toThrow();
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });
  });
});

//
// Issue #135: SP plan-type row grouping in the Recommendations table
//

describe('Issue #135: spGroupKey helper', () => {
  test('returns sp-group| prefix with provider, account_id, region', () => {
    const rec = mkSpRec('savings-plans-compute');
    expect(spGroupKey(rec)).toBe('sp-group|aws|acct-1|us-east-1');
  });

  test('uses empty string for absent cloud_account_id', () => {
    const rec = mkSpRec('savings-plans-compute', { cloud_account_id: undefined });
    expect(spGroupKey(rec)).toBe('sp-group|aws||us-east-1');
  });

  test('different providers produce different keys', () => {
    const awsRec   = mkSpRec('savings-plans-compute', { provider: 'aws'   as never });
    const azureRec = mkSpRec('savingsplans',           { provider: 'azure' as never });
    expect(spGroupKey(awsRec)).not.toBe(spGroupKey(azureRec));
  });
});

describe('Issue #135: groupSpCellKeys helper', () => {
  test('returns empty map when no SP recs are present', () => {
    const recs = [mkRec({ id: 'e1', service: 'ec2' })];
    const groups = groupRecsByCell(recs);
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    expect(result.size).toBe(0);
  });

  test('returns empty map for a single SP plan type (no grouping needed)', () => {
    const recs = [mkSpRec('savings-plans-compute')];
    const groups = groupRecsByCell(recs);
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    expect(result.size).toBe(0);
  });

  test('groups two SP plan types sharing provider+account+region into one entry', () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c' }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s' }),
    ];
    const groups = groupRecsByCell(recs);
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    expect(result.size).toBe(1);
    const [key, cellKeys] = Array.from(result.entries())[0]!;
    expect(key).toBe('sp-group|aws|acct-1|us-east-1');
    expect(cellKeys).toHaveLength(2);
  });

  test('preserves sort order within each group', () => {
    const recs = [
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 300 }),
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-ec2instance', { id: 'sp-e', savings: 200 }),
    ];
    const groups = groupRecsByCell(recs);
    // Present in insertion order (no sort) for this test
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    expect(result.size).toBe(1);
    const cellKeys = Array.from(result.values())[0]!;
    // order must match sortedKeys order (insertion order here)
    expect(cellKeys).toHaveLength(3);
    expect(cellKeys).toEqual(sortedKeys);
  });

  test('does not group SP recs from different accounts', () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-a', cloud_account_id: 'acct-A' }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-b', cloud_account_id: 'acct-B' }),
    ];
    const groups = groupRecsByCell(recs);
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    // Different accounts -> two singleton scopes -> no parent row
    expect(result.size).toBe(0);
  });

  test('does not group SP recs from different regions', () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-a', region: 'us-east-1' }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-b', region: 'eu-west-1' }),
    ];
    const groups = groupRecsByCell(recs);
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    expect(result.size).toBe(0);
  });

  test('non-SP recs in same scope are excluded from the SP group', () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c' }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s' }),
      mkRec({ id: 'ec2-1', service: 'ec2', cloud_account_id: 'acct-1', region: 'us-east-1' }),
    ];
    const groups = groupRecsByCell(recs);
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    // Only the two SP cells form a group; EC2 is not in any SP group.
    expect(result.size).toBe(1);
    const cellKeys = Array.from(result.values())[0]!;
    expect(cellKeys).toHaveLength(2);
  });
});

describe('Issue #135: SP group parent row rendered in table', () => {
  const setupDom = (): void => {
    document.body.innerHTML = [
      '<div id="opportunities-tab" class="tab-content active">',
      '<div id="recommendations-summary"></div>',
      '<div id="recommendations-list"></div>',
      '</div>',
      '<div id="purchase-modal" class="hidden">',
      '<div id="purchase-details"></div>',
      '</div>',
    ].join('');
  };

  beforeEach(() => {
    setupDom();
    jest.clearAllMocks();
    (state.getHiddenColumns as jest.Mock).mockReturnValue(new Set());
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
      last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      last_collection_error: null,
    });
    resetExpandedCells();
  });

  test('two SP plan types render one parent group row and no flat child rows (collapsed)', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    // One parent group row
    const groupRows = document.querySelectorAll('.rec-sp-group-row');
    expect(groupRows.length).toBe(1);

    // No flat recommendation-row children rendered yet (collapsed)
    const recRows = document.querySelectorAll('.recommendation-row');
    expect(recRows.length).toBe(0);
  });

  test('parent row service badge shows combined plan-type label', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    const badge = document.querySelector('.rec-sp-group-badge');
    expect(badge).not.toBeNull();
    const text = badge!.textContent ?? '';
    expect(text).toContain('Savings Plans');
    expect(text).toContain('Compute');
    expect(text).toContain('SageMaker');
  });

  test('parent row badge shows plan type count badge', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    const countBadge = document.querySelector('.rec-sp-plan-count');
    expect(countBadge).not.toBeNull();
    expect(countBadge!.textContent).toContain('2 plan types');
  });

  test('parent row chevron starts collapsed (aria-expanded=false)', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    const chevron = document.querySelector<HTMLButtonElement>('.rec-sp-group-chevron');
    expect(chevron).not.toBeNull();
    expect(chevron!.getAttribute('aria-expanded')).toBe('false');
  });

  test('clicking SP group chevron expands and shows child rows', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    const chevron = document.querySelector<HTMLButtonElement>('.rec-sp-group-chevron');
    chevron!.click();

    // After expand: child rows should appear
    const recRows = document.querySelectorAll('.recommendation-row');
    expect(recRows.length).toBe(2);

    // aria-expanded should flip
    const updatedChevron = document.querySelector<HTMLButtonElement>('.rec-sp-group-chevron');
    expect(updatedChevron!.getAttribute('aria-expanded')).toBe('true');

    // Collapse back: child rows should hide again and aria-expanded should reset.
    updatedChevron!.click();
    expect(document.querySelectorAll('.recommendation-row').length).toBe(0);
    const collapsedChevron = document.querySelector<HTMLButtonElement>('.rec-sp-group-chevron');
    expect(collapsedChevron!.getAttribute('aria-expanded')).toBe('false');
  });

  test('single SP plan type renders as a flat row with no group parent', async () => {
    const recs = [mkSpRec('savings-plans-compute', { id: 'sp-c', savings: 100 })];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    // No SP group parent row
    expect(document.querySelectorAll('.rec-sp-group-row').length).toBe(0);
    // One flat recommendation row rendered directly
    expect(document.querySelectorAll('.recommendation-row').length).toBe(1);
  });

  test('non-SP rows are not absorbed into the SP group', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
      mkRec({ id: 'ec2-1', service: 'ec2', cloud_account_id: 'acct-1', region: 'us-east-1', savings: 50 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    // One SP group parent row
    expect(document.querySelectorAll('.rec-sp-group-row').length).toBe(1);
    // The EC2 row renders as a flat recommendation-row (not inside SP group)
    expect(document.querySelectorAll('.recommendation-row').length).toBe(1);
  });

  test('SP group savings text shows aggregated savings from all plan types', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    const groupSavings = document.querySelector('.rec-sp-group-savings');
    expect(groupSavings).not.toBeNull();
    // Should reflect sum: 100 + 200 = 300
    expect(groupSavings!.textContent).toContain('$300');
  });

  test('issue #252: SP group savings does not double-apply the cost-period factor', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    // Monthly total is $300. On "yearly" (factor 12) the correct figure is
    // $3,600/yr. Pre-fix, scaleCost was applied once outside and again
    // inside formatCostForPeriod, yielding $36,000/yr (12x too high).
    (state.getCostPeriod as jest.Mock).mockReturnValue('yearly');
    await loadRecommendations();

    const groupSavings = document.querySelector('.rec-sp-group-savings');
    expect(groupSavings).not.toBeNull();
    expect(groupSavings!.textContent).toContain('$3600');
    expect(groupSavings!.textContent).not.toContain('$36000');
  });
});

// ============================================================================
// Issue #1419: account display name shown for non-admin users
// ============================================================================
// loadRecommendations must call listAccountsMinimal() (which hits
// /api/accounts/list, accessible to all users with view:recommendations)
// rather than listAccounts() (which hits /api/accounts, admin-only).
// Non-admin users previously saw the raw UUID because listAccounts()
// returned 403, .catch(() => []) silenced it, and the empty cache caused
// the ID to fall through as the display value.
describe('Issue #1419: Opportunities Account column shows display name via listAccountsMinimal', () => {
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
    (state.getCurrentUser as jest.Mock).mockReturnValue({ id: 'u-viewer', email: 'viewer@example.com', groups: [] });
    (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
      last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      last_collection_error: null,
    });
    (recsApi.refreshRecommendations as jest.Mock).mockResolvedValue({});
  });
  afterEach(() => {
    jest.useRealTimers();
  });

  test('listAccountsMinimal() is called (not listAccounts) so non-admin users get account names', async () => {
    // Simulate listAccountsMinimal succeeding for a non-admin user.
    (api.listAccountsMinimal as jest.Mock).mockResolvedValue([
      { id: 'acct-uuid-1234', name: 'Production AWS', external_id: '123456789', provider: 'aws' },
    ]);
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [{
        id: 'rec-acct', provider: 'aws', cloud_account_id: 'acct-uuid-1234',
        service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
        count: 1, term: 1, payment: 'all-upfront', savings: 50, upfront_cost: 200,
      }],
      regions: [],
    });

    await loadRecommendations();

    // listAccountsMinimal must have been called (not the admin-only listAccounts).
    expect(api.listAccountsMinimal).toHaveBeenCalled();
    expect(api.listAccounts).not.toHaveBeenCalled();

    const list = document.getElementById('recommendations-list');
    const html = list?.innerHTML ?? '';
    // The display name must appear in the rendered row.
    expect(html).toContain('Production AWS');
    // The raw UUID must NOT appear as the cell value.
    expect(html).not.toContain('acct-uuid-1234');
  });

  test('falls back to account ID when listAccountsMinimal returns no matching entry', async () => {
    (api.listAccountsMinimal as jest.Mock).mockResolvedValue([
      { id: 'acct-other', name: 'Other Account', external_id: '999', provider: 'aws' },
    ]);
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [{
        id: 'rec-fallback', provider: 'aws', cloud_account_id: 'acct-uuid-no-match',
        service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
        count: 1, term: 1, payment: 'all-upfront', savings: 50, upfront_cost: 200,
      }],
      regions: [],
    });

    await loadRecommendations();

    const list = document.getElementById('recommendations-list');
    const html = list?.innerHTML ?? '';
    // No matching entry: falls back to the raw ID.
    expect(html).toContain('acct-uuid-no-match');
  });
});

describe('capacity scaling keeps Eff. % invariant (#136)', () => {
  const rec = {
    id: 'cap', provider: 'azure', service: 'vm', resource_type: 'D2s_v3', region: 'eastus',
    count: 4, term: 1, payment: 'no-upfront', savings: 900, upfront_cost: 0,
    monthly_cost: 1000, on_demand_cost: 1900,
  } as unknown as LocalRecommendation;

  test('on_demand_cost scales with count like savings and upfront', () => {
    const scaled = scaleRecForCapacity(rec, 50)!;
    expect(scaled.count).toBe(2);
    expect(scaled.savings).toBe(450);
    expect(scaled.on_demand_cost).toBe(950);
  });

  test.each([25, 50, 75, 100])('Eff. %% at %i%% capacity equals the unscaled 47.4%%', (capacity) => {
    const scaled = scaleRecForCapacity(rec, capacity)!;
    expect(displaySavingsPct(scaled)!.toFixed(1)).toBe('47.4');
    expect(weightedDisplaySavingsPct([scaled])!.toFixed(1)).toBe('47.4');
  });

  test('an absent on_demand_cost stays absent', () => {
    const scaled = scaleRecForCapacity({ ...rec, on_demand_cost: null } as LocalRecommendation, 50)!;
    expect(scaled.on_demand_cost).toBeNull();
  });
});
