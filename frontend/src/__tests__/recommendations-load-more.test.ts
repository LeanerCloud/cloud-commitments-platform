/**
 * Recommendations module tests: loadRecommendations rendering and error tests.
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

    test('variants differing only by payment get distinct checkbox names (issue #746)', async () => {
      const base = { provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 150, upfront_cost: 1000 };
      const recs = [
        { ...base, id: 'rec-np', payment: 'no-upfront' },
        { ...base, id: 'rec-au', payment: 'all-upfront' },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: { total_count: 2, total_monthly_savings: 300, total_upfront_cost: 2000, avg_payback_months: 1 },
        recommendations: recs,
        regions: [],
      });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
      await loadRecommendations();
      document.querySelector<HTMLButtonElement>('.rec-cell-chevron')!.click();

      const label = (id: string) =>
        document.querySelector<HTMLInputElement>(`input[data-rec-id="${id}"]`)?.getAttribute('aria-label') ?? '';
      expect(label('rec-np')).toContain('No Upfront');
      expect(label('rec-au')).toContain('All Upfront');
      expect(label('rec-np')).not.toBe(label('rec-au'));
    });
  });
});
