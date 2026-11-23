/**
 * Recommendations module tests: loadRecommendations fetch and filter tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, setupRecommendationsTest } from './recommendations-harness';
import { loadRecommendations } from '../recommendations';
import * as api from '../api';
import * as state from '../state';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

describe('Recommendations Module', () => {
  beforeEach(setupRecommendationsTest);

  afterEach(() => {
    jest.useRealTimers();
  });

  describe('loadRecommendations', () => {
    afterEach(() => (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({}));

    test('fetches with provider/account_ids when no column filter is set', async () => {
      // No column filters active: service and region are absent from the
      // API call (undefined); provider + account_ids carry the only hints.
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: []
      });

      await loadRecommendations();

      expect(api.getRecommendations).toHaveBeenCalledWith({
        provider: 'all',
        account_ids: undefined,
        service: undefined,
        region: undefined,
      });
    });

    test('pushes single-value service filter to API params (issue #162)', async () => {
      // A single-value service column filter must be forwarded to the API
      // so the backend applies it in SQL, saving bandwidth on large tenants.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        service: { kind: 'set', values: ['ec2'] },
      });
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: [],
      });

      await loadRecommendations();

      expect(api.getRecommendations).toHaveBeenCalledWith(
        expect.objectContaining({ service: 'ec2' }),
      );
    });

    test('pushes single-value region filter to API params (issue #162)', async () => {
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        region: { kind: 'set', values: ['us-east-1'] },
      });
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: [],
      });

      await loadRecommendations();

      expect(api.getRecommendations).toHaveBeenCalledWith(
        expect.objectContaining({ region: 'us-east-1' }),
      );
    });

    test('does not push multi-value service filter to API (stays client-side)', async () => {
      // When the user has selected multiple services the server only supports
      // equality (single-value); fall back to client-side filtering.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        service: { kind: 'set', values: ['ec2', 'rds'] },
      });
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: [],
      });

      await loadRecommendations();

      const call = (api.getRecommendations as jest.Mock).mock.calls[0][0];
      expect(call.service).toBeUndefined();
    });

    test('renders recommendations summary', async () => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {
          total_count: 10,
          total_monthly_savings: 5000,
          total_upfront_cost: 10000,
          avg_payback_months: 2
        },
        recommendations: [],
        regions: []
      });

      await loadRecommendations();

      const summary = document.getElementById('recommendations-summary');
      expect(summary?.innerHTML).toContain('Total Recommendations');
      expect(summary?.innerHTML).toContain('Potential Monthly Savings');
      expect(summary?.innerHTML).toContain('Total Upfront Cost');
      expect(summary?.innerHTML).toContain('Payback Period');
    });

    test('Potential Monthly Savings card mirrors page-level range, not the API sum (#272)', async () => {
      // Two cells, two variants per cell. The cells share (provider, account,
      // service, resource_type, region, term, engine) within each group; the
      // variants differ by payment_option. The user can only buy one variant
      // per cell, so the achievable monthly savings is bounded by:
      //   min = sum of per-cell savingsMin = 100 + 200 = 300
      //   max = sum of per-cell savingsMax = 150 + 250 = 400
      // The API's total_monthly_savings sums all 4 variants (= 700), which
      // overstates achievable savings by ~75% on this 2-cell page. The card
      // must NOT render 700; it must render the range "$300 – $400".
      const recs = [
        { id: 'cell1-cheap',  provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront',     savings: 100, upfront_cost: 0 },
        { id: 'cell1-pricey', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment_option: 'all-upfront',    savings: 150, upfront_cost: 1000 },
        { id: 'cell2-cheap',  provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, payment_option: 'no-upfront',     savings: 200, upfront_cost: 0 },
        { id: 'cell2-pricey', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, payment_option: 'all-upfront',    savings: 250, upfront_cost: 2000 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: { total_count: 4, total_monthly_savings: 700, total_upfront_cost: 3000, avg_payback_months: 1 },
        recommendations: recs,
        regions: [],
      });
      // The summary card now reads the visible set via
      // state.getVisibleRecommendations() (so it stays in sync with
      // the banner under filter changes — see the next test for the
      // filter-divergence case). On an unfiltered initial render the
      // visible set equals the loaded set; mock it explicitly because
      // setVisibleRecommendations() inside renderRecommendationsList
      // is a no-op when state is mocked.
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

      await loadRecommendations();

      const summary = document.getElementById('recommendations-summary');
      const savingsCard = Array.from(summary?.querySelectorAll('.card') ?? [])
        .find((c) => c.querySelector('h3')?.textContent === 'Potential Monthly Savings');
      const value = savingsCard?.querySelector('.value.savings')?.textContent ?? '';
      // The page-level range is $300 – $400/mo; the API's flat 700 must not
      // appear on the card.
      expect(value).toContain('$300');
      expect(value).toContain('$400');
      expect(value).not.toContain('$700');
    });
  });
});
