/**
 * Recommendations module tests: column header filter trigger tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import { loadRecommendations } from '../recommendations';
import * as api from '../api';
import * as state from '../state';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

//
// Bundle B: column-filter popover + sticky bottom action box DOM behaviour.
// These tests assert the surfaces Bundle B introduced — header filter
// triggers, the detached popover lifecycle, and the bottom action box's
// label/disabled-state transitions.
//

describe('Bundle B: column header filter triggers', () => {
  const sampleRecs = [
    { id: 'rec-aws-1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
    { id: 'rec-az-1',  provider: 'azure', cloud_account_id: 'a2', service: 'vm',  resource_type: 'D2s',       region: 'eastus',    count: 2, term: 3, savings: 200, upfront_cost: 800 },
  ];

  beforeEach(() => {
    // Set up DOM (the top-level beforeEach belongs to a different describe).
    document.body.replaceChildren();
    const recsTab = document.createElement('div');
    recsTab.id = 'opportunities-tab';
    recsTab.className = 'tab-content active';
    const summary = document.createElement('div');
    summary.id = 'recommendations-summary';
    const list = document.createElement('div');
    list.id = 'recommendations-list';
    recsTab.appendChild(summary);
    recsTab.appendChild(list);
    document.body.appendChild(recsTab);
    const purchaseModal = document.createElement('div');
    purchaseModal.id = 'purchase-modal';
    purchaseModal.className = 'hidden';
    const purchaseDetails = document.createElement('div');
    purchaseDetails.id = 'purchase-details';
    purchaseModal.appendChild(purchaseDetails);
    document.body.appendChild(purchaseModal);

    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: sampleRecs,
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue(sampleRecs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(sampleRecs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
  });

  test('Clear button on a categorical column commits an explicit empty allow-list', async () => {
    // Issue #482: Clear is distinct from "All". It represents the user's
    // explicit "no values selected" intent and now persists as
    // {set, values: []} so the table shows 0 rows. Previously Clear
    // and All collapsed to the same null state, making them visually
    // indistinguishable per the bug report.
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      provider: { kind: 'set', values: ['aws'] },
    });
    await loadRecommendations();
    const providerBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]');
    providerBtn?.click();
    const clearBtn = document.querySelector<HTMLButtonElement>('.column-filter-popover .column-filter-clear');
    clearBtn?.click();
    expect(state.setRecommendationsColumnFilter).toHaveBeenCalledWith('provider', { kind: 'set', values: [] });
  });

  test('Issue #700: Clear resets (All) checkbox to unchecked (not indeterminate)', async () => {
    // Bug: the old Clear branch set cb.checked = false on individual boxes but
    // never called updateAllTriState(), so the (All) checkbox kept its prior
    // state (checked or indeterminate). After the fix, commitAllRef(false) is
    // used which calls updateAllTriState() and leaves (All) unchecked.
    //
    // Simulate real state-store behaviour: setRecommendationsColumnFilter
    // updates the store so the next getRecommendationsColumnFilters() call
    // sees the cleared filter. Without this the resyncOpenPopover() call
    // triggered by the rerender would re-apply the stale filter and overwrite
    // the tri-state that updateAllTriState() just set.
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      provider: { kind: 'set', values: ['aws'] },
    });
    (state.setRecommendationsColumnFilter as jest.Mock).mockImplementation(
      (col: string, val: unknown) => {
        (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue(
          val === null ? {} : { [col]: val },
        );
      },
    );
    await loadRecommendations();
    const providerBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]');
    providerBtn?.click();

    // (All) starts in a known state before Clear.
    const allBox = document.querySelector<HTMLInputElement>('.column-filter-popover .column-filter-all input[type="checkbox"]');
    expect(allBox).not.toBeNull();

    const clearBtn = document.querySelector<HTMLButtonElement>('.column-filter-popover .column-filter-clear');
    clearBtn?.click();

    // After Clear: (All) must be unchecked and not indeterminate.
    expect(allBox!.checked).toBe(false);
    expect(allBox!.indeterminate).toBe(false);
    expect(state.setRecommendationsColumnFilter).toHaveBeenCalledWith('provider', { kind: 'set', values: [] });
  });

  test('Issue #700: table <thead> survives a filter that yields zero rows', async () => {
    // Bug: renderRecommendationsList replaced the entire <table> with a <p>
    // when no rows matched, removing <thead>. After the fix, the header row
    // remains visible (with an empty <tbody> + hint cell) so columns are still
    // readable while the user adjusts filters.
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      provider: { kind: 'set', values: [] },
    });
    await loadRecommendations();

    const container = document.getElementById('recommendations-list');
    expect(container?.querySelector('thead')).not.toBeNull();
    // The empty hint cell must be present inside the table body.
    const emptyCell = container?.querySelector('tbody td.empty');
    expect(emptyCell).not.toBeNull();
    expect(emptyCell?.textContent).toMatch(/No rows match/);
    // No standalone <p class="empty"> should replace the table.
    expect(container?.querySelector('p.empty')).toBeNull();
  });
});
