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

//
// Issue #482: "All" checkbox tri-state + null-filter renders as all-checked.
//
describe('Issue #482: column filter "All" tri-state semantics', () => {
  const recs = [
    { id: 'r1', provider: 'aws',   cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
    { id: 'r2', provider: 'azure', cloud_account_id: 'a2', service: 'vm',  resource_type: 'D2s_v3',    region: 'eastus',    count: 1, term: 1, savings: 150, upfront_cost: 800 },
    { id: 'r3', provider: 'gcp',   cloud_account_id: 'a3', service: 'gce', resource_type: 'n2-standard-2', region: 'us-central1', count: 1, term: 1, savings: 200, upfront_cost: 600 },
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
  });

  test('opening a popover on an unfiltered column renders every value as checked', async () => {
    await loadRecommendations();
    document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]')!.click();
    const cbs = Array.from(document.querySelectorAll<HTMLInputElement>('.column-filter-popover .column-filter-item input[type="checkbox"]'));
    expect(cbs.length).toBe(3);
    cbs.forEach((cb) => expect(cb.checked).toBe(true));
    const allBox = document.querySelector<HTMLInputElement>('.column-filter-popover input[data-role="all"]');
    expect(allBox?.checked).toBe(true);
    expect(allBox?.indeterminate).toBe(false);
  });

  test('partial selection makes the (All) box indeterminate', async () => {
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      provider: { kind: 'set', values: ['aws'] },
    });
    await loadRecommendations();
    document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]')!.click();
    const allBox = document.querySelector<HTMLInputElement>('.column-filter-popover input[data-role="all"]');
    expect(allBox?.checked).toBe(false);
    expect(allBox?.indeterminate).toBe(true);
  });

  test('clicking (All) when it is checked unchecks every value AND persists an explicit empty allow-list', async () => {
    await loadRecommendations();
    document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]')!.click();
    const allBox = document.querySelector<HTMLInputElement>('.column-filter-popover input[data-role="all"]');
    expect(allBox?.checked).toBe(true);
    // Browser flips → unchecked on click of an already-checked tri-state.
    allBox!.checked = false;
    allBox!.dispatchEvent(new Event('change'));
    expect(state.setRecommendationsColumnFilter).toHaveBeenCalledWith('provider', { kind: 'set', values: [] });
  });

  test('clicking (All) when it is unchecked checks every value AND clears the filter (null)', async () => {
    // Start from a partial selection so the table still renders (any row
    // passes — provider 'aws' is in the allow-list). We rely on the
    // popover for THIS column showing only the aws box checked and the
    // azure/gcp boxes unchecked → (All) tri-state is unchecked-not-
    // indeterminate-not-checked, but since 1 of 3 is checked it's
    // indeterminate. To exercise the explicit "(All) unchecked → click"
    // path we set a second column's filter to narrow the visible set
    // while leaving provider's checkboxes all-unchecked. Easier: use a
    // {set, values: []} filter on a DIFFERENT column so the provider
    // popover renders with no provider-narrowing applied (all checked).
    // Then directly UNCHECK the (All) box to take it from checked → empty.
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      // narrowing on service so the table is not empty when provider
      // has no explicit filter
      service: { kind: 'set', values: ['ec2', 'vm', 'gce'] },
    });
    await loadRecommendations();
    document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]')!.click();
    const allBox = document.querySelector<HTMLInputElement>('.column-filter-popover input[data-role="all"]');
    // No provider filter → resync shows everything ticked.
    expect(allBox?.checked).toBe(true);
    // Browser flips → unchecked on click of a checked tri-state.
    allBox!.checked = false;
    allBox!.dispatchEvent(new Event('change'));
    expect(state.setRecommendationsColumnFilter).toHaveBeenCalledWith('provider', { kind: 'set', values: [] });
    // Now flip it back — (All) unchecked → checked path. After the previous
    // commitAll's rerender, the mock setRecommendationsColumnFilter has
    // updated state, but jest mocks don't propagate writes; the popover
    // would still render based on the still-mocked getRecommendationsColumnFilters
    // return value. Simulate the intended state explicitly.
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      provider: { kind: 'set', values: [] },
      service: { kind: 'set', values: ['ec2', 'vm', 'gce'] },
    });
    // Re-open the popover by toggling the trigger twice; without this the
    // popover keeps the previous resync's checkbox states.
    document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]')!.click(); // close
    document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]')!.click(); // reopen
    const allBox2 = document.querySelector<HTMLInputElement>('.column-filter-popover input[data-role="all"]');
    expect(allBox2?.checked).toBe(false);
    expect(allBox2?.indeterminate).toBe(false);
    allBox2!.checked = true;
    allBox2!.dispatchEvent(new Event('change'));
    expect(state.setRecommendationsColumnFilter).toHaveBeenLastCalledWith('provider', null);
  });

  test('checking every individual value collapses to null (the existing "all selected = no narrowing" rule)', async () => {
    // Start narrowed to 2 of 3 (aws, azure); checking the 3rd (gcp) box
    // takes us to all-3 → commit() collapses to null. One toggle keeps
    // the mock state interaction trivial — see Bundle B's notes about
    // resync re-reading the (mocked) filter after each commit.
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      provider: { kind: 'set', values: ['aws', 'azure'] },
    });
    await loadRecommendations();
    document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]')!.click();
    const gcpCb = document.querySelector<HTMLInputElement>('.column-filter-popover input[data-value="gcp"]')!;
    expect(gcpCb.checked).toBe(false);
    gcpCb.checked = true;
    gcpCb.dispatchEvent(new Event('change'));
    // All 3 now checked → commit collapses to null.
    expect(state.setRecommendationsColumnFilter).toHaveBeenLastCalledWith('provider', null);
  });

  // CR pass #2 regression: unchecking the last individual value used to
  // collapse to `null` (no filter), which silently snapped the popover
  // back to all-checked. After the fix, the individual-checkbox commit
  // mirrors the (All) / Clear path: an empty allow-list is persisted, so
  // the table renders 0 rows just like Clear.
  test('unchecking every individual value persists an explicit empty allow-list (not null)', async () => {
    // Start narrowed to a single value so the popover opens with exactly
    // one checkbox checked. Then untick it — commit() should produce
    // {kind: 'set', values: []}, NOT null.
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      provider: { kind: 'set', values: ['aws'] },
    });
    await loadRecommendations();
    document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]')!.click();
    const awsCb = document.querySelector<HTMLInputElement>('.column-filter-popover input[data-value="aws"]')!;
    expect(awsCb.checked).toBe(true);
    awsCb.checked = false;
    awsCb.dispatchEvent(new Event('change'));
    // Zero of 3 checked — must NOT collapse to null.
    expect(state.setRecommendationsColumnFilter).toHaveBeenLastCalledWith('provider', { kind: 'set', values: [] });
    // Sanity: the call site MUST be the {set, values: []} branch, not the
    // null branch. Guards against future refactors that re-introduce the
    // `selected.length === 0 → null` shortcut.
    const calls = (state.setRecommendationsColumnFilter as jest.Mock).mock.calls
      .filter((c) => c[0] === 'provider');
    const last = calls[calls.length - 1];
    expect(last[1]).not.toBeNull();
    expect(last[1]).toEqual({ kind: 'set', values: [] });
  });

  // CR pass #2 regression: with the empty allow-list persisted, applying
  // the filter to the recommendations must render zero rows — i.e. the
  // unchecking-last-value path reaches the same 0-row terminal state as
  // the (All) / Clear path.
  test('an empty allow-list filter renders 0 rows (applyColumnFilters returns [])', async () => {
    const { applyColumnFilters } = await import('../recommendations');
    const filtered = applyColumnFilters(
      recs as unknown as Parameters<typeof applyColumnFilters>[0],
      { provider: { kind: 'set', values: [] } },
    );
    expect(filtered).toEqual([]);
  });
});

//
// Issue #483: Scrolling inside the popover does NOT dismiss it.
//
describe('Issue #483: popover stays open while user scrolls its contents', () => {
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
  });

  test('a scroll event whose target is inside the popover does not dismiss it', async () => {
    await loadRecommendations();
    document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="resource_type"]')!.click();
    const popover = document.querySelector('.column-filter-popover');
    expect(popover).not.toBeNull();
    const list = popover!.querySelector('.column-filter-list');
    expect(list).not.toBeNull();
    // Simulate the user scrolling inside the popover's list. The capture-
    // phase scroll listener on window used to fire and close the popover;
    // it now ignores scrolls whose target is inside openPopover.el.
    const scrollEvt = new Event('scroll', { bubbles: true });
    list!.dispatchEvent(scrollEvt);
    expect(document.querySelector('.column-filter-popover')).not.toBeNull();
  });
});
