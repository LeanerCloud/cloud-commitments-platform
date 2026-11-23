/**
 * Recommendations module tests: the rest of the detail drawer tests and the first auto-refresh tests.
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
import * as state from '../state';
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

  describe('P6: sort + bulk toolbar + detail drawer', () => {
    const twoRecs = [
      { id: 'rec-14', provider: 'aws', service: 'ec2', resource_type: 't3.large', region: 'us-east-1', count: 2, term: 1, savings: 100, upfront_cost: 500 },
      { id: 'rec-15', provider: 'aws', service: 'rds', resource_type: 'db.m5.large', region: 'us-east-1', count: 4, term: 3, savings: 1500, upfront_cost: 9000 },
    ];

    beforeEach(() => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        recommendations: twoRecs,
        summary: {},
        available_regions: [],
      });
    });

    // Issue #344 T4': row-click toggles selection. The previous
    // row-click → openDetailDrawer behaviour was dropped (per plan.md
    // §T4) — the drawer payload duplicated the table, with
    // backend-deferred fields the only differentiators. Tests below
    // cover the new selection-toggle contract.
    //
    // The shared `wireSelection` helper hooks the state mocks to a real
    // Set so addSelectedRecommendation actually surfaces in subsequent
    // getSelectedRecommendationIDs reads — without this, re-renders see
    // an empty selection and the row checkbox renders unchecked.
    describe('row-click selection (T4′)', () => {
      function wireSelection(): Set<string> {
        const selected = new Set<string>();
        (state.getSelectedRecommendationIDs as jest.Mock).mockImplementation(() => new Set(selected));
        (state.addSelectedRecommendation as jest.Mock).mockImplementation((id: string) => { selected.add(id); });
        (state.removeSelectedRecommendation as jest.Mock).mockImplementation((id: string) => { selected.delete(id); });
        (state.clearSelectedRecommendations as jest.Mock).mockImplementation(() => { selected.clear(); });
        (state.getRecommendations as jest.Mock).mockReturnValue(twoRecs);
        (state.getVisibleRecommendations as jest.Mock).mockReturnValue(twoRecs);
        return selected;
      }

      test('clicking a non-interactive cell on a row toggles the row checkbox', async () => {
        wireSelection();
        await loadRecommendations();
        const firstRow = document.querySelector<HTMLTableRowElement>('tr.recommendation-row');
        const cb = firstRow?.querySelector<HTMLInputElement>('input[type="checkbox"][data-rec-id]');
        expect(cb).not.toBeNull();
        expect(cb!.checked).toBe(false);
        const recId = cb!.dataset['recId']!;

        // Service cell (index 3) is non-interactive — safe to click.
        firstRow?.querySelectorAll('td')[3]?.click();

        const cbAfter = document.querySelector<HTMLInputElement>(
          `tr.recommendation-row input[data-rec-id="${recId}"]`,
        );
        expect(cbAfter?.checked).toBe(true);
      });

      test('clicking a row a second time unselects it', async () => {
        wireSelection();
        await loadRecommendations();
        const firstRow = document.querySelector<HTMLTableRowElement>('tr.recommendation-row');
        const cb = firstRow?.querySelector<HTMLInputElement>('input[type="checkbox"][data-rec-id]');
        const recId = cb!.dataset['recId']!;

        firstRow?.querySelectorAll('td')[3]?.click();
        let cbAfter = document.querySelector<HTMLInputElement>(
          `tr.recommendation-row input[data-rec-id="${recId}"]`,
        );
        expect(cbAfter?.checked).toBe(true);

        // Second click on the (post-rerender) row toggles back off.
        const rerenderedRow = cbAfter!.closest('tr')!;
        rerenderedRow.querySelectorAll('td')[3]?.click();

        cbAfter = document.querySelector<HTMLInputElement>(
          `tr.recommendation-row input[data-rec-id="${recId}"]`,
        );
        expect(cbAfter?.checked).toBe(false);
      });

      test('row-click does NOT trigger when the click originates on an interactive descendant', async () => {
        wireSelection();
        await loadRecommendations();
        const firstRow = document.querySelector<HTMLTableRowElement>('tr.recommendation-row');
        const cb = firstRow?.querySelector<HTMLInputElement>('input[type="checkbox"][data-rec-id]');
        expect(cb!.checked).toBe(false);

        // Inject a synthetic action button into the first row to model
        // any inline per-row CTA (the production table doesn't currently
        // render one, but the click-filter rule must still hold).
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.textContent = 'Inline action';
        firstRow!.querySelectorAll('td')[3]!.appendChild(btn);

        btn.click();

        // Selection state must NOT change just because the click
        // bubbled from the button to the row.
        const cbAfter = document.querySelector<HTMLInputElement>(
          `tr.recommendation-row input[data-rec-id="${cb!.dataset['recId']}"]`,
        );
        expect(cbAfter?.checked).toBe(false);
      });

      test('clicking the checkbox itself does not double-toggle (native click handles it)', async () => {
        wireSelection();
        await loadRecommendations();
        const firstRow = document.querySelector<HTMLTableRowElement>('tr.recommendation-row');
        const cb = firstRow!.querySelector<HTMLInputElement>('input[type="checkbox"][data-rec-id]')!;
        expect(cb.checked).toBe(false);

        // Native click on a <input type="checkbox"> toggles checked
        // before the click event fires. Our row-click handler must skip
        // when the originating target is the checkbox so we don't
        // re-toggle it back to its prior state.
        cb.click();

        const cbAfter = document.querySelector<HTMLInputElement>(
          `tr.recommendation-row input[data-rec-id="${cb.dataset['recId']}"]`,
        );
        expect(cbAfter?.checked).toBe(true);
      });
    });
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

    test('cold cache (null last_collected_at) — refresh fires + in-flight toast shown', async () => {
      mockGetRecs();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
        last_collected_at: null,
        last_collection_error: null,
      });

      await loadRecommendations();
      // Flush microtasks so the async refreshRecommendations call runs.
      await Promise.resolve();

      expect(recsApi.refreshRecommendations).toHaveBeenCalledTimes(1);
      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'Refreshing recommendations…', kind: 'info' }),
      );
    });

    test('stale cache (>24h ago) — refresh fires + in-flight toast shown', async () => {
      mockGetRecs();
      const twentyFiveHoursAgo = new Date(Date.now() - 25 * 60 * 60 * 1000).toISOString();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
        last_collected_at: twentyFiveHoursAgo,
        last_collection_error: null,
      });

      await loadRecommendations();
      await Promise.resolve();

      expect(recsApi.refreshRecommendations).toHaveBeenCalledTimes(1);
      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'Refreshing recommendations…', kind: 'info' }),
      );
    });

    test('fresh cache (<24h ago) — refresh does NOT fire + no toast', async () => {
      mockGetRecs();
      const oneHourAgo = new Date(Date.now() - 60 * 60 * 1000).toISOString();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
        last_collected_at: oneHourAgo,
        last_collection_error: null,
      });

      await loadRecommendations();
      await Promise.resolve();

      expect(recsApi.refreshRecommendations).not.toHaveBeenCalled();
      expect(mockShowToast).not.toHaveBeenCalled();
    });

    test('cold cache + collection error — friendly toast, raw text kept out of it (#748)', async () => {
      mockGetRecs();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
        last_collected_at: null,
        last_collection_error: 'Provider X returned 403 Forbidden',
      });

      const warnSpy = jest.spyOn(console, 'warn').mockImplementation(() => undefined);
      await loadRecommendations();
      await Promise.resolve();

      expect(warnSpy).toHaveBeenCalledTimes(1);
      expect(warnSpy.mock.calls[0]).toContain('Provider X returned 403 Forbidden');
      warnSpy.mockRestore();
      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({
          message: 'The last recommendations collection had errors. Refreshing may resolve it.',
          kind: 'error',
        }),
      );
      for (const [arg] of mockShowToast.mock.calls) {
        expect((arg as { message: string }).message).not.toContain('Provider X');
      }
    });

    test('refresh failure — error toast shown with message', async () => {
      mockGetRecs();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
        last_collected_at: null,
        last_collection_error: null,
      });
      (recsApi.refreshRecommendations as jest.Mock).mockRejectedValue(new Error('Network timeout'));

      await loadRecommendations();
      // Two microtask ticks: one for the freshness call, one for the refresh rejection.
      await Promise.resolve();
      await Promise.resolve();

      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({
          message: 'Recommendations refresh failed: Network timeout',
          kind: 'error',
        }),
      );
    });
  });
});
