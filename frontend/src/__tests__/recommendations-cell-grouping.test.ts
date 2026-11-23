/**
 * Recommendations module tests: cell grouping, savings range and collapse and expand tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, mkRec, sameCell } from './recommendations-harness';
import {
  loadRecommendations,
  groupRecsByCell,
  cellSummary,
  pageLevelRange,
  resetExpandedCells,
  scaleCost,
} from '../recommendations';
import { HOURS_PER_MONTH } from '../lib/hours-per-month';
import type { LocalRecommendation } from '../types';
import * as api from '../api';
import * as state from '../state';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

describe('Monthly Cost + Effective % column rendering', () => {
  beforeEach(() => {
    document.body.innerHTML = [
      '<div id="opportunities-tab" class="tab-content active">',
      '<div id="recommendations-summary"></div>',
      '<div id="recommendations-list"></div>',
      '</div>',
      '<div id="purchase-modal" class="hidden">',
      '<div id="purchase-details"></div>',
      '</div>',
    ].join('');
    jest.clearAllMocks();
    jest.useFakeTimers();
    window.alert = jest.fn();
  });

  afterEach(() => {
    jest.useRealTimers();
  });

  const baseRec = (overrides: Partial<LocalRecommendation> = {}): LocalRecommendation => ({
    id: 'test-rec',
    provider: 'aws' as const,
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

  test('on_demand_monthly numeric filter: row with no on_demand_cost does not match = 0 (#330)', async () => {
    // #330 — without provider on_demand_cost, onDemandMonthly returns null,
    // which becomes a NaN sentinel in numericCellValue. A filter expr "0"
    // (equals zero) must NOT match such rows.
    const nullRec = baseRec({ savings: 100, upfront_cost: 0, monthly_cost: 50, term: 1 });
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [nullRec],
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([nullRec]);
    // Apply a filter that would match $0 if NaN were treated as 0.
    // loadRecommendations now calls getRecommendationsColumnFilters once for
    // pushdown extraction before the render path calls it again (issue #162),
    // so we need mockReturnValue rather than mockReturnValueOnce. The mock is
    // explicitly reset to {} afterward so subsequent tests are unaffected
    // (jest.clearAllMocks does not reset mockReturnValue).
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      on_demand_monthly: { kind: 'expr', expr: '0' },
    });
    await loadRecommendations();
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});

    // Null-monthly_cost row should be filtered out; tbody should have no visible recommendation rows.
    const rows = document.querySelectorAll('tbody tr.recommendation-row');
    expect(rows.length).toBe(0);
  });

  test('sort header for on_demand_monthly is wired - clicking sets sort to on_demand_monthly', async () => {
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [baseRec()],
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([baseRec()]);
    await loadRecommendations();

    const header = document.querySelector<HTMLTableCellElement>('th[data-sort="on_demand_monthly"]');
    expect(header).not.toBeNull();
    header!.click();
    expect(state.setRecommendationsSort).toHaveBeenCalledWith(
      expect.objectContaining({ column: 'on_demand_monthly' }),
    );
  });
});

//
// Issues #225 + #226: cell grouping with savings range and collapse/expand
//

describe('Issues #225 + #226: cell grouping with savings range and collapse/expand', () => {
  describe('groupRecsByCell (pure helper)', () => {
    test('groups two recs with the same cell key into one entry', () => {
      const recs = sameCell(80, 120);
      const groups = groupRecsByCell(recs);
      expect(groups.size).toBe(1);
      expect(groups.values().next().value).toHaveLength(2);
    });

    test('groups recs with different resource_type into separate cells', () => {
      const rec1 = mkRec({ resource_type: 'm5.large' });
      const rec2 = mkRec({ resource_type: 't3.medium' });
      const groups = groupRecsByCell([rec1, rec2]);
      expect(groups.size).toBe(2);
    });

    test('EC2 recs differing only by platform or tenancy are separate cells; same pair shares one (issue #806)', () => {
      const d = { platform: 'Linux/UNIX', tenancy: 'default' };
      const base = mkRec({ details: d });
      expect(groupRecsByCell([base, mkRec({ details: { ...d, platform: 'Windows' } })]).size).toBe(2);
      expect(groupRecsByCell([base, mkRec({ details: { ...d, tenancy: 'dedicated' } })]).size).toBe(2);
      expect(groupRecsByCell([base, mkRec({ details: { ...d }, term: 3 })]).size).toBe(1);
      const zonal = mkRec({ availability_zone: 'us-east-1a', details: d });
      expect(Array.from(groupRecsByCell([zonal]).keys())[0]).toBe('aws||ec2|us-east-1|m5.large||us-east-1a|Linux/UNIX|default');
    });

    test('non-EC2 recs and EC2 recs without platform/tenancy keep their cell key (issue #806)', () => {
      const rds = mkRec({ service: 'rds', details: { platform: 'x', tenancy: 'y' } });
      expect(Array.from(groupRecsByCell([rds]).keys())[0]).toBe('aws||rds|us-east-1|m5.large|');
      expect(Array.from(groupRecsByCell([mkRec({ details: { instance_type: 'm5.large' } }), mkRec({})]).keys())).toEqual(['aws||ec2|us-east-1|m5.large|']);
    });

    test('zonal EC2 recs differing only by AZ are separate cells; regional recs are unchanged (issue #792)', () => {
      const a = mkRec({ availability_zone: 'us-east-1a' });
      const b = mkRec({ availability_zone: 'us-east-1b' });
      const a2 = mkRec({ availability_zone: 'us-east-1a', term: 3 });
      const regional1 = mkRec({});
      const regional2 = mkRec({ term: 3 });
      const groups = groupRecsByCell([a, b, a2, regional1, regional2]);
      expect(groups.size).toBe(3);
      expect(Array.from(groups.keys())).toContain('aws||ec2|us-east-1|m5.large|');
    });
  });

  describe('cellSummary (pure helper)', () => {
    test('computes min and max savings across variants', () => {
      const recs = sameCell(80, 120);
      const s = cellSummary(recs);
      expect(s.savingsMin).toBe(80);
      expect(s.savingsMax).toBe(120);
    });

    test('collapses to same value for a single-variant cell', () => {
      const rec = mkRec({ savings: 55, upfront_cost: 0, term: 1 });
      const s = cellSummary([rec]);
      expect(s.savingsMin).toBe(55);
      expect(s.savingsMax).toBe(55);
      expect(s.termMin).toBe(1);
      expect(s.termMax).toBe(1);
    });

    test('returns zeroed summary for empty input (defensive)', () => {
      const s = cellSummary([]);
      expect(s.savingsMin).toBe(0);
      expect(s.savingsMax).toBe(0);
    });
  });

  describe('pageLevelRange (pure helper)', () => {
    test('sums per-cell min and max across two cells', () => {
      const cell1 = sameCell(50, 100);  // min=50, max=100
      const cell2 = [
        mkRec({ id: 'b1', resource_type: 't3.small', savings: 30, term: 1, payment: 'no-upfront', upfront_cost: 0 }),
        mkRec({ id: 'b2', resource_type: 't3.small', savings: 70, term: 3, payment: 'all-upfront', upfront_cost: 600 }),
      ];
      const groups = groupRecsByCell([...cell1, ...cell2]);
      const plr = pageLevelRange(groups);
      expect(plr.cellCount).toBe(2);
      expect(plr.savingsMin).toBe(80);   // 50 + 30
      expect(plr.savingsMax).toBe(170);  // 100 + 70
    });

    test('returns cellCount=0 and savings=0 for empty groups', () => {
      const plr = pageLevelRange(new Map());
      expect(plr.cellCount).toBe(0);
      expect(plr.savingsMin).toBe(0);
      expect(plr.savingsMax).toBe(0);
    });

    test('paybackMonthsMin uses per-cell paired variants, not cross-extrema (PR #283 CR)', () => {
      // Cell A has two variants:
      //   v1: savings=50,  upfront=0    → ratio = 0     (best payback: 0 months)
      //   v2: savings=200, upfront=300  → ratio = 1.5   (1.5 months)
      //
      // Cell B has two variants:
      //   v3: savings=100, upfront=200  → ratio = 2     (2 months)
      //   v4: savings=150, upfront=600  → ratio = 4     (4 months)
      //
      // Attainable min-payback selection: cell A picks v1 (ratio 0), cell B
      // picks v3 (ratio 2). Total upfront=0+200=200, total savings=50+100=150.
      // paybackMonthsMin = 200/150 ≈ 1.333...
      //
      // Cross-extrema (old code) would compute:
      //   upfrontMin = 0 (from v1) + 200 (from v3) = 200
      //   savingsMax = 200 (from v2) + 150 (from v4) = 350
      //   paybackMonthsMin = 200/350 ≈ 0.571
      // That's not attainable because it mixes upfront from v1 with savings
      // from v2 (different variants of cell A).
      //
      // The new per-cell paired logic must produce 200/150 ≈ 1.333, not 0.571.
      const cellA = [
        mkRec({ id: 'a-v1', resource_type: 'm5.large',  savings:  50, upfront_cost:   0 }),
        mkRec({ id: 'a-v2', resource_type: 'm5.large',  savings: 200, upfront_cost: 300 }),
      ];
      const cellB = [
        mkRec({ id: 'b-v3', resource_type: 't3.medium', savings: 100, upfront_cost: 200 }),
        mkRec({ id: 'b-v4', resource_type: 't3.medium', savings: 150, upfront_cost: 600 }),
      ];
      const groups = groupRecsByCell([...cellA, ...cellB]);
      const plr = pageLevelRange(groups);

      // Per-cell paired: best ratio for A is v1 (0/50=0), best for B is v3 (200/100=2).
      // Combined: upfront=0+200=200, savings=50+100=150 → paybackMin=200/150≈1.333
      expect(plr.paybackMonthsMin).toBeCloseTo(200 / 150, 5);

      // Cross-extrema would give 200/350≈0.571 — must NOT be the result.
      expect(plr.paybackMonthsMin).not.toBeCloseTo(200 / 350, 5);

      // paybackMonthsMax: worst payback per cell. A picks v2 (300/200=1.5),
      // B picks v4 (600/150=4). Combined: upfront=300+600=900, savings=200+150=350.
      // paybackMax = 900/350 ≈ 2.571
      expect(plr.paybackMonthsMax).toBeCloseTo(900 / 350, 5);
    });
  });

  describe('DOM rendering (cell grouping integrated)', () => {
    beforeEach(() => {
      document.body.innerHTML = [
        '<div id="opportunities-tab" class="tab-content active">',
        '<div id="recommendations-summary"></div>',
        '<div id="recommendations-list"></div>',
        '</div>',
        '<div id="purchase-modal" class="hidden">',
        '<div id="purchase-details"></div>',
        '</div>',
      ].join('');
      jest.clearAllMocks();
      // Reset getHiddenColumns to return empty Set so tests don't inherit sticky values
      (state.getHiddenColumns as jest.Mock).mockReturnValue(new Set());
      jest.useFakeTimers();
      window.alert = jest.fn();
      // Reset cell expand state so tests don't share module-level expandedCells.
      resetExpandedCells();
    });

    afterEach(() => {
      jest.useRealTimers();
    });

    const multiVariantRecs = (): LocalRecommendation[] => [
      mkRec({ id: 'mv-1', savings: 80,  term: 1, payment: 'no-upfront',   upfront_cost: 0 }),
      mkRec({ id: 'mv-2', savings: 120, term: 3, payment: 'all-upfront',  upfront_cost: 1200 }),
    ];

    test('multi-variant cell renders a summary row, not two flat variant rows by default', async () => {
      const recs = multiVariantRecs();
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      await loadRecommendations();

      // Summary row should be present
      const summaryRow = document.querySelector('.rec-cell-summary-row');
      expect(summaryRow).not.toBeNull();

      // Variant rows should NOT be rendered (collapsed by default)
      const variantRows = document.querySelectorAll('.rec-variant-row');
      expect(variantRows.length).toBe(0);
    });

    test('multi-variant summary row omits hidden column values', async () => {
      const recs = multiVariantRecs();
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getHiddenColumns as jest.Mock).mockReturnValue(new Set(['region', 'savings', 'upfront_cost', 'term']));
      await loadRecommendations();

      const summaryContent = document.querySelector('.rec-cell-summary-content');
      expect(summaryContent).not.toBeNull();
      expect(summaryContent!.textContent).toContain('2 variants');
      expect(summaryContent!.textContent).not.toContain('us-east-1');
      expect(summaryContent!.textContent).not.toContain('$80');
      expect(summaryContent!.textContent).not.toContain('$120');
      expect(summaryContent!.textContent).not.toContain('upfront:');
      expect(summaryContent!.textContent).not.toContain('term:');
      expect(summaryContent!.querySelector('.rec-cell-range')).toBeNull();
    });

    test('chevron button lives inside td.checkbox-col of the summary row (closes #280)', async () => {
      const recs = multiVariantRecs();
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      await loadRecommendations();

      const summaryRow = document.querySelector('.rec-cell-summary-row');
      expect(summaryRow).not.toBeNull();
      const checkboxCell = summaryRow!.querySelector('td.checkbox-col');
      expect(checkboxCell).not.toBeNull();
      const chevron = checkboxCell!.querySelector<HTMLButtonElement>('.rec-cell-chevron');
      expect(chevron).not.toBeNull();
    });

    test('chevron is a <button> with aria-label and is not inside rec-cell-summary-content (closes #280)', async () => {
      const recs = multiVariantRecs();
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      await loadRecommendations();

      const chevron = document.querySelector<HTMLButtonElement>('.rec-cell-chevron');
      expect(chevron).not.toBeNull();
      // Must be a <button> so native Enter/Space keyboard activation works without extra keydown handlers.
      expect(chevron!.tagName.toLowerCase()).toBe('button');
      expect(chevron!.getAttribute('type')).toBe('button');
      expect(chevron!.getAttribute('aria-label')).toBeTruthy();
      // Chevron must NOT be a descendant of .rec-cell-summary-content.
      expect(chevron!.closest('.rec-cell-summary-content')).toBeNull();
    });

    test('clicking chevron expands the cell and shows variant rows', async () => {
      const recs = multiVariantRecs();
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      await loadRecommendations();

      const chevron = document.querySelector<HTMLButtonElement>('.rec-cell-chevron');
      expect(chevron).not.toBeNull();
      // aria-expanded starts false (collapsed)
      expect(chevron!.getAttribute('aria-expanded')).toBe('false');
      chevron!.click();

      // After expand: variant rows should appear and aria-expanded flips
      const variantRows = document.querySelectorAll('.rec-variant-row');
      expect(variantRows.length).toBe(2);
      const updatedChevron = document.querySelector<HTMLButtonElement>('.rec-cell-chevron');
      expect(updatedChevron!.getAttribute('aria-expanded')).toBe('true');
    });

    test('page-level range banner is removed (closes #278) — savings card is the canonical surface', async () => {
      // The "Recommended range" banner under the table was redundant with
      // the Potential Monthly Savings card after #272 / #279 brought the
      // same range to the summary header. Closing #278 removed the
      // banner; the card is the single source of truth.
      const recs = multiVariantRecs();
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
      await loadRecommendations();

      // Banner gone.
      expect(document.querySelector('.rec-range-banner')).toBeNull();
      // Card carries the range.
      const summary = document.getElementById('recommendations-summary');
      const savingsCard = Array.from(summary?.querySelectorAll('.card') ?? [])
        .find((c) => /Monthly Savings/.test(c.querySelector('h3')?.textContent ?? ''));
      expect(savingsCard?.querySelector('.value.savings')?.textContent).toMatch(/\$\d/);
    });

    test('single-variant cell renders a flat row, no summary row', async () => {
      const rec = mkRec({ id: 'solo', savings: 60 });
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: [rec] });
      (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
      await loadRecommendations();

      const summaryRow = document.querySelector('.rec-cell-summary-row');
      expect(summaryRow).toBeNull();

      // The flat row should exist as a regular recommendation-row
      const rows = document.querySelectorAll('tr.recommendation-row');
      expect(rows.length).toBe(1);
    });

    test('Expand all button appears in filter-status bar for multi-variant cells', async () => {
      const recs = multiVariantRecs();
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      await loadRecommendations();

      const expandAllBtn = document.querySelector('.expand-all-toggle');
      expect(expandAllBtn).not.toBeNull();
      expect(expandAllBtn!.textContent).toMatch(/Expand all/);
    });

    test('Expand all expands all multi-variant cells', async () => {
      const recs = multiVariantRecs();
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      await loadRecommendations();

      const expandAllBtn = document.querySelector<HTMLButtonElement>('.expand-all-toggle');
      expect(expandAllBtn).not.toBeNull();
      expandAllBtn!.click();

      // After expand all: variant rows should be visible
      const variantRows = document.querySelectorAll('.rec-variant-row');
      expect(variantRows.length).toBe(2);
    });

    // QA 4.13: expand/collapse state must be reset when the data source
    // reloads due to a provider/account Global filter change. Before the fix,
    // stale expandedCells keys from the prior provider desynchronised the
    // Expand-All button label and left newly-shown groups collapsed.
    test('QA 4.13: expand state clears on loadRecommendations (provider filter change)', async () => {
      const recs = multiVariantRecs();
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);

      // 1. Load and expand all groups.
      await loadRecommendations();
      const expandAllBtn = document.querySelector<HTMLButtonElement>('.expand-all-toggle');
      expect(expandAllBtn).not.toBeNull();
      expandAllBtn!.click();

      // Confirm we are now in "Collapse all" state.
      expect(expandAllBtn!.textContent).toMatch(/Collapse all/);

      // 2. Simulate a provider filter change by calling loadRecommendations()
      //    again (this is exactly what the subscribeProvider callback invokes).
      await loadRecommendations();

      // 3. Expand state must have been cleared: button label reverts to
      //    "Expand all" and no variant rows are visible (groups are collapsed).
      const expandAllBtn2 = document.querySelector<HTMLButtonElement>('.expand-all-toggle');
      expect(expandAllBtn2).not.toBeNull();
      expect(expandAllBtn2!.textContent).toMatch(/Expand all/);

      const variantRows = document.querySelectorAll('.rec-variant-row');
      expect(variantRows.length).toBe(0);
    });
  });
});

//
// Issue #319: cost-period selector tests
//

describe('Issue #319: scaleCost', () => {
  test('monthly factor is 1 (identity)', () => {
    expect(scaleCost(100, 'monthly')).toBe(100);
  });

  // Issues #337/#394: hourly uses the backend's 730-hour month, not 720.
  test('hourly factor is 1/730, the shared hours-per-month', () => {
    expect(HOURS_PER_MONTH).toBe(730);
    expect(scaleCost(730, 'hourly')).toBeCloseTo(1.0, 10);
    expect(scaleCost(720, 'hourly')).not.toBeCloseTo(1.0, 4);
  });

  test('daily factor is 1/30', () => {
    expect(scaleCost(300, 'daily')).toBeCloseTo(10.0);
  });

  test('yearly factor is 12', () => {
    expect(scaleCost(100, 'yearly')).toBeCloseTo(1200);
  });
});
