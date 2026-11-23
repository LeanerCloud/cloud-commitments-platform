/**
 * Recommendations module tests: column filter All semantics, popover scrolling and numeric filter display tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, setupOpportunitiesTabDom } from './recommendations-harness';
import { loadRecommendations } from '../recommendations';
import type { CostPeriod } from '../state';
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

  test('a scroll event outside the popover still dismisses it', async () => {
    await loadRecommendations();
    document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="resource_type"]')!.click();
    expect(document.querySelector('.column-filter-popover')).not.toBeNull();
    // Scroll the document body (outside the popover) — should still close.
    const scrollEvt = new Event('scroll', { bubbles: true });
    document.body.dispatchEvent(scrollEvt);
    expect(document.querySelector('.column-filter-popover')).toBeNull();
  });

  test('a commit that re-renders the table keeps the new trigger expanded', async () => {
    await loadRecommendations();
    const oldTrigger = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="resource_type"]')!;
    oldTrigger.click();
    const popover = document.querySelector<HTMLElement>('.column-filter-popover')!;
    const box = popover.querySelector<HTMLInputElement>('input[data-value="t3.medium"]')!;
    box.checked = false;
    box.dispatchEvent(new Event('change', { bubbles: true }));
    const newTrigger = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="resource_type"]')!;
    expect(newTrigger).not.toBe(oldTrigger);
    expect(document.querySelector('.column-filter-popover')).toBe(popover);
    expect(newTrigger.getAttribute('aria-expanded')).toBe('true');
  });

  test('a scroll while the trigger stays on screen keeps the popover open and re-anchors it', async () => {
    // Clicking a funnel at the table scroller's clipped edge scrolls the
    // table (and the mirrored strip a frame later); those scroll events
    // must not close the popover the click just opened.
    await loadRecommendations();
    const trigger = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="resource_type"]')!;
    let left = 400;
    trigger.getBoundingClientRect = () => ({
      left, top: 300, width: 24, height: 24, right: left + 24, bottom: 324, x: left, y: 300, toJSON: () => ({}),
    }) as DOMRect;
    trigger.click();
    const popover = document.querySelector<HTMLElement>('.column-filter-popover');
    expect(popover).not.toBeNull();
    expect(popover!.style.left).toBe('400px');
    left = 250;
    document.body.dispatchEvent(new Event('scroll', { bubbles: true }));
    expect(document.querySelector('.column-filter-popover')).toBe(popover);
    expect(popover!.style.left).toBe('250px');
  });
});

//
// Issue #484: Numeric filter exact-match against the displayed rounded value.
//
describe('Issue #484: numeric filter matches the displayed rounded value', () => {
  // Choose a savings value whose raw form rounds to a different display
  // value depending on which precision we use. Under hourly period, the
  // display rounds to PERIOD_DECIMALS.hourly (4) decimals.
  //
  // raw monthly = 123.456789 → daily = monthly/30 = 4.1152263; hourly =
  // monthly/730 = 0.169119... Use a monthly that's clean enough to keep
  // the assertions readable while still distinguishing exact rounding.
  const recs = [
    { id: 'r-exact',   provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 123.456789, upfront_cost: 500 },
    { id: 'r-other',   provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, savings: 250,        upfront_cost: 800 },
  ];

  beforeEach(() => {
    setupOpportunitiesTabDom();
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
      last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      last_collection_error: null,
    });
  });

  test('typing the rounded-display value of a row matches that row at daily period', async () => {
    // Under daily period (PERIOD_DECIMALS.daily = 2), raw monthly
    // 123.456789 / 30 = 4.1152263 → displays as "$4.12" (2 dp). Filter
    // expression "4.12" must match the row even though the raw scaled
    // value (4.1152263) doesn't equal 4.12.
    (state.getCostPeriod as jest.Mock).mockReturnValue('daily');
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      savings: { kind: 'expr', expr: '4.12' },
    });
    const { applyColumnFilters } = await import('../recommendations');
    // Cast through unknown to match the LocalRecommendation shape used
    // by the function; the test recs include all the fields the filter
    // actually reads (savings).
    const filtered = applyColumnFilters(recs as unknown as Parameters<typeof applyColumnFilters>[0], state.getRecommendationsColumnFilters());
    expect(filtered.map((r) => r.id)).toEqual(['r-exact']);
  });

  test('typing a value that does NOT match the rounded display excludes the row', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('daily');
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      savings: { kind: 'expr', expr: '4.11' },
    });
    const { applyColumnFilters } = await import('../recommendations');
    const filtered = applyColumnFilters(recs as unknown as Parameters<typeof applyColumnFilters>[0], state.getRecommendationsColumnFilters());
    expect(filtered.map((r) => r.id)).toEqual([]);
  });

  test('comparison operators apply against the rounded value too (>5 with a raw 4.999 stays excluded)', async () => {
    // Raw monthly 4.999 rounds to 5.00 at monthly period (0 dp → 5).
    // Under "monthly" period the display precision for savings is 0
    // decimals, so 4.999 displays as "$5" → ">4" includes it, ">5"
    // does not (since rounded = 5 and 5 > 5 is false).
    const sub = [
      { id: 'r-near-5', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 4.999, upfront_cost: 0 },
    ];
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      savings: { kind: 'expr', expr: '>4' },
    });
    const { applyColumnFilters } = await import('../recommendations');
    expect(applyColumnFilters(sub as unknown as Parameters<typeof applyColumnFilters>[0], state.getRecommendationsColumnFilters()).map((r) => r.id)).toEqual(['r-near-5']);

    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      savings: { kind: 'expr', expr: '>5' },
    });
    expect(applyColumnFilters(sub as unknown as Parameters<typeof applyColumnFilters>[0], state.getRecommendationsColumnFilters()).map((r) => r.id)).toEqual([]);
  });

  test('integer columns continue to match exact integer values', async () => {
    const sub = [
      { id: 'r-5',  provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 5,  term: 1, savings: 100, upfront_cost: 0 },
      { id: 'r-10', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 10, term: 1, savings: 100, upfront_cost: 0 },
    ];
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      count: { kind: 'expr', expr: '5' },
    });
    const { applyColumnFilters } = await import('../recommendations');
    expect(applyColumnFilters(sub as unknown as Parameters<typeof applyColumnFilters>[0], state.getRecommendationsColumnFilters()).map((r) => r.id)).toEqual(['r-5']);
  });

  // CR pass #2 regression: `displayPrecision` for currency columns MUST
  // agree with the fraction-digit count `formatCurrency` actually renders,
  // so that an exact-match filter on the displayed string ("$123") matches
  // the row whose underlying value rounds to that string at the active
  // period. Both sources must agree, so the test pins the symmetry at the
  // canonical `CURRENCY_DEFAULT_DIGITS` constant — which formatCurrency
  // uses as its default and displayPrecision now imports — rather than at
  // a hard-coded literal that would silently drift if the default ever
  // changes.
  describe('displayPrecision agrees with formatCurrency for currency columns', () => {
    function fractionDigitsOf(s: string): number {
      // Strip the leading currency symbol and formatCurrency's en-US comma
      // group separators (#1728), then count digits after a decimal point.
      // Returns 0 for "$123", 2 for "$123.45", etc.
      const stripped = s.replace(/[^0-9.]/g, '');
      const dot = stripped.indexOf('.');
      return dot < 0 ? 0 : stripped.length - dot - 1;
    }

    test('upfront_cost: displayPrecision matches formatCurrency at every period', async () => {
      const recsMod = await import('../recommendations');
      // utils is jest.mock()'d at the top of this file (so the
      // recommendations module sees a stubbed formatCurrency). For this
      // test we want the REAL formatCurrency to assert the fraction-digit
      // contract holds against actual production behaviour, so we pull it
      // via jest.requireActual rather than the mocked import.
      const utilsActual = jest.requireActual<typeof import('../utils')>('../utils');
      const displayPrecision = recsMod.displayPrecision;
      const formatCurrency = utilsActual.formatCurrency;
      // upfront_cost is always rendered via plain formatCurrency (no
      // period scaling). The fraction digit count must therefore be
      // period-independent and equal to formatCurrency's own output.
      const sample = 123.456789;
      const renderedDigits = fractionDigitsOf(formatCurrency(sample));
      const periods: CostPeriod[] = ['hourly', 'daily', 'monthly', 'yearly'];
      for (const period of periods) {
        expect(displayPrecision('upfront_cost', period)).toBe(renderedDigits);
      }
    });

    test('monthly_cost: displayPrecision at monthly period matches formatCurrency', async () => {
      const recsMod = await import('../recommendations');
      // utils is jest.mock()'d at the top of this file (so the
      // recommendations module sees a stubbed formatCurrency). For this
      // test we want the REAL formatCurrency to assert the fraction-digit
      // contract holds against actual production behaviour, so we pull it
      // via jest.requireActual rather than the mocked import.
      const utilsActual = jest.requireActual<typeof import('../utils')>('../utils');
      const displayPrecision = recsMod.displayPrecision;
      const formatCurrency = utilsActual.formatCurrency;
      const sample = 123.456789;
      const renderedDigits = fractionDigitsOf(formatCurrency(sample));
      // At monthly period, formatCostForPeriod delegates to formatCurrency
      // → displayPrecision must agree with formatCurrency's digit count.
      expect(displayPrecision('monthly_cost', 'monthly')).toBe(renderedDigits);
    });
  });
});
