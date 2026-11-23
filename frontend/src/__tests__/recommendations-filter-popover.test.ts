/**
 * Recommendations module tests: column filter All semantics, popover scrolling and numeric filter display tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, setupOpportunitiesTabDom } from './recommendations-harness';
import { loadRecommendations } from '../recommendations';
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
// Issue #481: Sort column + direction persisted across page refresh via
// URL query params (?sort=<col>&dir=<asc|desc>).
//
describe('Issue #481: URL persistence of sort state', () => {
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
    // Reset URL between tests.
    window.history.replaceState({}, '', '/');
  });

  test('invalid ?dir=foo is silently ignored', async () => {
    window.history.replaceState({}, '', '/?sort=provider&dir=foo');
    await loadRecommendations();
    expect(state.setRecommendationsSort).not.toHaveBeenCalledWith(
      expect.objectContaining({ direction: 'foo' }),
    );
  });

  test('clicking a header writes the active sort to the URL', async () => {
    await loadRecommendations();
    const header = document.querySelector<HTMLTableCellElement>('th[data-sort="upfront_cost"]');
    header!.click();
    const params = new URLSearchParams(window.location.search);
    expect(params.get('sort')).toBe('upfront_cost');
    expect(params.get('dir')).toBe('asc');
  });
});
