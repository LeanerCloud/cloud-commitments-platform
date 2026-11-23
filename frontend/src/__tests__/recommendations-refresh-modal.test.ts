/**
 * Recommendations module tests: auto-refresh on page open and the first openPurchaseModal tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import {
  mockFactoryDefaults,
  mockShowToast,
  setupRecommendationsTest,
} from './recommendations-harness';
import { loadRecommendations, resetAutoRefreshInFlight } from '../recommendations';
import * as api from '../api';
import * as recsApi from '../api/recommendations';

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

  describe('auto-refresh on page open (#284)', () => {
    // Helper: set up the minimal DOM + api mock that loadRecommendations needs.
    function mockGetRecs() {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: [],
      });
    }

    beforeEach(() => {
      // Reset the dedup guard so each test starts with no refresh in flight.
      resetAutoRefreshInFlight();
    });

    test('202 accepted — success toast only after the started collect finishes', async () => {
      mockGetRecs();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValueOnce({
        last_collected_at: null,
        last_collection_error: null,
      });
      (recsApi.refreshRecommendations as jest.Mock).mockResolvedValue({
        started_at: new Date().toISOString(),
        last_collected_at: null,
      });
      let collecting = true;
      (recsApi.getRecommendationsFreshness as jest.Mock).mockImplementation(async () => ({
        last_collected_at: collecting ? null : new Date().toISOString(),
        last_collection_error: null,
        last_collection_started_at: collecting ? new Date().toISOString() : null,
      }));

      await loadRecommendations();
      await Promise.resolve();
      await Promise.resolve();
      await jest.advanceTimersByTimeAsync(10_000);

      // Marker still held: nothing announced and no reload yet.
      expect(mockShowToast).not.toHaveBeenCalledWith(
        expect.objectContaining({ message: 'Recommendations refreshed' }),
      );
      expect(api.getRecommendations).toHaveBeenCalledTimes(1);

      collecting = false;
      await jest.advanceTimersByTimeAsync(5_000);

      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'Recommendations refreshed', kind: 'success' }),
      );
      expect(mockShowToast).not.toHaveBeenCalledWith(
        expect.objectContaining({ kind: 'error' }),
      );
      expect(api.getRecommendations).toHaveBeenCalledTimes(2);
    });

    test('202 accepted — explicit failure when the started collect outlasts the poll budget', async () => {
      mockGetRecs();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValueOnce({
        last_collected_at: null,
        last_collection_error: null,
      });
      (recsApi.refreshRecommendations as jest.Mock).mockResolvedValue({
        started_at: new Date().toISOString(),
        last_collected_at: null,
      });
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
        last_collected_at: null,
        last_collection_error: null,
        last_collection_started_at: new Date().toISOString(),
      });

      await loadRecommendations();
      await Promise.resolve();
      await Promise.resolve();
      await jest.advanceTimersByTimeAsync(60 * 5_000);

      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({
          message: 'Recommendations refresh is taking longer than expected; try again shortly',
          kind: 'error',
        }),
      );
      expect(mockShowToast).not.toHaveBeenCalledWith(
        expect.objectContaining({ message: 'Recommendations refreshed' }),
      );
      expect(api.getRecommendations).toHaveBeenCalledTimes(1);
    });

    test('dedup — concurrent stale loads fire refreshRecommendations only once', async () => {
      mockGetRecs();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
        last_collected_at: null,
        last_collection_error: null,
      });

      // A pending promise that won't resolve until we call resolveRefresh().
      let resolveRefresh!: () => void;
      const pendingRefresh = new Promise<void>((r) => { resolveRefresh = r; });
      (recsApi.refreshRecommendations as jest.Mock).mockReturnValue(pendingRefresh);

      // Fire two stale loads without awaiting the first refresh to settle.
      const first = loadRecommendations();
      const second = loadRecommendations();

      await first;
      await second;
      // Flush the microtasks that kick off triggerAutoRefreshIfStale.
      await Promise.resolve();
      await Promise.resolve();

      // Only one API call despite two stale-load triggers.
      expect(recsApi.refreshRecommendations).toHaveBeenCalledTimes(1);

      // Clean up: resolve the pending promise so no timers hang after the test.
      resolveRefresh();
      await pendingRefresh;
    });

    test('warns how many GCP recommendations the last collection skipped (#1472)', async () => {
      mockGetRecs();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
        last_collected_at: new Date().toISOString(),
        last_collection_error: null,
        last_collection_partial: {
          providers: [{
            provider: 'gcp', incomplete_accounts: 0, attempted_scopes: 0, failed_scopes: 0,
            failed_details: 0, skipped_recommendations: 3, reasons: {},
          }],
        },
      });

      await loadRecommendations();
      await Promise.resolve();
      await Promise.resolve();

      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({ kind: 'warning', message: expect.stringContaining('3 GCP recommendation(s) skipped') }),
      );
    });
  });
});
