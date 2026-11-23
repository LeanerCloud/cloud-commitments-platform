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

  test('Clear button on a numeric column clears the expression filter (null)', async () => {
    // Numeric columns still use null on Clear: the empty-set semantic
    // only applies to categorical filters because the set-membership
    // model maps naturally to "no values allowed". Numeric "clear" just
    // means "no expression applied".
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      savings: { kind: 'expr', expr: '>100' },
    });
    await loadRecommendations();
    const savingsBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="savings"]');
    savingsBtn?.click();
    const clearBtn = document.querySelector<HTMLButtonElement>('.column-filter-popover .column-filter-clear');
    clearBtn?.click();
    expect(state.setRecommendationsColumnFilter).toHaveBeenCalledWith('savings', null);
  });

  test('Clear-filters badge appears when at least one filter is active', async () => {
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      provider: { kind: 'set', values: ['aws'] },
    });
    await loadRecommendations();
    const badge = document.querySelector<HTMLButtonElement>('.recommendations-filter-status .clear-filters');
    expect(badge).not.toBeNull();
    expect(badge?.textContent).toContain('Clear filters (1)');
  });

  test('aria-live region announces visible/loaded count', async () => {
    await loadRecommendations();
    const live = document.querySelector('.recommendations-filter-live');
    expect(live).not.toBeNull();
    expect(live?.getAttribute('aria-live')).toBe('polite');
    expect(live?.textContent).toMatch(/Showing \d+ of \d+/);
  });

  // Issue #137: 'All Savings Plans' affordance in the service column-filter
  // popover. PR #123 split a single 'savings-plans' service into four per-
  // plan-type slugs, so the user lost the one-click "filter to all SP recs"
  // affordance. These tests pin the new tri-state group toggle.
  describe('Issue #137: All Savings Plans tri-state in service column-filter', () => {
    const spRecs = [
      { id: 'rec-aws-1',  provider: 'aws', cloud_account_id: 'a1', service: 'ec2',                        resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
      { id: 'rec-sp-c',   provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-compute',      resource_type: 'sp',        region: 'us-east-1', count: 1, term: 1, savings: 200, upfront_cost: 800 },
      { id: 'rec-sp-e',   provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-ec2instance',  resource_type: 'sp',        region: 'us-east-1', count: 1, term: 1, savings: 150, upfront_cost: 700 },
      { id: 'rec-sp-s',   provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-sagemaker',    resource_type: 'sp',        region: 'us-east-1', count: 1, term: 1, savings: 250, upfront_cost: 900 },
    ];

    beforeEach(() => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: spRecs,
        regions: [],
      });
      (state.getRecommendations as jest.Mock).mockReturnValue(spRecs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(spRecs);
      // Reset the column-filters mock to a deterministic empty state so a
      // prior test in this describe block leaving a custom mockReturnValue
      // (e.g. the indeterminate test setting `service: { values: [...] }`)
      // doesn't bleed into a later test's popover-build that reads
      // getRecommendationsColumnFilters() during resync.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    });

    test('service popover renders the All Savings Plans group toggle when 2+ SP slugs present', async () => {
      await loadRecommendations();
      const serviceBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="service"]');
      serviceBtn?.click();
      const groupBox = document.querySelector<HTMLInputElement>('.column-filter-popover input[data-role="sp-group"]');
      expect(groupBox).not.toBeNull();
      const groupLabel = groupBox?.closest('label');
      expect(groupLabel?.textContent).toContain('All Savings Plans');
    });

    test('group toggle does NOT render for non-service columns', async () => {
      await loadRecommendations();
      const providerBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]');
      providerBtn?.click();
      const groupBox = document.querySelector('.column-filter-popover input[data-role="sp-group"]');
      expect(groupBox).toBeNull();
    });

    test('group toggle does NOT render when only 0 or 1 SP slugs present', async () => {
      const oneSPRec = [
        { id: 'rec1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',                   resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
        { id: 'rec2', provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-compute', resource_type: 'sp',        region: 'us-east-1', count: 1, term: 1, savings: 200, upfront_cost: 800 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: oneSPRec, regions: [] });
      (state.getRecommendations as jest.Mock).mockReturnValue(oneSPRec);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(oneSPRec);

      await loadRecommendations();
      const serviceBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="service"]');
      serviceBtn?.click();
      const groupBox = document.querySelector('.column-filter-popover input[data-role="sp-group"]');
      expect(groupBox).toBeNull();
    });

    test('clicking group toggle from a non-SP filter expands it to include all SP slug values', async () => {
      // Issue #482: with no active filter, every checkbox renders as
      // checked (including the SP-group toggle). To exercise the "tick
      // the SP group to add SPs" flow, start from a filter that already
      // restricts service to a non-SP subset.
      //
      // Use a 5-distinct-value visible set (ec2, rds, + 3 SPs) so adding
      // SPs lands at 4-of-5 selected, strictly partial, so the commit
      // does NOT collapse to the "all selected" null state.
      const widerRecs = [
        { id: 'rec-ec2', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',                        resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
        { id: 'rec-rds', provider: 'aws', cloud_account_id: 'a1', service: 'rds',                        resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, savings: 120, upfront_cost: 600 },
        { id: 'rec-spc', provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-compute',      resource_type: 'sp',        region: 'us-east-1', count: 1, term: 1, savings: 200, upfront_cost: 800 },
        { id: 'rec-spe', provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-ec2instance',  resource_type: 'sp',        region: 'us-east-1', count: 1, term: 1, savings: 150, upfront_cost: 700 },
        { id: 'rec-sps', provider: 'aws', cloud_account_id: 'a1', service: 'savings-plans-sagemaker',    resource_type: 'sp',        region: 'us-east-1', count: 1, term: 1, savings: 250, upfront_cost: 900 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: widerRecs, regions: [] });
      (state.getRecommendations as jest.Mock).mockReturnValue(widerRecs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(widerRecs);
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        service: { kind: 'set', values: ['ec2'] },
      });
      await loadRecommendations();
      const serviceBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="service"]');
      serviceBtn?.click();
      const groupBox = document.querySelector<HTMLInputElement>('.column-filter-popover input[data-role="sp-group"]');
      expect(groupBox).not.toBeNull();
      expect(groupBox?.checked).toBe(false);
      // Browser flips checked → true on first click; simulate that.
      groupBox!.checked = true;
      groupBox!.dispatchEvent(new Event('change'));

      // Filter committed with ec2 + the three SP values (in any order).
      // Total selected = 4 of 5 distinct, so the commit stays as a set.
      const calls = (state.setRecommendationsColumnFilter as jest.Mock).mock.calls;
      const lastCall = calls[calls.length - 1];
      expect(lastCall[0]).toBe('service');
      expect(lastCall[1]?.kind).toBe('set');
      expect((lastCall[1]?.values as string[]).sort()).toEqual([
        'ec2',
        'savings-plans-compute',
        'savings-plans-ec2instance',
        'savings-plans-sagemaker',
      ]);
    });

    test('clicking group toggle (off) persists an empty allow-list (0-row state)', async () => {
      // Pre-set the filter so the SP tri-state renders as checked.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        service: { kind: 'set', values: ['savings-plans-compute', 'savings-plans-ec2instance', 'savings-plans-sagemaker'] },
      });
      await loadRecommendations();
      const serviceBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="service"]');
      serviceBtn?.click();
      const groupBox = document.querySelector<HTMLInputElement>('.column-filter-popover input[data-role="sp-group"]');
      expect(groupBox?.checked).toBe(true);
      // Browser flips → unchecked on click of an already-checked tri-state.
      groupBox!.checked = false;
      groupBox!.dispatchEvent(new Event('change'));

      // CR pass #2: unchecking the SP group when no non-SP boxes are
      // currently selected leaves zero checkboxes ticked. The individual
      // commit() path now persists that as an explicit empty allow-list
      // (matching (All)-off / Clear), not null — so the table renders 0
      // rows rather than silently snapping back to "no narrowing applied".
      expect(state.setRecommendationsColumnFilter).toHaveBeenLastCalledWith('service', { kind: 'set', values: [] });
    });

    test('group toggle resyncs to indeterminate when only some SPs are filter-active', async () => {
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        service: { kind: 'set', values: ['savings-plans-compute'] },
      });
      await loadRecommendations();
      const serviceBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="service"]');
      serviceBtn?.click();
      const groupBox = document.querySelector<HTMLInputElement>('.column-filter-popover input[data-role="sp-group"]');
      expect(groupBox?.checked).toBe(false);
      expect(groupBox?.indeterminate).toBe(true);
    });
  });
});
