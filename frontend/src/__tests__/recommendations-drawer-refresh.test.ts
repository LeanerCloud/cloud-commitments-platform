/**
 * Recommendations module tests: the rest of the detail drawer tests and the first auto-refresh tests.
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
    });
  });
});
