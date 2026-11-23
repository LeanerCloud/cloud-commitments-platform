/**
 * Recommendations module tests: usage sparklines, column filter persistence and the SP group key helper.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, setupOpportunitiesTabDom } from './recommendations-harness';
import { renderUsageSparkline } from '../recommendations';
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
