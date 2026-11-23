/**
 * Recommendations module tests: variant selection, effective savings and effective percent tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import {
  loadRecommendations,
  pickBestVariantPerCell,
  seedGlobalDefaults,
  effectiveMonthlySavings,
  effectiveSavingsPct,
} from '../recommendations';
import type { LocalRecommendation } from '../types';
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

//
// issue #223: default-seed from GlobalConfig across all 3 surfaces.
// These tests exercise the pickBestVariantPerCell config-match tiebreaker
// and the seedGlobalDefaults hook that injects resolved GlobalConfig values.
//

describe('issue #223: pickBestVariantPerCell config-match tiebreaker', () => {
  const rec = (
    id: string,
    term: 1 | 3,
    payment: string,
    savings = 100,
    upfront_cost = 0,
  ) => ({
    id,
    provider: 'aws' as const,
    cloud_account_id: 'acct-1',
    service: 'ec2',
    resource_type: 't3.medium',
    region: 'us-east-1',
    engine: '',
    count: 1,
    term,
    payment,
    savings,
    upfront_cost,
  } as unknown as LocalRecommendation);

  afterEach(() => {
    // Reset module cache to initial defaults so tests don't bleed into each other.
    seedGlobalDefaults(1, 'all-upfront');
  });

  test('prefers variant matching configured (term, payment) over highest-effective', () => {
    // Two variants in one cell: 1yr/all-upfront (configured default) vs
    // 3yr/no-upfront (higher effective savings).
    const recs = [
      rec('want-this', 1, 'all-upfront', 300, 0), // effective = $300/mo
      rec('skip-this', 3, 'no-upfront', 400, 0), // effective = $400/mo (higher)
    ];
    seedGlobalDefaults(1, 'all-upfront');
    const result = pickBestVariantPerCell(recs);
    expect(result).toHaveLength(1);
    expect(result[0]!.id).toBe('want-this');
  });

  test('falls back to highest-effective when no variant matches configured defaults', () => {
    // Neither variant matches term=1/all-upfront; fallback picks highest net savings.
    const recs = [
      rec('low-effective', 3, 'all-upfront', 200, 36000), // net savings = $200/mo
      rec('high-effective', 3, 'no-upfront', 400, 0), // net savings = $400/mo
    ];
    seedGlobalDefaults(1, 'all-upfront'); // neither matches
    const result = pickBestVariantPerCell(recs);
    expect(result).toHaveLength(1);
    expect(result[0]!.id).toBe('high-effective');
  });

  test('handles multiple independent cells and picks config-match in each', () => {
    // Two distinct cells; each has a variant matching configured defaults.
    const recs = [
      rec('cell-a-match', 1, 'all-upfront', 100, 0),
      rec('cell-a-other', 3, 'no-upfront', 400, 0), // higher effective but different cell
      { ...rec('cell-b-match', 1, 'all-upfront', 200, 0), region: 'eu-west-1', id: 'cell-b-match' },
      { ...rec('cell-b-other', 3, 'partial-upfront', 600, 0), region: 'eu-west-1', id: 'cell-b-other' },
    ];
    seedGlobalDefaults(1, 'all-upfront');
    const result = pickBestVariantPerCell(recs);
    const ids = result.map((r) => r.id).sort();
    expect(ids).toEqual(['cell-a-match', 'cell-b-match']);
  });

  test('config-match with 3yr/partial-upfront as configured defaults', () => {
    const recs = [
      rec('wrong-1', 1, 'all-upfront', 100, 0),
      rec('want-3yr', 3, 'partial-upfront', 100, 0),
    ];
    seedGlobalDefaults(3, 'partial-upfront');
    const result = pickBestVariantPerCell(recs);
    expect(result).toHaveLength(1);
    expect(result[0]!.id).toBe('want-3yr');
  });
});

//
// Issue #220 / #221: effectiveMonthlySavings + effectiveSavingsPct helpers
// + Monthly Cost and Effective % column rendering
//

describe('effectiveMonthlySavings', () => {
  const mk = (overrides: Partial<LocalRecommendation>): LocalRecommendation => ({
    id: 'r',
    provider: 'aws',
    service: 'ec2',
    resource_type: 't3.medium',
    region: 'us-east-1',
    count: 1,
    term: 1,
    savings: 100,
    upfront_cost: 0,
    monthly_cost: 50,
    ...overrides,
  } as unknown as LocalRecommendation);

  test('no-upfront: effective equals raw savings when upfront=0', () => {
    expect(effectiveMonthlySavings(mk({ savings: 100, upfront_cost: 0, term: 1 }))).toBeCloseTo(100);
  });

  test('all-upfront: savings is already net so returns savings directly', () => {
    // Provider reports net monthly savings after factoring in the upfront cost.
    // effectiveMonthlySavings must NOT subtract amortized upfront again (#1103).
    expect(effectiveMonthlySavings(mk({ savings: 50, upfront_cost: 600, term: 1 }))).toBeCloseTo(50);
    expect(effectiveMonthlySavings(mk({ savings: 1200, upfront_cost: 36000, term: 3 }))).toBeCloseTo(1200);
  });

  test('partial-upfront: savings is already net, returns savings directly', () => {
    expect(effectiveMonthlySavings(mk({ savings: 600, upfront_cost: 7200, term: 3 }))).toBeCloseTo(600);
  });

  test('term=0: returns raw savings (no division by term needed)', () => {
    expect(effectiveMonthlySavings(mk({ savings: 60, upfront_cost: 120, term: 0 }))).toBeCloseTo(60);
  });

  test('savings is returned as-is regardless of upfront size', () => {
    expect(effectiveMonthlySavings(mk({ savings: 10, upfront_cost: 1200, term: 1 }))).toBeCloseTo(10);
  });
});

describe('effectiveSavingsPct', () => {
  // mk() defaults to 'azure' for reconstruction-path tests. AWS rows require
  // on_demand_cost to be set; without it effectiveSavingsPct returns null
  // (#323). Azure reconstruction is still valid (Azure may omit on_demand_cost
  // for older cached rows where the field was not yet populated).
  const mk = (overrides: Partial<LocalRecommendation>): LocalRecommendation => ({
    id: 'r',
    provider: 'azure',
    service: 'ec2',
    resource_type: 't3.medium',
    region: 'us-east-1',
    count: 1,
    term: 1,
    savings: 100,
    upfront_cost: 0,
    monthly_cost: 50,
    ...overrides,
  } as unknown as LocalRecommendation);

  test('no-upfront: pct = savings / (monthly_cost + savings) * 100', () => {
    const pct = effectiveSavingsPct(mk({ savings: 100, upfront_cost: 0, monthly_cost: 50, term: 1 }));
    expect(pct).not.toBeNull();
    expect(pct!).toBeCloseTo(66.67, 1);
  });

  test('all-upfront with monthly_cost=0: numerator is savings (already net, no double-subtract)', () => {
    // savings=50 is already net of amortized upfront; denominator reconstruction
    // keeps amortized in onDemand so we get 50/100=50%, not 0/100=0% (#1103).
    const pct = effectiveSavingsPct(mk({ savings: 50, upfront_cost: 600, monthly_cost: 0, term: 1 }));
    expect(pct).not.toBeNull();
    expect(pct!).toBeCloseTo(50, 1);
  });

  test('partial-upfront: standard case', () => {
    // savings=600 net; amortized=200; onDemand=200+600+200=1000; pct=600/1000=60%.
    const pct = effectiveSavingsPct(mk({ savings: 600, upfront_cost: 7200, monthly_cost: 200, term: 3 }));
    expect(pct).not.toBeNull();
    expect(pct!).toBeCloseTo(60, 1);
  });

  test('on_demand_monthly=0 returns null (no division by zero)', () => {
    const pct = effectiveSavingsPct(mk({ savings: 0, upfront_cost: 0, monthly_cost: 0, term: 1 }));
    expect(pct).toBeNull();
  });

  test('term=0 clamps to 12 months (no explosion)', () => {
    const pct = effectiveSavingsPct(mk({ savings: 60, upfront_cost: 120, monthly_cost: 40, term: 0 }));
    expect(pct).toBeNull();
  });

  test('positive net savings even with large upfront: no double-subtract (#1103)', () => {
    // savings=10 is already net; amortized=100; onDemand=400+10+100=510.
    // Old formula gave -90/510=-17.65%; correct result is 10/510=1.96%.
    const pct = effectiveSavingsPct(mk({ savings: 10, upfront_cost: 1200, monthly_cost: 400, term: 1 }));
    expect(pct).not.toBeNull();
    expect(pct!).toBeGreaterThan(0);
    expect(pct!).toBeCloseTo(1.96, 1);
  });

  test('undefined/null monthly_cost returns null (data not provided)', () => {
    // monthly_cost null/undefined means the provider API did not return a monthly
    // recurring breakdown. Without it we cannot reconstruct on_demand_monthly,
    // so effectiveSavingsPct must return null rather than collapsing the
    // denominator to savings alone (which produced the misleading 100% rows in #252).
    expect(effectiveSavingsPct(mk({ savings: 100, upfront_cost: 0, monthly_cost: undefined, term: 1 }))).toBeNull();
    expect(effectiveSavingsPct(mk({ savings: 100, upfront_cost: 0, monthly_cost: null, term: 1 }))).toBeNull();
  });

  test('monthly_cost=0 (real all-upfront) is treated as known data, not missing', () => {
    // A literal 0 means the backend explicitly reported zero recurring cost
    // (e.g. an all-upfront commitment). effectiveSavingsPct SHOULD compute a
    // result in this case, because on_demand_monthly = 0 + savings + amortized.
    const pct = effectiveSavingsPct(mk({ savings: 100, upfront_cost: 0, monthly_cost: 0, term: 1 }));
    // onDemand = 0 + 100 + 0 = 100; effective = 100/100 * 100 = 100%
    expect(pct).not.toBeNull();
    expect(pct!).toBeCloseTo(100, 1);
  });

  // #323: AWS rows require the provider-canonical on_demand_cost; without it
  // the reconstruction formula (monthly_cost + savings + amortized) diverges
  // from the true CE baseline and produces misleadingly high percentages.
  describe('AWS reconstruction fallback returns null (#323)', () => {
    const mkAws = (overrides: Partial<LocalRecommendation>): LocalRecommendation =>
      mk({ provider: 'aws', ...overrides });

    test('AWS row with no on_demand_cost returns null regardless of monthly_cost', () => {
      // Repro: RI rec where EstimatedMonthlyOnDemandCost was absent from the
      // CE response. Reconstruction gives a number but it diverges from the
      // real denominator, so null is the correct sentinel.
      expect(effectiveSavingsPct(mkAws({ savings: 100, upfront_cost: 0, monthly_cost: 50, term: 1 }))).toBeNull();
    });

    test('AWS row with on_demand_cost=0 (treated as not-populated) returns null', () => {
      // Backend nonZeroPtr converts 0 -> nil, so 0 at the frontend means the
      // field was absent. Same outcome as missing.
      expect(effectiveSavingsPct(mkAws({ savings: 100, upfront_cost: 0, monthly_cost: 50, term: 1, on_demand_cost: 0 }))).toBeNull();
    });

    test('AWS row with valid on_demand_cost computes normally', () => {
      // When CE supplies the baseline, the formula is well-defined.
      // effectiveSavings = 100 - 0 = 100; pct = 100 / 300 * 100 = 33.33%
      const pct = effectiveSavingsPct(mkAws({ savings: 100, upfront_cost: 0, monthly_cost: 200, term: 1, on_demand_cost: 300 }));
      expect(pct).not.toBeNull();
      expect(pct!).toBeCloseTo(33.33, 1);
    });
  });

  // #274: on_demand_cost (when populated by the provider) is used directly
  // as the denominator instead of reconstructing it from
  // monthly_cost + savings + amortized. The reconstruction collapses for
  // Azure all-upfront recs where monthly_cost = $0 and inflates the % well
  // past realistic ceilings.
  describe('on_demand_cost preference (#274)', () => {
    test('repro of the live Azure D11_v2 row with provider-supplied on_demand_cost', () => {
      // Reconstructed (no on_demand_cost): savings=$29, upfront=$26,
      // monthly=$0, term=1 → onDemand = 0 + 29 + 2.17 = $31.17 →
      // pct = 29 / 31.17 ≈ 93% (reconstruction inflates because monthly_cost=0
      // collapses the denominator; the provider baseline is far more accurate).
      // With provider-supplied on_demand_cost = $122.64 (the real Azure
      // CostWithNoReservedInstances for 2 × Standard_D11_v2 in eastus),
      // pct = 29 / 122.64 ≈ 23.65% -- within realistic 1-year RI savings.
      // (Old formula subtracted amortized from numerator too: 26.83/122.64=21.88%.)
      const pctReconstructed = effectiveSavingsPct(
        mk({ savings: 29, upfront_cost: 26, monthly_cost: 0, term: 1 }),
      );
      expect(pctReconstructed!).toBeGreaterThan(80);

      const pctWithBaseline = effectiveSavingsPct(
        mk({ savings: 29, upfront_cost: 26, monthly_cost: 0, term: 1, on_demand_cost: 122.64 }),
      );
      expect(pctWithBaseline).not.toBeNull();
      expect(pctWithBaseline!).toBeCloseTo(23.65, 1);
    });

    test('on_demand_cost overrides reconstruction even when monthly_cost is non-zero', () => {
      // Demonstrates the preference order: when both are present, the
      // provider's on_demand_cost wins over the reconstructed value.
      // Otherwise a stale/cached monthly_cost could override the canonical
      // baseline silently.
      const pct = effectiveSavingsPct(
        mk({ savings: 100, upfront_cost: 0, monthly_cost: 50, term: 1, on_demand_cost: 500 }),
      );
      // With baseline: pct = (100 - 0) / 500 * 100 = 20%
      // Reconstruction would have given: pct = 100 / (50 + 100 + 0) * 100 = 66.7%
      expect(pct).not.toBeNull();
      expect(pct!).toBeCloseTo(20, 1);
    });
  });
});
