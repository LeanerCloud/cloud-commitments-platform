/**
 * Recommendations module tests: variant selection, effective savings and effective percent tests.
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

// Issue #224: at most one (term, payment) variant per physical-resource
// cell can be selected at any time. After PR #195's per-cell fan-out (2
// terms × 3 payments per cell), naive selection produces wrong purchase
// intent — manual checking lets the user accumulate sibling commitments,
// and `select-all` over-commits 6×. Cell = `(provider, account, service,
// region, resource_type, engine)`. The fix lives in two places: the
// per-row checkbox change handler (deselect any in-cell sibling on check)
// and the select-all handler (group by cell, pick highest-effective per).
describe('Issue #224: one-variant-per-cell radio selection', () => {
  beforeEach(() => {
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
    document.body.appendChild(purchaseModal);
    jest.clearAllMocks();
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
  });

  // (a) Manual toggle: two variants of the same cell. Checking variant B
  // when variant A is already selected must remove A first, leaving only B.
  test('(a) checking variant B in same cell deselects sibling variant A', async () => {
    const recs = [
      { id: 'cellA-1y-allup',  provider: 'aws', cloud_account_id: 'acct-1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', engine: '', count: 1, term: 1, savings: 100, upfront_cost: 500 },
      { id: 'cellA-3y-noup',   provider: 'aws', cloud_account_id: 'acct-1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', engine: '', count: 1, term: 3, savings: 200, upfront_cost: 0 },
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    // state.getRecommendations() is the full loaded set (used by the sibling-eviction loop).
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    // Pretend variant A is already selected (the "user previously checked it" state).
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['cellA-1y-allup']));
    await loadRecommendations();

    // issues #225/#226: multi-variant cells are collapsed by default.
    // Expand the cell first so the variant checkboxes are rendered.
    const chevron = document.querySelector<HTMLButtonElement>('.rec-cell-chevron');
    expect(chevron).not.toBeNull();
    chevron!.click();

    // Tick variant B in the same cell.
    const cbs = Array.from(document.querySelectorAll<HTMLInputElement>('tbody input[data-rec-id]'));
    const variantB = cbs.find((cb) => cb.dataset['recId'] === 'cellA-3y-noup');
    expect(variantB).toBeDefined();
    variantB!.checked = true;
    variantB!.dispatchEvent(new Event('change'));

    // The handler must have removed sibling A AND added B.
    const removed = (state.removeSelectedRecommendation as jest.Mock).mock.calls.map((c) => c[0]);
    const added = (state.addSelectedRecommendation as jest.Mock).mock.calls.map((c) => c[0]);
    expect(removed).toContain('cellA-1y-allup');
    expect(added).toContain('cellA-3y-noup');
    // Sanity: B was not also removed, A was not also added.
    expect(removed).not.toContain('cellA-3y-noup');
    expect(added).not.toContain('cellA-1y-allup');
  });

  // (b) Cross-cell independence: selecting a rec in cell X must not
  // affect cell Y's selection state. The radio enforcement is per-cell,
  // not global.
  test('(b) selecting in cell X does not affect cell Y selections', async () => {
    const recs = [
      { id: 'cellX-1y',  provider: 'aws', cloud_account_id: 'acct-1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', engine: '', count: 1, term: 1, savings: 100, upfront_cost: 500 },
      { id: 'cellY-1y',  provider: 'aws', cloud_account_id: 'acct-1', service: 'rds', resource_type: 'db.r5.large', region: 'us-east-1', engine: 'mysql', count: 1, term: 1, savings: 200, upfront_cost: 1000 },
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    // Pretend cellY is already selected.
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['cellY-1y']));
    await loadRecommendations();

    // Tick cellX.
    const cbs = Array.from(document.querySelectorAll<HTMLInputElement>('tbody input[data-rec-id]'));
    const cellX = cbs.find((cb) => cb.dataset['recId'] === 'cellX-1y');
    expect(cellX).toBeDefined();
    cellX!.checked = true;
    cellX!.dispatchEvent(new Event('change'));

    // cellY must NOT have been removed — cells are independent.
    const removed = (state.removeSelectedRecommendation as jest.Mock).mock.calls.map((c) => c[0]);
    expect(removed).not.toContain('cellY-1y');
    const added = (state.addSelectedRecommendation as jest.Mock).mock.calls.map((c) => c[0]);
    expect(added).toContain('cellX-1y');
  });

  // (c) Select-all collapses to one-per-cell. Three distinct cells × six
  // variants each = 18 recs. After select-all, exactly 3 should be added,
  // not 18 (one per cell). This is the headline money-impact fix.
  test('(c) select-all picks exactly one variant per cell (3 cells × 6 variants → 3 selected)', async () => {
    const cells = [
      { account: 'acct-1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', engine: '' },
      { account: 'acct-1', service: 'ec2', resource_type: 'm5.large',  region: 'us-east-1', engine: '' },
      { account: 'acct-1', service: 'rds', resource_type: 'db.r5.large', region: 'eu-west-1', engine: 'mysql' },
    ];
    const variants = [
      { term: 1, payment: 'all-upfront' },
      { term: 1, payment: 'partial-upfront' },
      { term: 1, payment: 'no-upfront' },
      { term: 3, payment: 'all-upfront' },
      { term: 3, payment: 'partial-upfront' },
      { term: 3, payment: 'no-upfront' },
    ];
    const recs: Array<Record<string, unknown>> = [];
    let i = 0;
    for (const c of cells) {
      for (const v of variants) {
        recs.push({
          id: `c${i++}`,
          provider: 'aws', cloud_account_id: c.account, service: c.service,
          resource_type: c.resource_type, region: c.region, engine: c.engine,
          count: 1, term: v.term, savings: 100 + i, upfront_cost: 500,
        });
      }
    }
    expect(recs).toHaveLength(18);

    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    await loadRecommendations();

    const selectAll = document.getElementById('select-all-recs') as HTMLInputElement;
    expect(selectAll).not.toBeNull();
    selectAll.checked = true;
    selectAll.dispatchEvent(new Event('change'));

    // Exactly 3 add calls — one per cell.
    expect((state.addSelectedRecommendation as jest.Mock).mock.calls).toHaveLength(3);
    // And clearSelectedRecommendations was called first to drop any stale state.
    expect(state.clearSelectedRecommendations).toHaveBeenCalled();
  });

  // (d) Tiebreaker: when multiple variants share a cell, select-all picks
  // the variant with the highest EFFECTIVE monthly savings (amortizing
  // upfront over term * 12 months) — NOT the highest raw `savings`.
  // Concrete example: a 3yr/all-upfront with $36000 upfront + $1200/mo
  // headline savings has effective = 1200 - 36000/36 = $200. A 1yr/no-upfront
  // `savings` is already the net monthly savings (provider-reported, post-amortization).
  // select-all picks the variant with the highest net savings per cell.
  test('(d) select-all picks highest-effective-savings (net) per cell', async () => {
    const recs = [
      // 3yr/all-upfront — net savings already $200/mo (upfront already factored in by the provider).
      { id: 'big-upfront', provider: 'aws', cloud_account_id: 'acct-1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', engine: '', count: 1, term: 3, savings: 200, upfront_cost: 36000 },
      // 1yr/no-upfront — net savings $300/mo.
      { id: 'no-upfront',  provider: 'aws', cloud_account_id: 'acct-1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', engine: '', count: 1, term: 1, savings: 300,  upfront_cost: 0 },
      // 3yr/partial-upfront — highest net savings $400/mo.
      { id: 'middle',      provider: 'aws', cloud_account_id: 'acct-1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', engine: '', count: 1, term: 3, savings: 400,  upfront_cost: 7200 },
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    await loadRecommendations();

    const selectAll = document.getElementById('select-all-recs') as HTMLInputElement;
    selectAll.checked = true;
    selectAll.dispatchEvent(new Event('change'));

    // Exactly one add call (single cell, one variant picked).
    const addCalls = (state.addSelectedRecommendation as jest.Mock).mock.calls;
    expect(addCalls).toHaveLength(1);
    // The "middle" variant has the highest net savings ($400/mo > $300 > $200) -- picked.
    expect(addCalls[0]![0]).toBe('middle');
  });
});
