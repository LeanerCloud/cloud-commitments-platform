/**
 * Recommendations module tests: the last loadRecommendations tests and the first sort, bulk toolbar and detail drawer tests.
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

    test('individual row checkbox adds recommendation when checked', async () => {
      const mockRecs = [
        { id: 'rec-9', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 }
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: mockRecs,
        regions: []
      });

      await loadRecommendations();

      const checkbox = document.querySelector('input[data-rec-id]') as HTMLInputElement;
      checkbox.checked = true;
      checkbox.dispatchEvent(new Event('change'));

      expect(state.addSelectedRecommendation).toHaveBeenCalled();
      expect((state.addSelectedRecommendation as jest.Mock).mock.calls[0][0]).toMatch(/^rec-/);
    });

    test('individual row checkbox removes recommendation when unchecked', async () => {
      const mockRecs = [
        { id: 'rec-10', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 }
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: mockRecs,
        regions: []
      });

      await loadRecommendations();

      const checkbox = document.querySelector('input[data-rec-id]') as HTMLInputElement;
      checkbox.checked = false;
      checkbox.dispatchEvent(new Event('change'));

      expect(state.removeSelectedRecommendation).toHaveBeenCalled();
      expect((state.removeSelectedRecommendation as jest.Mock).mock.calls[0][0]).toMatch(/^rec-/);
    });

    // Issue #187: when two Azure recs share `(provider, service, region,
    // resource_type, payment)` but differ in subscription or term, they
    // each carry a distinct backend ID after the scheduler hash fix
    // (see TestScheduler_ConvertRecommendations_HashUniqueness). The
    // frontend selection toggle keys on data-rec-id, so distinct IDs →
    // independent toggles. Pin that here so a future regression on the
    // frontend rendering side surfaces immediately.
    test('toggling one row does NOT flip a sibling row with a distinct ID', async () => {
      const mockRecs = [
        { id: 'rec-azure-sub1', provider: 'azure', cloud_account_id: 'sub1', service: 'compute', resource_type: 'D2s', region: 'eastus', count: 1, term: 1, savings: 100, upfront_cost: 500 },
        { id: 'rec-azure-sub2', provider: 'azure', cloud_account_id: 'sub2', service: 'compute', resource_type: 'D2s', region: 'eastus', count: 1, term: 1, savings: 200, upfront_cost: 800 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: mockRecs,
        regions: []
      });

      await loadRecommendations();

      const checkboxes = Array.from(
        document.querySelectorAll<HTMLInputElement>('tbody input[data-rec-id]'),
      );
      expect(checkboxes).toHaveLength(2);
      const ids = checkboxes.map((cb) => cb.dataset['recId']).sort();
      expect(ids).toEqual(['rec-azure-sub1', 'rec-azure-sub2']);

      // Tick rec-azure-sub1 specifically — the table sort order would
      // otherwise depend on savings (sub2's 200 > sub1's 100), so we
      // pick by ID, not index.
      const sub1 = checkboxes.find((cb) => cb.dataset['recId'] === 'rec-azure-sub1');
      expect(sub1).toBeDefined();
      sub1!.checked = true;
      sub1!.dispatchEvent(new Event('change'));

      // Only sub1's ID should land in addSelectedRecommendation; sub2
      // stays untouched.
      const calls = (state.addSelectedRecommendation as jest.Mock).mock.calls;
      const calledIds = calls.map((c) => c[0]);
      expect(calledIds).toContain('rec-azure-sub1');
      expect(calledIds).not.toContain('rec-azure-sub2');
    });

    // Issue #224: when a sibling variant is selected but currently hidden by a
    // column filter (e.g. user filtered to 3yr, but 1yr sibling is still
    // selected in state), checking a 3yr sibling must evict the hidden 1yr
    // selection. The fix iterates state.getRecommendations() (full loaded set)
    // rather than the filtered `recommendations` array rendered in the DOM.
    test('checking a visible variant evicts a same-cell sibling hidden by filter', async () => {
      const allRecs = [
        { id: 'rec-1yr', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 0 },
        { id: 'rec-3yr', provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 3, savings: 300, upfront_cost: 0 },
      ];
      // Simulate: API returns all recs, but only the 3yr one is visible (1yr is
      // hidden by filter). state.getRecommendations() returns the full set.
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [allRecs[1]], // only 3yr visible in rendered list
        regions: []
      });
      (state.getRecommendations as jest.Mock).mockReturnValue(allRecs); // full set in state
      (state as unknown as { getRecommendationByID: jest.Mock }).getRecommendationByID.mockImplementation((id: string) => allRecs.find((r) => r.id === id));
      // Simulate: 1yr is already selected in state.
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['rec-1yr']));

      await loadRecommendations();

      // Tick the 3yr checkbox.
      const cb = document.querySelector<HTMLInputElement>('input[data-rec-id="rec-3yr"]');
      expect(cb).not.toBeNull();
      cb!.checked = true;
      cb!.dispatchEvent(new Event('change'));

      // The hidden 1yr sibling must be evicted.
      const removeCalls = (state.removeSelectedRecommendation as jest.Mock).mock.calls.map(c => c[0]);
      expect(removeCalls).toContain('rec-1yr');
      // And the 3yr rec is added.
      const addCalls = (state.addSelectedRecommendation as jest.Mock).mock.calls.map(c => c[0]);
      expect(addCalls).toContain('rec-3yr');
    });
  });
});
