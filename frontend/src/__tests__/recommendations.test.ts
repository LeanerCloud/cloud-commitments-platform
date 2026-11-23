/**
 * Recommendations module tests
 */
// The harness registers the module mocks, so it must be imported first.
import {
  mockFactoryDefaults,
  mkRec,
  sameCell,
  DOM_FOR_319,
  setupOpportunitiesTabDom,
  mkSpRec,
} from './recommendations-harness';
import {
  loadRecommendations,
  displaySavingsPct,
  groupRecsByCell,
  cellSummary,
  pageLevelRange,
  resetExpandedCells,
  scaleCost,
  formatCostForPeriod,
  periodSuffix,
  loadColumnVisibility,
  saveColumnVisibility,
  resetColumnVisibilityState,
  TOGGLEABLE_COLUMNS,
  COLUMN_DEFS,
  renderUsageSparkline,
  loadColumnFilters,
  saveColumnFilters,
  resetColumnFiltersState,
  spGroupKey,
  groupSpCellKeys,
  formatCapacity,
  scaleRecForCapacity,
  weightedDisplaySavingsPct,
} from '../recommendations';
import type { CostPeriod } from '../state';
import { HOURS_PER_MONTH } from '../lib/hours-per-month';
import type { LocalRecommendation } from '../types';
import { localStorageMock } from './setup';
import * as api from '../api';
import * as state from '../state';
import * as recsApi from '../api/recommendations';

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

  test('table header includes "Monthly Cost", "On-Demand Monthly", and "Effective %" columns', async () => {
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [baseRec()],
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([baseRec()]);
    await loadRecommendations();

    const headers = Array.from(document.querySelectorAll('th')).map((th) => th.textContent ?? '');
    expect(headers.some((h) => h.includes('Monthly Cost'))).toBe(true);
    expect(headers.some((h) => h.includes('On-Demand Monthly'))).toBe(true);
    expect(headers.some((h) => h.includes('Effective %'))).toBe(true);
  });

  test('no-upfront row: Monthly Cost shows rec.monthly_cost, Effective % is positive', async () => {
    // AWS row requires on_demand_cost (#323); add it so the pct column renders.
    const rec = baseRec({ savings: 100, upfront_cost: 0, monthly_cost: 50, term: 1, on_demand_cost: 150 });
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [rec],
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
    await loadRecommendations();

    const cells = Array.from(document.querySelectorAll('tbody td')).map((td) => td.textContent ?? '');
    expect(cells.some((c) => c === '$50')).toBe(true);
    expect(cells.some((c) => c.includes('%') && !c.includes('em'))).toBe(true);
  });

  test('all-upfront row: Monthly Cost shows $0, Effective % uses net savings directly (#1103)', async () => {
    // AWS row requires on_demand_cost (#323). savings=50 (already net), upfront=600, term=1.
    // Old formula: effectiveSavings=0 => 0.0%. Fixed: pct=50/100=50.0%.
    const rec = baseRec({ savings: 50, upfront_cost: 600, monthly_cost: 0, term: 1, on_demand_cost: 100 });
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [rec],
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
    await loadRecommendations();

    const cells = Array.from(document.querySelectorAll('tbody td')).map((td) => td.textContent ?? '');
    expect(cells.some((c) => c === '$0')).toBe(true);
    expect(cells.some((c) => c === '50.0%')).toBe(true);
  });

  test('on_demand_monthly=0 row: Effective % renders as em-dash', async () => {
    const rec = baseRec({ savings: 0, upfront_cost: 0, monthly_cost: 0, term: 1 });
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [rec],
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
    await loadRecommendations();

    const cells = Array.from(document.querySelectorAll('tbody td')).map((td) => td.textContent ?? '');
    expect(cells.some((c) => c === '—')).toBe(true);
  });

  test('negative-effective row: Effective % cell has effective-pct-negative class', async () => {
    // Provider reports negative net savings (cost anomaly -- rec would cost more than on-demand).
    // savings=-90 (already net negative), on_demand_cost=510 => pct<0.
    const rec = baseRec({ savings: -90, upfront_cost: 0, monthly_cost: 400, term: 1, on_demand_cost: 510 });
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [rec],
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
    await loadRecommendations();

    const negativeCells = document.querySelectorAll('tbody td.effective-pct-negative');
    expect(negativeCells.length).toBeGreaterThan(0);
  });

  test('sort header for monthly_cost is wired - clicking sets sort to monthly_cost', async () => {
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [baseRec()],
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([baseRec()]);
    await loadRecommendations();

    const header = document.querySelector<HTMLTableCellElement>('th[data-sort="monthly_cost"]');
    expect(header).not.toBeNull();
    header!.click();
    expect(state.setRecommendationsSort).toHaveBeenCalledWith(
      expect.objectContaining({ column: 'monthly_cost' }),
    );
  });

  test('sort header for effective_savings_pct is wired - clicking sets sort', async () => {
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [baseRec()],
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([baseRec()]);
    await loadRecommendations();

    const header = document.querySelector<HTMLTableCellElement>('th[data-sort="effective_savings_pct"]');
    expect(header).not.toBeNull();
    header!.click();
    expect(state.setRecommendationsSort).toHaveBeenCalledWith(
      expect.objectContaining({ column: 'effective_savings_pct' }),
    );
  });

  test('On-Demand Monthly column renders formatCurrency from provider on_demand_cost (#330)', async () => {
    // #330 — column shows the raw provider value, not a reconstruction.
    const rec = baseRec({ on_demand_cost: 150, savings: 100, upfront_cost: 0, monthly_cost: 50, term: 1 });
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [rec],
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
    await loadRecommendations();

    const headers = Array.from(document.querySelectorAll('th')).map((th) => th.textContent ?? '');
    expect(headers.some((h) => h.includes('On-Demand Monthly'))).toBe(true);

    // Pin the specific on_demand_monthly cell by column index (not a scan of all tds).
    const odmColIdx = Array.from(document.querySelectorAll('th'))
      .findIndex((th) => th.getAttribute('data-sort') === 'on_demand_monthly');
    expect(odmColIdx).toBeGreaterThanOrEqual(0);
    const firstRowCells = Array.from(
      document.querySelectorAll('tbody tr.recommendation-row td'),
    );
    expect(firstRowCells[odmColIdx]?.textContent).toBe('$150');
  });

  test('On-Demand Monthly column renders em-dash when on_demand_cost is missing (#330)', async () => {
    // #330 — without provider on_demand_cost, no reconstruction; cell is em-dash.
    const rec = baseRec({ savings: 100, upfront_cost: 0, monthly_cost: 50, term: 1 });
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [rec],
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([rec]);
    await loadRecommendations();

    // Pin the specific on_demand_monthly cell (not a scan of all tds — Effective % also renders em-dash).
    const odmColIdx = Array.from(document.querySelectorAll('th'))
      .findIndex((th) => th.getAttribute('data-sort') === 'on_demand_monthly');
    const firstRowCells = Array.from(
      document.querySelectorAll('tbody tr.recommendation-row td'),
    );
    expect(firstRowCells[odmColIdx]?.textContent).toBe('—');
  });

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

  test('null input returns null', () => {
    const periods: CostPeriod[] = ['hourly', 'daily', 'monthly', 'yearly'];
    for (const p of periods) {
      expect(scaleCost(null, p)).toBeNull();
    }
  });

  test('undefined input returns null', () => {
    const periods: CostPeriod[] = ['hourly', 'daily', 'monthly', 'yearly'];
    for (const p of periods) {
      expect(scaleCost(undefined, p)).toBeNull();
    }
  });

  test('zero input returns 0 (not null)', () => {
    expect(scaleCost(0, 'hourly')).toBe(0);
  });
});

describe('Issue #319: formatCostForPeriod', () => {
  test('monthly uses formatCurrency mock ($X)', () => {
    // formatCurrency mock returns `$${val}` so for 100 → "$100"
    expect(formatCostForPeriod(100, 'monthly')).toBe('$100');
  });

  test('hourly uses 4 decimal places', () => {
    expect(formatCostForPeriod(730, 'hourly')).toMatch(/\$1\.0000/);
  });

  test('daily uses 2 decimal places', () => {
    expect(formatCostForPeriod(300, 'daily')).toMatch(/\$10\.00/);
  });

  test('yearly uses 0 decimal places', () => {
    expect(formatCostForPeriod(100, 'yearly')).toMatch(/\$1200$/);
  });

  test('null input returns em-dash for all periods', () => {
    const periods: CostPeriod[] = ['hourly', 'daily', 'monthly', 'yearly'];
    for (const p of periods) {
      expect(formatCostForPeriod(null, p)).toBe('—');
    }
  });

  test('undefined input returns em-dash for all periods', () => {
    const periods: CostPeriod[] = ['hourly', 'daily', 'monthly', 'yearly'];
    for (const p of periods) {
      expect(formatCostForPeriod(undefined, p)).toBe('—');
    }
  });

  test('zero input renders as $0 (not em-dash)', () => {
    // hourly: $0.0000, daily: $0.00, monthly: $0, yearly: $0
    expect(formatCostForPeriod(0, 'hourly')).toMatch(/\$0\.0000/);
    expect(formatCostForPeriod(0, 'daily')).toMatch(/\$0\.00/);
    expect(formatCostForPeriod(0, 'yearly')).toMatch(/\$0$/);
  });
});

describe('Issue #319: periodSuffix', () => {
  test('returns correct suffix for each period', () => {
    expect(periodSuffix('hourly')).toBe('/ hr');
    expect(periodSuffix('daily')).toBe('/ day');
    expect(periodSuffix('monthly')).toBe('/ mo');
    expect(periodSuffix('yearly')).toBe('/ yr');
  });
});

describe('Issue #319: column header labels update with period', () => {
  const mockRec = {
    id: 'r1',
    provider: 'aws' as const,
    service: 'ec2',
    resource_type: 'c5.large',
    region: 'us-east-1',
    count: 2,
    term: 1,
    payment: 'no-upfront',
    savings: 500,
    upfront_cost: 0,
    monthly_cost: 300,
    cloud_account_id: undefined,
  };

  beforeEach(() => {
    document.body.innerHTML = DOM_FOR_319;
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [mockRec],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([mockRec]);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([mockRec]);
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
  });

  test('monthly period: headers show "Monthly Savings" and "Monthly Cost"', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    await loadRecommendations();
    const headers = document.querySelectorAll('th.sortable span');
    const labels = Array.from(headers).map((h) => h.textContent);
    expect(labels).toContain('Monthly Savings');
    expect(labels).toContain('Monthly Cost');
  });

  test('hourly period: headers show "Savings / hr" and "Cost / hr"', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('hourly');
    await loadRecommendations();
    const headers = document.querySelectorAll('th.sortable span');
    const labels = Array.from(headers).map((h) => h.textContent);
    expect(labels).toContain('Savings / hr');
    expect(labels).toContain('Cost / hr');
  });

  test('daily period: headers show "Savings / day" and "Cost / day"', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('daily');
    await loadRecommendations();
    const headers = document.querySelectorAll('th.sortable span');
    const labels = Array.from(headers).map((h) => h.textContent);
    expect(labels).toContain('Savings / day');
    expect(labels).toContain('Cost / day');
  });

  test('yearly period: headers show "Savings / yr" and "Cost / yr"', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('yearly');
    await loadRecommendations();
    const headers = document.querySelectorAll('th.sortable span');
    const labels = Array.from(headers).map((h) => h.textContent);
    expect(labels).toContain('Savings / yr');
    expect(labels).toContain('Cost / yr');
  });

  test('non-cost headers are period-invariant', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('hourly');
    await loadRecommendations();
    const headers = document.querySelectorAll('th.sortable span');
    const labels = Array.from(headers).map((h) => h.textContent);
    expect(labels).toContain('Provider');
    expect(labels).toContain('Term');
    expect(labels).toContain('Upfront Cost');
    expect(labels).toContain('Effective %');
  });
});

describe('Issue #319: table cells scale with period', () => {
  const mockRec = {
    id: 'r1',
    provider: 'aws' as const,
    service: 'ec2',
    resource_type: 'c5.large',
    region: 'us-east-1',
    count: 2,
    term: 1,
    payment: 'no-upfront',
    savings: 720,
    upfront_cost: 0,
    monthly_cost: 720,
    cloud_account_id: undefined,
  };

  beforeEach(() => {
    document.body.innerHTML = DOM_FOR_319;
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [mockRec],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([mockRec]);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([mockRec]);
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
  });

  test('hourly: savings cell shows $0.9863 (720 / 730 hours)', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('hourly');
    await loadRecommendations();
    const rows = document.querySelectorAll('tr.recommendation-row');
    expect(rows.length).toBeGreaterThan(0);
    const savingsCell = rows[0]!.querySelector('.savings');
    expect(savingsCell?.textContent).toMatch(/\$0\.9863/);
  });

  test('daily: savings cell shows $24.00', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('daily');
    await loadRecommendations();
    const rows = document.querySelectorAll('tr.recommendation-row');
    const savingsCell = rows[0]!.querySelector('.savings');
    expect(savingsCell?.textContent).toMatch(/\$24\.00/);
  });

  test('monthly: savings cell shows existing formatCurrency output', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    await loadRecommendations();
    const rows = document.querySelectorAll('tr.recommendation-row');
    const savingsCell = rows[0]!.querySelector('.savings');
    // formatCurrency mock returns `$${val}` so for 720 it returns "$720"
    expect(savingsCell?.textContent).toContain('720');
  });

  test('yearly: savings cell shows $8640', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('yearly');
    await loadRecommendations();
    const rows = document.querySelectorAll('tr.recommendation-row');
    const savingsCell = rows[0]!.querySelector('.savings');
    expect(savingsCell?.textContent).toMatch(/\$8640/);
  });

  test('upfront_cost is NOT scaled (period-invariant)', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('hourly');
    const recWithUpfront = { ...mockRec, upfront_cost: 1000 };
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [recWithUpfront],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([recWithUpfront]);
    await loadRecommendations();
    const rows = document.querySelectorAll('tr.recommendation-row');
    // We just verify "1000" appears somewhere but "0.0014" (1000/730) does not
    const rowText = rows[0]!.textContent ?? '';
    expect(rowText).toContain('1000');
    expect(rowText).not.toMatch(/0\.0014/);
  });

  test('null monthly_cost renders "—" regardless of period', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('yearly');
    const recNullCost = { ...mockRec, monthly_cost: null };
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [recNullCost],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([recNullCost]);
    await loadRecommendations();
    const rows = document.querySelectorAll('tr.recommendation-row');
    const rowHtml = rows[0]!.innerHTML;
    expect(rowHtml).toContain('—');
  });

  // CR pass-1 nitpick: the existing #319 cases verify labels and formatted
  // values, but they never prove that sort and filter accessors actually
  // apply the period scale. A regression that reverts numericCellValue or
  // SORTABLE_NUMERIC_COLUMNS.savings to raw monthly_cost would still pass
  // every assertion above. The two tests below pin the scaled-numeric
  // contract from both ends: ordering (sort) and inclusion (filter).
  test('yearly sort orders savings cells by yearly-scaled value (asc)', async () => {
    const recSmall = { ...mockRec, id: 'r-small', resource_type: 'small', savings: 100, monthly_cost: 100 };
    const recLarge = { ...mockRec, id: 'r-large', resource_type: 'large', savings: 200, monthly_cost: 200 };
    // Inputs are intentionally pre-sorted DESC so a no-op scale that
    // preserves input order would fail this asc assertion.
    (state.getCostPeriod as jest.Mock).mockReturnValue('yearly');
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'asc' });
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [recLarge, recSmall],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([recLarge, recSmall]);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([recLarge, recSmall]);

    await loadRecommendations();

    const rows = Array.from(document.querySelectorAll('tr.recommendation-row'));
    expect(rows).toHaveLength(2);
    // Even though recLarge appears first in the input array, ascending
    // savings sort (which routes through scaleCost) places r-small first.
    expect(rows[0]?.textContent).toContain('small');
    expect(rows[1]?.textContent).toContain('large');
  });

  test('hourly numeric filter compares the scaled (per-hour) savings, not the raw monthly value', async () => {
    // Both recs would pass a ">0.75" filter against raw monthly_cost (360
    // and 720 are both > 0.75). After hourly scaling (÷730), only r-high
    // (0.986) passes; r-low (0.493) is filtered out. If numericCellValue were
    // to drop the scaleCost call, this test would surface the regression.
    const recLow  = { ...mockRec, id: 'r-low',  resource_type: 'low',  savings: 360, monthly_cost: 360 };
    const recHigh = { ...mockRec, id: 'r-high', resource_type: 'high', savings: 720, monthly_cost: 720 };
    (state.getCostPeriod as jest.Mock).mockReturnValue('hourly');
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      savings: { kind: 'expr', expr: '>0.75' },
    });
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [recLow, recHigh],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([recLow, recHigh]);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([recLow, recHigh]);

    await loadRecommendations();

    const rows = Array.from(document.querySelectorAll('tr.recommendation-row'));
    expect(rows).toHaveLength(1);
    expect(rows[0]?.textContent).toContain('high');
  });
});

describe('Issue #319: summary card label updates with period', () => {
  const mockRec = {
    id: 'r1',
    provider: 'aws' as const,
    service: 'ec2',
    resource_type: 'c5.large',
    region: 'us-east-1',
    count: 1,
    term: 1,
    payment: 'no-upfront',
    savings: 500,
    upfront_cost: 0,
    monthly_cost: 300,
    cloud_account_id: undefined,
  };

  beforeEach(() => {
    document.body.innerHTML = DOM_FOR_319;
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [mockRec],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([mockRec]);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([mockRec]);
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
  });

  test('monthly period: card says "Potential Monthly Savings"', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    await loadRecommendations();
    const summary = document.getElementById('recommendations-summary');
    expect(summary?.textContent).toContain('Potential Monthly Savings');
  });

  test('hourly period: card says "Potential Savings / hr"', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('hourly');
    await loadRecommendations();
    const summary = document.getElementById('recommendations-summary');
    expect(summary?.textContent).toContain('Potential Savings / hr');
  });

  test('yearly period: card says "Potential Savings / yr"', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('yearly');
    await loadRecommendations();
    const summary = document.getElementById('recommendations-summary');
    expect(summary?.textContent).toContain('Potential Savings / yr');
  });

  test('Upfront Cost card is not affected by period', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('hourly');
    await loadRecommendations();
    const summary = document.getElementById('recommendations-summary');
    expect(summary?.textContent).toContain('Total Upfront Cost');
  });
});

describe('Issue #319: cost-period dropdown is rendered in the filter-status bar', () => {
  const mockRec = {
    id: 'r1',
    provider: 'aws' as const,
    service: 'ec2',
    resource_type: 'c5.large',
    region: 'us-east-1',
    count: 1,
    term: 1,
    payment: 'no-upfront',
    savings: 500,
    upfront_cost: 0,
    monthly_cost: 300,
    cloud_account_id: undefined,
  };

  beforeEach(() => {
    document.body.innerHTML = DOM_FOR_319;
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [mockRec],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue([mockRec]);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([mockRec]);
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
  });

  test('cost-period dropdown is rendered after loadRecommendations', async () => {
    await loadRecommendations();
    const select = document.querySelector<HTMLSelectElement>('#cost-period-select');
    expect(select).not.toBeNull();
  });

  test('dropdown has 4 options: Hourly, Daily, Monthly, Yearly', async () => {
    await loadRecommendations();
    const select = document.querySelector<HTMLSelectElement>('#cost-period-select');
    const options = Array.from(select?.options ?? []).map((o) => o.textContent);
    expect(options).toContain('Hourly');
    expect(options).toContain('Daily');
    expect(options).toContain('Monthly');
    expect(options).toContain('Yearly');
  });

  test('dropdown selected value reflects getCostPeriod() result', async () => {
    (state.getCostPeriod as jest.Mock).mockReturnValue('yearly');
    await loadRecommendations();
    const select = document.querySelector<HTMLSelectElement>('#cost-period-select');
    expect(select?.value).toBe('yearly');
  });

  test('changing the dropdown calls setCostPeriod and triggers rerender', async () => {
    // Seed a different return value for the post-change loadRecommendations()
    // call so we can verify the rerender actually rebuilt headers/cells
    // against the new period (not just that setCostPeriod was invoked).
    // CR pass-1 nitpick: the previous version of this test would still pass
    // if the change handler stopped rebuilding the DOM.
    (state.getCostPeriod as jest.Mock)
      .mockReturnValueOnce('monthly')
      .mockReturnValue('hourly');
    await loadRecommendations();
    const select = document.querySelector<HTMLSelectElement>('#cost-period-select');
    expect(select).not.toBeNull();
    select!.value = 'hourly';
    select!.dispatchEvent(new Event('change'));
    await Promise.resolve();
    expect(state.setCostPeriod).toHaveBeenCalledWith('hourly');
    // The rerender should have rebuilt the savings column header to reflect
    // the new hourly period — pin the DOM update so a future regression that
    // stops invalidating the rendered tree fails this test.
    expect(document.getElementById('recommendations-list')?.textContent).toContain('Savings / hr');
  });

  test('dropdown label text is accessible', async () => {
    await loadRecommendations();
    const label = document.querySelector('.cost-period-selector-label');
    expect(label?.textContent).toContain('Show costs');
  });
});

describe('Issue #319: localStorage persistence (state.ts getCostPeriod / setCostPeriod)', () => {
  // These tests exercise the actual state module (not the mock).
  // We import directly from the source rather than the mocked version.
  // The global setup.ts replaces window.localStorage with a noop mock
  // (getItem→null, setItem→undefined), so we install a Map-backed shim
  // here to actually test persistence semantics, then reset it.
  let getCostPeriodFn: () => string;
  let setCostPeriodFn: (period: string) => void;

  beforeEach(() => {
    const store = new Map<string, string>();
    (localStorage.getItem as jest.Mock).mockImplementation(
      (key: string) => (store.has(key) ? store.get(key)! : null)
    );
    (localStorage.setItem as jest.Mock).mockImplementation(
      (key: string, value: string) => { store.set(key, value); }
    );
    (localStorage.removeItem as jest.Mock).mockImplementation(
      (key: string) => { store.delete(key); }
    );
    (localStorage.clear as jest.Mock).mockImplementation(() => { store.clear(); });

    // Use requireActual to bypass the jest.mock('../state') and get the real
    // module. This lets us test the actual localStorage persistence logic.
    const stateModule = jest.requireActual('../state') as typeof import('../state');
    getCostPeriodFn = stateModule.getCostPeriod as () => string;
    setCostPeriodFn = stateModule.setCostPeriod as (period: string) => void;
    // Reset in-memory costPeriodMemory to the module default before each
    // test, otherwise it survives the localStorage clear and the
    // "invalid value falls back to default" test sees stale 'hourly' from
    // the prior "setCostPeriod persists" test.
    setCostPeriodFn('monthly');
    store.clear();
  });

  afterEach(() => {
    (localStorage.getItem as jest.Mock).mockReset().mockImplementation(() => null);
    (localStorage.setItem as jest.Mock).mockReset().mockImplementation(() => undefined);
    (localStorage.removeItem as jest.Mock).mockReset().mockImplementation(() => undefined);
    (localStorage.clear as jest.Mock).mockReset().mockImplementation(() => undefined);
  });

  test('default period is monthly when localStorage is empty', () => {
    expect(getCostPeriodFn()).toBe('monthly');
  });

  test('setCostPeriod persists to localStorage', () => {
    setCostPeriodFn('hourly');
    expect(localStorage.getItem('cudly.recs.costPeriod')).toBe('hourly');
  });

  test('getCostPeriod reads back the persisted value', () => {
    localStorage.setItem('cudly.recs.costPeriod', 'hourly');
    expect(getCostPeriodFn()).toBe('hourly');
  });

  test('invalid value in localStorage falls back to static default, not prior in-memory state', () => {
    // CR pass-1 nitpick: seed a non-default value first so this test fails if
    // getCostPeriod() incorrectly leaks a stale in-memory cache instead of
    // re-reading + validating localStorage on each call. Without the seed, a
    // broken implementation that only reads localStorage on first call would
    // still pass.
    setCostPeriodFn('hourly');
    expect(getCostPeriodFn()).toBe('hourly');
    localStorage.setItem('cudly.recs.costPeriod', 'invalid-value');
    expect(getCostPeriodFn()).toBe('monthly');
  });
});

// ============================================================================
// Issue #318: Column visibility — localStorage persistence + toggle layer
// ============================================================================
describe('Column visibility (issue #318)', () => {
  beforeEach(() => {
    resetColumnVisibilityState();
    // localStorageMock is cleared by jest.clearAllMocks() in the global beforeEach
    // (setup.ts). Each test below configures the mock as needed.
  });

  // --- loadColumnVisibility ---

  describe('loadColumnVisibility', () => {
    // Nothing saved (or an unreadable entry): the default hidden set, which
    // keeps Monthly Savings in view at a 1440px window.
    const DEFAULTS = ['capacity', 'effective_savings_pct', 'monthly_cost', 'on_demand_monthly', 'usage_history'];

    test('returns the default hidden columns when localStorage key is absent', () => {
      // Default mock: getItem returns null
      const result = loadColumnVisibility();
      expect([...result].sort()).toEqual(DEFAULTS);
    });

    test('returns the default hidden columns on JSON parse error', () => {
      localStorageMock.getItem.mockReturnValue('{not valid json}');
      const result = loadColumnVisibility();
      expect([...result].sort()).toEqual(DEFAULTS);
    });

    test('returns the default hidden columns when schemaVersion is wrong', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 99, hidden: ['count'] }),
      );
      const result = loadColumnVisibility();
      expect([...result].sort()).toEqual(DEFAULTS);
    });

    test('returns the default hidden columns when hidden is not an array', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 1, hidden: 'count' }),
      );
      const result = loadColumnVisibility();
      expect([...result].sort()).toEqual(DEFAULTS);
    });

    test('a saved "show every column" choice beats the defaults', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 1, hidden: [] }),
      );
      expect(loadColumnVisibility().size).toBe(0);
    });

    test('returns the correct hidden set for valid JSON', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 1, hidden: ['count', 'term'] }),
      );
      const result = loadColumnVisibility();
      expect(result.has('count')).toBe(true);
      expect(result.has('term')).toBe(true);
      expect(result.size).toBe(2);
    });

    test('silently drops unknown column keys (forward-compatibility)', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 1, hidden: ['count', 'future_column_not_yet_added'] }),
      );
      const result = loadColumnVisibility();
      // 'count' is a known toggleable column; 'future_column_not_yet_added' is not
      expect(result.has('count')).toBe(true);
      expect(result.size).toBe(1);
    });

    test('silently drops fixed (non-toggleable) column keys', () => {
      // provider/account/service/resource_type are fixed; should be ignored even if stored
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 1, hidden: ['provider', 'account', 'count'] }),
      );
      const result = loadColumnVisibility();
      expect(result.has('provider')).toBe(false);
      expect(result.has('account')).toBe(false);
      expect(result.has('count')).toBe(true);
      expect(result.size).toBe(1);
    });
  });

  // --- saveColumnVisibility ---

  describe('saveColumnVisibility', () => {
    test('writes correct JSON shape to localStorage via setItem', () => {
      const hidden = new Set<import('../state').RecommendationsColumnId>(['count', 'term']);
      saveColumnVisibility(hidden);
      expect(localStorageMock.setItem).toHaveBeenCalledWith(
        'cudly.recs.columnVisibility.v1',
        expect.stringContaining('"schemaVersion":1'),
      );
      const callArg = localStorageMock.setItem.mock.calls[0]?.[1] as string;
      const parsed = JSON.parse(callArg);
      expect(parsed.schemaVersion).toBe(1);
      expect(parsed.hidden).toContain('count');
      expect(parsed.hidden).toContain('term');
      expect(parsed.hidden.length).toBe(2);
    });

    test('writes empty array for no hidden columns', () => {
      saveColumnVisibility(new Set());
      const callArg = localStorageMock.setItem.mock.calls[0]?.[1] as string;
      const parsed = JSON.parse(callArg);
      expect(parsed.hidden).toEqual([]);
    });
  });

  // --- TOGGLEABLE_COLUMNS and COLUMN_DEFS ---

  describe('COLUMN_DEFS and TOGGLEABLE_COLUMNS', () => {
    test('COLUMN_DEFS contains all 15 column ids (13 data + capacity + usage_history sparkline)', () => {
      const keys = COLUMN_DEFS.map((c) => c.key);
      expect(keys).toContain('provider');
      expect(keys).toContain('account');
      expect(keys).toContain('service');
      expect(keys).toContain('resource_type');
      expect(keys).toContain('capacity');
      expect(keys).toContain('region');
      expect(keys).toContain('count');
      expect(keys).toContain('term');
      expect(keys).toContain('payment');
      expect(keys).toContain('savings');
      expect(keys).toContain('upfront_cost');
      expect(keys).toContain('monthly_cost');
      expect(keys).toContain('on_demand_monthly');
      expect(keys).toContain('effective_savings_pct');
      // issue #239: usage_history sparkline column added
      expect(keys).toContain('usage_history');
      expect(COLUMN_DEFS.length).toBe(15);
    });

    test('capacity column appears immediately after resource_type', () => {
      const keys = COLUMN_DEFS.map((c) => c.key);
      expect(keys.indexOf('capacity')).toBe(keys.indexOf('resource_type') + 1);
    });

    test('TOGGLEABLE_COLUMNS excludes fixed identity columns', () => {
      const keys = TOGGLEABLE_COLUMNS.map((c) => c.key);
      expect(keys).not.toContain('provider');
      expect(keys).not.toContain('account');
      expect(keys).not.toContain('service');
      expect(keys).not.toContain('resource_type');
      // All other 11 columns (9 original + capacity + usage_history) should be toggleable.
      expect(keys).toContain('capacity');
      expect(keys).toContain('region');
      expect(keys).toContain('count');
      expect(keys).toContain('term');
      expect(keys).toContain('payment');
      expect(keys).toContain('savings');
      expect(keys).toContain('upfront_cost');
      expect(keys).toContain('monthly_cost');
      expect(keys).toContain('on_demand_monthly');
      expect(keys).toContain('effective_savings_pct');
      expect(keys).toContain('usage_history');
      expect(keys.length).toBe(11);
    });
  });
});

//
// formatCapacity (closes #219)
//
describe('formatCapacity', () => {
  test('returns formatted string when both vcpu and memory_gb are populated', () => {
    expect(formatCapacity(8, 32)).toBe('8 vCPU / 32 GB');
  });

  test('handles fractional memory without trailing zeros', () => {
    expect(formatCapacity(2, 0.5)).toBe('2 vCPU / 0.5 GB');
  });

  test('returns null when vcpu is null', () => {
    expect(formatCapacity(null, 32)).toBeNull();
  });

  test('returns null when memory_gb is null', () => {
    expect(formatCapacity(8, null)).toBeNull();
  });

  test('returns null when vcpu is undefined', () => {
    expect(formatCapacity(undefined, 32)).toBeNull();
  });

  test('returns null when memory_gb is undefined', () => {
    expect(formatCapacity(8, undefined)).toBeNull();
  });

  test('returns null when vcpu is 0 (unknown)', () => {
    expect(formatCapacity(0, 32)).toBeNull();
  });

  test('returns null when memory_gb is 0 (unknown)', () => {
    expect(formatCapacity(8, 0)).toBeNull();
  });
});

//
// Issue #494: deterministic group sort on multi-variant cells.
//
// After PR #195's per-(term, payment) fan-out, every cell has BOTH 1yr and 3yr
// variants. The previous `Math.max(...va.map(numericKey))` cell-score collapses
// to the same value across every cell for Term / Payment / Monthly Cost /
// Effective % / (and many cells for Upfront), producing apparently-random row
// order. These regression tests assert deterministic ordering matching what
// the user sees in the rendered summary row.
//
// PR #491 (closes #480) fixed the default-direction inversion; this PR fixes
// the upstream "every cell ties" symptom.
//
describe('Issue #494: deterministic group sort on multi-variant cells', () => {
  /** Build the minimum DOM loadRecommendations needs, using createElement so
   * no innerHTML assignment is required (the rest of the file uses innerHTML;
   * here we use the safer DOM API to match the test boilerplate pattern in
   * `Bundle B: column header filter triggers`). */
  function buildTestDOM(): void {
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
  }

  /**
   * Build a single multi-variant cell (2 variants: term=1 + term=3) with the
   * provided per-variant payments / upfront / monthly_cost. Cell identity is
   * controlled by `resource_type` so callers can build N distinct cells in one
   * fixture.
   */
  function multiVariantCell(opts: {
    resourceType: string;
    payment1y: string;
    payment3y: string;
    upfront1y: number;
    upfront3y: number;
    monthly1y?: number | null;
    monthly3y?: number | null;
    onDemand?: number | null;
    savings1y?: number;
    savings3y?: number;
  }): LocalRecommendation[] {
    const slug = opts.resourceType.replace(/\W/g, '');
    return [
      {
        id: `${slug}-1y`,
        provider: 'aws',
        cloud_account_id: 'a1',
        service: 'ec2',
        resource_type: opts.resourceType,
        region: 'us-east-1',
        count: 1,
        term: 1,
        payment: opts.payment1y,
        savings: opts.savings1y ?? 100,
        upfront_cost: opts.upfront1y,
        monthly_cost: opts.monthly1y === undefined ? 50 : opts.monthly1y,
        on_demand_cost: opts.onDemand === undefined ? 200 : opts.onDemand,
      } as unknown as LocalRecommendation,
      {
        id: `${slug}-3y`,
        provider: 'aws',
        cloud_account_id: 'a1',
        service: 'ec2',
        resource_type: opts.resourceType,
        region: 'us-east-1',
        count: 1,
        term: 3,
        payment: opts.payment3y,
        savings: opts.savings3y ?? 300,
        upfront_cost: opts.upfront3y,
        monthly_cost: opts.monthly3y === undefined ? 40 : opts.monthly3y,
        on_demand_cost: opts.onDemand === undefined ? 200 : opts.onDemand,
      } as unknown as LocalRecommendation,
    ];
  }

  /**
   * Read the rendered cell order from the DOM. Multi-variant cells render as
   * `tr.rec-cell-summary-row[data-cell-key]`; the test fixtures here use only
   * multi-variant cells (every cell has 2 variants) so this single selector
   * captures the full ordering.
   */
  function renderedCellOrder(): string[] {
    return Array.from(
      document.querySelectorAll<HTMLTableRowElement>('tr.rec-cell-summary-row'),
    ).map((tr) => tr.getAttribute('data-cell-key') ?? '');
  }

  /**
   * Find a key fragment in the rendered order and assert it is actually
   * present. Returns the index. Prevents false-positive `<` comparisons that
   * would silently pass when `findIndex` returns `-1` for both sides (per CR
   * review on the initial commit).
   */
  function indexOrFail(order: string[], keyFragment: string): number {
    const idx = order.findIndex((k) => k.includes(keyFragment));
    expect(idx).toBeGreaterThanOrEqual(0);
    return idx;
  }

  /** Set up the DOM + standard mocks; caller provides the rec list + sort. */
  function setupTestFixture(
    recs: LocalRecommendation[],
    sort: { column: string; direction: 'asc' | 'desc' },
  ): void {
    buildTestDOM();
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: recs,
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    (state.getRecommendationsSort as jest.Mock).mockReturnValue(sort);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
  }

  //
  // 4.9 - Term
  //
  test('Term asc: orders cells by summary.termMin (1yr-grouped before 3yr-grouped)', async () => {
    const cellMixed = multiVariantCell({
      resourceType: 'aaa-mixed', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0,
    });
    // "1yr-only" cell: both variants term=1, different payments so they
    // produce two distinct rec rows under the same cellKey.
    const cell1yOnly: LocalRecommendation[] = [
      { id: 'bbb-1y-nu', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'bbb-1y-only', region: 'us-east-1', count: 1, term: 1,
        payment: 'no-upfront', savings: 100, upfront_cost: 0, monthly_cost: 50 } as unknown as LocalRecommendation,
      { id: 'bbb-1y-au', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'bbb-1y-only', region: 'us-east-1', count: 1, term: 1,
        payment: 'all-upfront', savings: 110, upfront_cost: 800, monthly_cost: 0 } as unknown as LocalRecommendation,
    ];
    const cell3yOnly: LocalRecommendation[] = [
      { id: 'ccc-3y-nu', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'ccc-3y-only', region: 'us-east-1', count: 1, term: 3,
        payment: 'no-upfront', savings: 300, upfront_cost: 0, monthly_cost: 40 } as unknown as LocalRecommendation,
      { id: 'ccc-3y-au', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'ccc-3y-only', region: 'us-east-1', count: 1, term: 3,
        payment: 'all-upfront', savings: 320, upfront_cost: 2400, monthly_cost: 0 } as unknown as LocalRecommendation,
    ];
    // Intentionally wrong insertion order so a no-op sort would fail.
    const recs = [...cell3yOnly, ...cellMixed, ...cell1yOnly];

    setupTestFixture(recs, { column: 'term', direction: 'asc' });
    await loadRecommendations();

    const order = renderedCellOrder();
    // Expected: 1yr-only first (termMin=1, termMax=1), mixed second (termMin=1,
    // termMax=3), 3yr-only last (termMin=3). The encoded score termMin*100 +
    // termMax distinguishes the 1yr-only and mixed cells via termMax.
    expect(indexOrFail(order, 'bbb-1y-only'))
      .toBeLessThan(indexOrFail(order, 'aaa-mixed'));
    expect(indexOrFail(order, 'aaa-mixed'))
      .toBeLessThan(indexOrFail(order, 'ccc-3y-only'));
  });

  test('Term desc: order is the exact reverse of ascending', async () => {
    const cellMixed = multiVariantCell({
      resourceType: 'aaa-mixed', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0,
    });
    const cell1yOnly: LocalRecommendation[] = [
      { id: 'bbb-1y-nu', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'bbb-1y-only', region: 'us-east-1', count: 1, term: 1,
        payment: 'no-upfront', savings: 100, upfront_cost: 0, monthly_cost: 50 } as unknown as LocalRecommendation,
      { id: 'bbb-1y-au', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'bbb-1y-only', region: 'us-east-1', count: 1, term: 1,
        payment: 'all-upfront', savings: 110, upfront_cost: 800, monthly_cost: 0 } as unknown as LocalRecommendation,
    ];
    const cell3yOnly: LocalRecommendation[] = [
      { id: 'ccc-3y-nu', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'ccc-3y-only', region: 'us-east-1', count: 1, term: 3,
        payment: 'no-upfront', savings: 300, upfront_cost: 0, monthly_cost: 40 } as unknown as LocalRecommendation,
      { id: 'ccc-3y-au', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'ccc-3y-only', region: 'us-east-1', count: 1, term: 3,
        payment: 'all-upfront', savings: 320, upfront_cost: 2400, monthly_cost: 0 } as unknown as LocalRecommendation,
    ];
    const recs = [...cellMixed, ...cell1yOnly, ...cell3yOnly];

    setupTestFixture(recs, { column: 'term', direction: 'desc' });
    await loadRecommendations();

    const order = renderedCellOrder();
    expect(indexOrFail(order, 'ccc-3y-only'))
      .toBeLessThan(indexOrFail(order, 'aaa-mixed'));
    expect(indexOrFail(order, 'aaa-mixed'))
      .toBeLessThan(indexOrFail(order, 'bbb-1y-only'));
  });

  //
  // 4.10 - Payment
  //
  test('Payment asc: orders cells by canonical PAYMENT_ORDER (no-upfront < partial-upfront < all-upfront)', async () => {
    // Each cell has term=1 + term=3 variants with the *same* payment so the
    // canonical first-variant payment per cell is unambiguous.
    const cellPartial = multiVariantCell({
      resourceType: 'partial-cell', payment1y: 'partial-upfront', payment3y: 'partial-upfront',
      upfront1y: 200, upfront3y: 600,
    });
    const cellAllUp = multiVariantCell({
      resourceType: 'allup-cell', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 1000, upfront3y: 3000,
    });
    const cellNoUp = multiVariantCell({
      resourceType: 'noup-cell', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0,
    });
    const recs = [...cellAllUp, ...cellPartial, ...cellNoUp];  // intentionally wrong insertion order

    setupTestFixture(recs, { column: 'payment', direction: 'asc' });
    await loadRecommendations();

    const order = renderedCellOrder();
    // Expected semantic order: no-upfront < partial-upfront < all-upfront.
    // Previous alphabetic comparator would have produced all-upfront <
    // no-upfront < partial-upfront, which this test would fail under.
    expect(indexOrFail(order, 'noup-cell'))
      .toBeLessThan(indexOrFail(order, 'partial-cell'));
    expect(indexOrFail(order, 'partial-cell'))
      .toBeLessThan(indexOrFail(order, 'allup-cell'));
  });

  //
  // 4.12 - Upfront Cost
  //
  test('Upfront Cost asc: orders cells by summary.upfrontMin', async () => {
    const cellLow = multiVariantCell({
      resourceType: 'low-upfront', payment1y: 'no-upfront', payment3y: 'partial-upfront',
      upfront1y: 0, upfront3y: 500,
    });
    const cellMid = multiVariantCell({
      resourceType: 'mid-upfront', payment1y: 'partial-upfront', payment3y: 'all-upfront',
      upfront1y: 300, upfront3y: 1500,
    });
    const cellHigh = multiVariantCell({
      resourceType: 'high-upfront', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 800, upfront3y: 2400,
    });
    const recs = [...cellHigh, ...cellMid, ...cellLow];  // wrong insertion order

    setupTestFixture(recs, { column: 'upfront_cost', direction: 'asc' });
    await loadRecommendations();

    const order = renderedCellOrder();
    // Expected: upfrontMin asc -> 0 < 300 < 800.
    expect(indexOrFail(order, 'low-upfront'))
      .toBeLessThan(indexOrFail(order, 'mid-upfront'));
    expect(indexOrFail(order, 'mid-upfront'))
      .toBeLessThan(indexOrFail(order, 'high-upfront'));
  });

  //
  // 4.13 - Monthly Cost
  //
  test('Monthly Cost asc: orders cells by Math.min over non-null variants', async () => {
    const cellLow = multiVariantCell({  // min(30, 20) = 20
      resourceType: 'low-monthly', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, monthly1y: 30, monthly3y: 20,
    });
    const cellMid = multiVariantCell({  // min(60, null) = 60 (best-case-of-known)
      resourceType: 'mid-monthly', payment1y: 'no-upfront', payment3y: 'all-upfront',
      upfront1y: 0, upfront3y: 2400, monthly1y: 60, monthly3y: null,
    });
    const cellHigh = multiVariantCell({  // min(100, 90) = 90
      resourceType: 'high-monthly', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, monthly1y: 100, monthly3y: 90,
    });
    const recs = [...cellHigh, ...cellMid, ...cellLow];

    setupTestFixture(recs, { column: 'monthly_cost', direction: 'asc' });
    await loadRecommendations();

    const order = renderedCellOrder();
    // Expected: 20 < 60 < 90.
    expect(indexOrFail(order, 'low-monthly'))
      .toBeLessThan(indexOrFail(order, 'mid-monthly'));
    expect(indexOrFail(order, 'mid-monthly'))
      .toBeLessThan(indexOrFail(order, 'high-monthly'));
  });

  test('Monthly Cost: cells with all-null monthly_cost sort last in both asc and desc', async () => {
    const cellFinite = multiVariantCell({
      resourceType: 'finite-monthly', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, monthly1y: 50, monthly3y: 40,
    });
    const cellAllNull = multiVariantCell({
      resourceType: 'allnull-monthly', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 1000, upfront3y: 3000, monthly1y: null, monthly3y: null,
    });
    const recs = [...cellAllNull, ...cellFinite];

    // Ascending: finite first, all-null last.
    setupTestFixture(recs, { column: 'monthly_cost', direction: 'asc' });
    await loadRecommendations();
    let order = renderedCellOrder();
    expect(indexOrFail(order, 'finite-monthly'))
      .toBeLessThan(indexOrFail(order, 'allnull-monthly'));

    // Descending: all-null cells STAY last regardless of direction - the
    // POSITIVE_INFINITY null-sentinel logic flips them out of the normal
    // descending order specifically because "no data" rows should be
    // de-emphasised in both directions.
    setupTestFixture(recs, { column: 'monthly_cost', direction: 'desc' });
    await loadRecommendations();
    order = renderedCellOrder();
    expect(indexOrFail(order, 'finite-monthly'))
      .toBeLessThan(indexOrFail(order, 'allnull-monthly'));
  });

  test('Monthly Cost: two all-null cells sort deterministically via cellKey tiebreaker', async () => {
    // Both cells have ALL null monthly_cost - their scores are both
    // POSITIVE_INFINITY. The naive numeric diff would be Infinity - Infinity
    // = NaN; the comparator MUST short-circuit to the cellKey tiebreaker so
    // the two cells render in a stable order across repeated invocations.
    // Closes the gap surfaced by CodeRabbit on the initial #494 commit.
    const cellA = multiVariantCell({
      resourceType: 'aaa-null', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 1000, upfront3y: 3000, monthly1y: null, monthly3y: null,
    });
    const cellB = multiVariantCell({
      resourceType: 'bbb-null', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 2000, upfront3y: 6000, monthly1y: null, monthly3y: null,
    });
    const recs = [...cellB, ...cellA];  // intentionally wrong insertion order

    setupTestFixture(recs, { column: 'monthly_cost', direction: 'asc' });
    await loadRecommendations();
    const first = renderedCellOrder();
    expect(first.length).toBe(2);
    // cellKey contains the resource_type slug, so cellKey for 'aaa-null'
    // sorts before 'bbb-null' under localeCompare.
    expect(indexOrFail(first, 'aaa-null'))
      .toBeLessThan(indexOrFail(first, 'bbb-null'));

    // Re-run to confirm determinism (would fail under Infinity - Infinity = NaN).
    setupTestFixture(recs, { column: 'monthly_cost', direction: 'asc' });
    await loadRecommendations();
    const second = renderedCellOrder();
    expect(second).toEqual(first);
  });

  //
  // 4.15 - Effective %
  //
  test('Effective % asc: orders cells by Math.max over non-null variants (lowest best-pct first)', async () => {
    // effectiveSavingsPct uses on_demand_cost when set. Pick on-demand values
    // so each cell's pct is predictable. Formula:
    //   pct = (savings - upfront/(term*12)) / on_demand * 100
    // low-pct  cell: both variants score ~5%, max = 5%
    const cellLowPct = multiVariantCell({
      resourceType: 'low-pct', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 10, savings3y: 10, onDemand: 200,
    });
    // mid-pct cell: both variants score ~15%, max = 15%
    const cellMidPct = multiVariantCell({
      resourceType: 'mid-pct', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 30, savings3y: 30, onDemand: 200,
    });
    // high-pct cell: both variants score ~25%, max = 25%
    const cellHighPct = multiVariantCell({
      resourceType: 'high-pct', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 50, savings3y: 50, onDemand: 200,
    });
    const recs = [...cellHighPct, ...cellMidPct, ...cellLowPct];

    setupTestFixture(recs, { column: 'effective_savings_pct', direction: 'asc' });
    await loadRecommendations();

    const order = renderedCellOrder();
    // Expected asc: 5% < 15% < 25%.
    expect(indexOrFail(order, 'low-pct'))
      .toBeLessThan(indexOrFail(order, 'mid-pct'));
    expect(indexOrFail(order, 'mid-pct'))
      .toBeLessThan(indexOrFail(order, 'high-pct'));
  });

  test('Effective %: cells with all-null pct sort last regardless of direction', async () => {
    // A cell whose every variant has term=0 returns null from
    // effectiveSavingsPct - that's the null sentinel under test.
    const cellFinite = multiVariantCell({
      resourceType: 'finite-pct', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 20, savings3y: 30, onDemand: 200,
    });
    const cellAllNull: LocalRecommendation[] = [
      { id: 'nullpct-v1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'allnull-pct', region: 'us-east-1', count: 1, term: 0,
        payment: 'no-upfront', savings: 0, upfront_cost: 0, monthly_cost: 50,
        on_demand_cost: 200 } as unknown as LocalRecommendation,
      { id: 'nullpct-v2', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: 'allnull-pct', region: 'us-east-1', count: 1, term: 0,
        payment: 'all-upfront', savings: 0, upfront_cost: 0, monthly_cost: 50,
        on_demand_cost: 200 } as unknown as LocalRecommendation,
    ];
    const recs = [...cellAllNull, ...cellFinite];

    setupTestFixture(recs, { column: 'effective_savings_pct', direction: 'asc' });
    await loadRecommendations();
    let order = renderedCellOrder();
    expect(indexOrFail(order, 'finite-pct'))
      .toBeLessThan(indexOrFail(order, 'allnull-pct'));

    setupTestFixture(recs, { column: 'effective_savings_pct', direction: 'desc' });
    await loadRecommendations();
    order = renderedCellOrder();
    expect(indexOrFail(order, 'finite-pct'))
      .toBeLessThan(indexOrFail(order, 'allnull-pct'));
  });

  //
  // Determinism: repeating the same sort yields the same order.
  //
  // The pre-#494 bug also surfaced as "two clicks of the same header may
  // produce a different broken order" because every cell tied -> fallback to
  // browser sort stability, which is implementation-defined for old V8 builds
  // and varies across JS engines. With the new comparator every cell has a
  // distinct score (or, if genuinely tied, a stable cellKey tiebreaker), so
  // repeated invocations MUST produce the same order.
  //
  // Selection-independent Term sort: cells with different term distributions
  // must sort correctly by cellSummary score (termMin*100+termMax) regardless
  // of which variants the user has selected. Fix for Issue #768: the previous
  // "selected-variant short-circuit" in cellScoreFor() switched the score to
  // the selected variant's individual term value, which caused rows to reorder
  // on every checkbox toggle.
  //
  test('Term sort is selection-independent: cells rank by term distribution not by selected variant', async () => {
    // cell-1y-only: both variants are term=1 (termMin=1, termMax=1, score=101)
    const cell1yOnly: LocalRecommendation[] = [
      { id: '1yonly-nu', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: '1y-only', region: 'us-east-1', count: 1, term: 1,
        payment: 'no-upfront', savings: 100, upfront_cost: 0, monthly_cost: 50 } as unknown as LocalRecommendation,
      { id: '1yonly-au', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: '1y-only', region: 'us-east-1', count: 1, term: 1,
        payment: 'all-upfront', savings: 110, upfront_cost: 800, monthly_cost: 0 } as unknown as LocalRecommendation,
    ];
    // cell-mixed: term=1 and term=3 variants (termMin=1, termMax=3, score=103)
    const cellMixed = multiVariantCell({
      resourceType: 'mixed', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0,
    });
    // cell-3y-only: both variants are term=3 (termMin=3, termMax=3, score=303)
    const cell3yOnly: LocalRecommendation[] = [
      { id: '3yonly-nu', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: '3y-only', region: 'us-east-1', count: 1, term: 3,
        payment: 'no-upfront', savings: 300, upfront_cost: 0, monthly_cost: 40 } as unknown as LocalRecommendation,
      { id: '3yonly-au', provider: 'aws', cloud_account_id: 'a1', service: 'ec2',
        resource_type: '3y-only', region: 'us-east-1', count: 1, term: 3,
        payment: 'all-upfront', savings: 320, upfront_cost: 2400, monthly_cost: 0 } as unknown as LocalRecommendation,
    ];
    const recs = [...cell3yOnly, ...cellMixed, ...cell1yOnly];

    // Select the 3yr variant from the mixed cell. Under the old (buggy) code
    // this caused mixed-cell's score to become 3*100+3=303, tying it with
    // 3y-only and pushing it behind 1y-only in asc order.
    setupTestFixture(recs, { column: 'term', direction: 'asc' });
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(
      new Set(['mixed-3y']),
    );
    await loadRecommendations();

    const order = renderedCellOrder();
    // Expected under term asc (selection must NOT affect order):
    //   1y-only (score=101) < mixed (score=103) < 3y-only (score=303)
    expect(indexOrFail(order, '1y-only'))
      .toBeLessThan(indexOrFail(order, 'mixed'));
    expect(indexOrFail(order, 'mixed'))
      .toBeLessThan(indexOrFail(order, '3y-only'));
  });

  //
  // Issue #768: toggling checkboxes must not change row sort order.
  //
  // Before the fix, cellScoreFor() switched to the selected variant's
  // individual value when selectedRecs contained a variant id, causing
  // groupsInSortOrder() to produce a different order after a checkbox toggle.
  //
  test('Issue #768: row order is identical before and after toggling checkboxes', async () => {
    // Three cells with distinct savings so they sort in a predictable order.
    // Cell A: savings = 10 (lowest) → should be last under desc
    // Cell B: savings = 50 (middle)
    // Cell C: savings = 90 (highest) → should be first under desc
    const cellA = multiVariantCell({
      resourceType: 'cell-A', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 10, savings3y: 15,
    });
    const cellB = multiVariantCell({
      resourceType: 'cell-B', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 50, savings3y: 55,
    });
    const cellC = multiVariantCell({
      resourceType: 'cell-C', payment1y: 'no-upfront', payment3y: 'no-upfront',
      upfront1y: 0, upfront3y: 0, savings1y: 90, savings3y: 95,
    });
    const recs = [...cellA, ...cellB, ...cellC];

    // --- render with no selection ---
    setupTestFixture(recs, { column: 'savings', direction: 'desc' });
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set<string>());
    await loadRecommendations();
    const orderBefore = renderedCellOrder();

    // Verify base order (desc: C > B > A).
    expect(indexOrFail(orderBefore, 'cell-C'))
      .toBeLessThan(indexOrFail(orderBefore, 'cell-B'));
    expect(indexOrFail(orderBefore, 'cell-B'))
      .toBeLessThan(indexOrFail(orderBefore, 'cell-A'));

    // --- simulate toggling two checkboxes (select 1yr variant of A and 3yr of C) ---
    setupTestFixture(recs, { column: 'savings', direction: 'desc' });
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(
      new Set(['cellA-1y', 'cellC-3y']),
    );
    await loadRecommendations();
    const orderAfter = renderedCellOrder();

    // Row order must be identical after selection change.
    expect(orderAfter).toEqual(orderBefore);
  });

  test('Sort is deterministic across repeated renders for each affected column', async () => {
    const cells = [
      multiVariantCell({ resourceType: 'A-cell', payment1y: 'no-upfront',      payment3y: 'all-upfront',     upfront1y: 0,   upfront3y: 1500, monthly1y: 50,  monthly3y: 0,  savings1y: 20, savings3y: 30 }),
      multiVariantCell({ resourceType: 'B-cell', payment1y: 'partial-upfront', payment3y: 'partial-upfront', upfront1y: 300, upfront3y: 900,  monthly1y: 40,  monthly3y: 30, savings1y: 25, savings3y: 35 }),
      multiVariantCell({ resourceType: 'C-cell', payment1y: 'all-upfront',     payment3y: 'no-upfront',      upfront1y: 800, upfront3y: 0,    monthly1y: 60,  monthly3y: 45, savings1y: 30, savings3y: 40 }),
    ].flat();

    const columns: Array<{ column: string; direction: 'asc' | 'desc' }> = [
      { column: 'term',                  direction: 'asc' },
      { column: 'payment',               direction: 'asc' },
      { column: 'upfront_cost',          direction: 'asc' },
      { column: 'monthly_cost',          direction: 'asc' },
      { column: 'effective_savings_pct', direction: 'asc' },
    ];

    for (const sort of columns) {
      setupTestFixture(cells, sort);
      await loadRecommendations();
      const first = renderedCellOrder();
      // Re-run with the SAME inputs.
      setupTestFixture(cells, sort);
      await loadRecommendations();
      const second = renderedCellOrder();
      expect(second).toEqual(first);
      // Must not be the empty / single-element trivial case.
      expect(first.length).toBe(3);
    }
  });

  //
  // QA 4.13 - Monthly Cost: zero-cost (all-upfront) variants must not prevent
  // sort-direction toggle from reordering rows.
  //
  // Before the fix, cells with at least one all-upfront variant (monthly_cost=0)
  // all scored 0 via Math.min(...finite), tying every such cell so the direction
  // multiplier had nothing to act on and subsequent sort clicks were no-ops.
  // The fix uses the minimum NON-ZERO recurring cost, falling back to 0 only
  // when all finite values are 0 (pure all-upfront cell).
  //
  test('Monthly Cost: mixed cell (all-upfront + no-upfront) sorts by non-zero recurring cost, not 0', async () => {
    // cellLow: all-upfront (monthly=0) + no-upfront (monthly=20) -> score = 20
    const cellLow = multiVariantCell({
      resourceType: 'low-mixed', payment1y: 'all-upfront', payment3y: 'no-upfront',
      upfront1y: 500, upfront3y: 0, monthly1y: 0, monthly3y: 20,
    });
    // cellHigh: all-upfront (monthly=0) + no-upfront (monthly=80) -> score = 80
    const cellHigh = multiVariantCell({
      resourceType: 'high-mixed', payment1y: 'all-upfront', payment3y: 'no-upfront',
      upfront1y: 500, upfront3y: 0, monthly1y: 0, monthly3y: 80,
    });
    const recs = [...cellHigh, ...cellLow]; // wrong insertion order

    // Ascending: cellLow (score=20) must precede cellHigh (score=80).
    setupTestFixture(recs, { column: 'monthly_cost', direction: 'asc' });
    await loadRecommendations();
    const ascOrder = renderedCellOrder();
    expect(indexOrFail(ascOrder, 'low-mixed'))
      .toBeLessThan(indexOrFail(ascOrder, 'high-mixed'));

    // Descending: cellHigh (score=80) must now precede cellLow (score=20).
    // This is the toggle that was broken before the fix -- both cells scored 0
    // so direction-flip was a no-op.
    setupTestFixture(recs, { column: 'monthly_cost', direction: 'desc' });
    await loadRecommendations();
    const descOrder = renderedCellOrder();
    expect(indexOrFail(descOrder, 'high-mixed'))
      .toBeLessThan(indexOrFail(descOrder, 'low-mixed'));

    // The two directions must produce OPPOSITE orderings (the toggle is live).
    expect(ascOrder).not.toEqual(descOrder);
  });

  test('Monthly Cost: pure all-upfront cell scores 0 (not POSITIVE_INFINITY) and sorts before all-null cells', async () => {
    // cellPureAllUpfront: both variants have monthly_cost=0 (all-upfront) -> score=0
    const cellPureAllUpfront = multiVariantCell({
      resourceType: 'pure-upfront', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 600, upfront3y: 1800, monthly1y: 0, monthly3y: 0,
    });
    // cellAllNull: both variants have monthly_cost=null -> score=POSITIVE_INFINITY
    const cellAllNull = multiVariantCell({
      resourceType: 'pure-null', payment1y: 'all-upfront', payment3y: 'all-upfront',
      upfront1y: 1000, upfront3y: 3000, monthly1y: null, monthly3y: null,
    });
    const recs = [...cellAllNull, ...cellPureAllUpfront];

    // Ascending: pure-upfront (score=0) must come before all-null (score=+Inf).
    setupTestFixture(recs, { column: 'monthly_cost', direction: 'asc' });
    await loadRecommendations();
    const order = renderedCellOrder();
    expect(indexOrFail(order, 'pure-upfront'))
      .toBeLessThan(indexOrFail(order, 'pure-null'));
  });
});

//
// Issue #479: Select-all checkbox tri-state.
// The header checkbox renders with the right .checked / .indeterminate state
// reflecting current selection vs. the set of best-variant-per-cell recs
// (the set the select-all click actually populates).
//
describe('Issue #479: Select-all header checkbox tri-state', () => {
  const recs = [
    { id: 'r1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
    { id: 'r2', provider: 'aws', cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',     region: 'us-east-1', count: 1, term: 1, savings: 200, upfront_cost: 800 },
  ];

  beforeEach(() => {
    setupOpportunitiesTabDom();
    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
      last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      last_collection_error: null,
    });
  });

  test('with no selection, header checkbox is unchecked and not indeterminate', async () => {
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    await loadRecommendations();
    const cb = document.getElementById('select-all-recs') as HTMLInputElement;
    expect(cb.checked).toBe(false);
    expect(cb.indeterminate).toBe(false);
  });

  test('with a partial selection, header checkbox is indeterminate', async () => {
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['r1']));
    await loadRecommendations();
    const cb = document.getElementById('select-all-recs') as HTMLInputElement;
    expect(cb.indeterminate).toBe(true);
    expect(cb.checked).toBe(false);
  });

  test('with every best-variant selected, header checkbox is checked and not indeterminate', async () => {
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['r1', 'r2']));
    await loadRecommendations();
    const cb = document.getElementById('select-all-recs') as HTMLInputElement;
    expect(cb.checked).toBe(true);
    expect(cb.indeterminate).toBe(false);
  });

  test('clicking when all selected clears selection (the previously-broken second-click path)', async () => {
    // Repro from the issue: with all rows selected, the header checkbox
    // used to render as unchecked, so the second click flipped it to
    // checked and re-selected everything (no-op). Now it renders as
    // checked, so the second click flips to unchecked and clears.
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['r1', 'r2']));
    await loadRecommendations();
    const cb = document.getElementById('select-all-recs') as HTMLInputElement;
    expect(cb.checked).toBe(true);
    // Simulate the browser's pre-change flip from true → false.
    cb.checked = false;
    cb.dispatchEvent(new Event('change'));
    expect(state.clearSelectedRecommendations).toHaveBeenCalled();
  });
});

//
// Issue #480: First-click sort direction per column.
// Text columns and most numerics default to 'asc' (A→Z / low → high).
// `savings` and `on_demand_monthly` keep 'desc' as the platform default.
//
describe('Issue #480: per-column default sort direction', () => {
  const recs = [
    { id: 'r1', provider: 'aws',   cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment: 'no-upfront', savings: 100, upfront_cost: 0,    monthly_cost: 50,  on_demand_cost: 80  },
    { id: 'r2', provider: 'azure', cloud_account_id: 'a2', service: 'vm',  resource_type: 'D2s_v3',    region: 'eastus',    count: 1, term: 1, payment: 'no-upfront', savings: 150, upfront_cost: 1000, monthly_cost: 100, on_demand_cost: 130 },
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

  test.each<[string, 'asc' | 'desc']>([
    ['provider',              'asc'],
    ['account',               'asc'],
    ['service',               'asc'],
    ['resource_type',         'asc'],
    ['region',                'asc'],
    ['count',                 'asc'],
    ['term',                  'asc'],
    ['payment',               'asc'],
    ['upfront_cost',          'asc'],
    ['monthly_cost',          'asc'],
    ['effective_savings_pct', 'asc'],
    ['savings',               'desc'],  // exception per QA notes
    ['on_demand_monthly',     'desc'],  // exception per QA notes
  ])('first click on %s header sorts %s', async (col, expectedDir) => {
    // Pin the active sort to a column distinct from the one we click so
    // the click is treated as a "first click on a previously-unsorted
    // column" rather than a toggle on the active column. Pick `region`
    // unless `region` itself is the column under test, in which case
    // pick `provider`.
    const initialColumn = col === 'region' ? 'provider' : 'region';
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: initialColumn, direction: 'asc' });
    await loadRecommendations();
    const header = document.querySelector<HTMLTableCellElement>(`th[data-sort="${col}"]`);
    expect(header).not.toBeNull();
    header!.click();
    expect(state.setRecommendationsSort).toHaveBeenCalledWith({ column: col, direction: expectedDir });
  });

  test('second click on the same column toggles the direction', async () => {
    // Provider's first click goes to asc (covered above). After
    // state reports the active sort, the next click flips to desc.
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'provider', direction: 'asc' });
    await loadRecommendations();
    const header = document.querySelector<HTMLTableCellElement>('th[data-sort="provider"]');
    header!.click();
    expect(state.setRecommendationsSort).toHaveBeenLastCalledWith({ column: 'provider', direction: 'desc' });
  });
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

  test('valid ?sort=col&dir=asc seeds setRecommendationsSort on load', async () => {
    window.history.replaceState({}, '', '/?sort=upfront_cost&dir=asc');
    await loadRecommendations();
    expect(state.setRecommendationsSort).toHaveBeenCalledWith({ column: 'upfront_cost', direction: 'asc' });
  });

  test('valid ?sort=col&dir=desc seeds setRecommendationsSort on load', async () => {
    window.history.replaceState({}, '', '/?sort=provider&dir=desc');
    await loadRecommendations();
    expect(state.setRecommendationsSort).toHaveBeenCalledWith({ column: 'provider', direction: 'desc' });
  });

  test('invalid ?sort=<unknown> is silently ignored', async () => {
    window.history.replaceState({}, '', '/?sort=not_a_column&dir=asc');
    await loadRecommendations();
    expect(state.setRecommendationsSort).not.toHaveBeenCalledWith(
      expect.objectContaining({ column: 'not_a_column' }),
    );
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

    test('on_demand_monthly: displayPrecision at monthly period matches formatCurrency', async () => {
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
      expect(displayPrecision('on_demand_monthly', 'monthly')).toBe(renderedDigits);
    });

    test('monthly_cost / on_demand_monthly non-monthly periods use PERIOD_DECIMALS-based precision', async () => {
      // Non-monthly periods bypass formatCurrency (see formatCostForPeriod)
      // and use `toFixed(PERIOD_DECIMALS[period])`. Verify the precision
      // values are non-zero where expected so the exact-match filter
      // matches the displayed string (e.g. "$0.1715" at hourly).
      const { displayPrecision } = await import('../recommendations');
      // hourly: 4 dp, daily: 2 dp, yearly: 0 dp (per PERIOD_DECIMALS).
      expect(displayPrecision('monthly_cost', 'hourly')).toBe(4);
      expect(displayPrecision('monthly_cost', 'daily')).toBe(2);
      expect(displayPrecision('monthly_cost', 'yearly')).toBe(0);
      expect(displayPrecision('on_demand_monthly', 'hourly')).toBe(4);
      expect(displayPrecision('on_demand_monthly', 'daily')).toBe(2);
      expect(displayPrecision('on_demand_monthly', 'yearly')).toBe(0);
    });
  });
});

//
// Issue #239: renderUsageSparkline unit tests
//
describe('renderUsageSparkline (issue #239)', () => {
  test('returns em-dash for null', () => {
    expect(renderUsageSparkline(null)).toBe('—');
  });

  test('returns em-dash for undefined', () => {
    expect(renderUsageSparkline(undefined)).toBe('—');
  });

  test('returns em-dash for empty array', () => {
    expect(renderUsageSparkline([])).toBe('—');
  });

  test('returns an SVG element for a single point', () => {
    const html = renderUsageSparkline([75]);
    expect(html).toContain('<svg');
    expect(html).toContain('class="usage-sparkline"');
    // Single-point path uses a circle.
    expect(html).toContain('<circle');
    expect(html).not.toContain('<polyline');
    // Accessible label is present; aria-hidden is not.
    expect(html).toContain('role="img"');
    expect(html).toContain('aria-label=');
    expect(html).not.toContain('aria-hidden');
  });

  test('returns a polyline SVG for 7 points', () => {
    const pcts = [80, 85, 90, 70, 95, 100, 60];
    const html = renderUsageSparkline(pcts);
    expect(html).toContain('<svg');
    expect(html).toContain('class="usage-sparkline"');
    expect(html).toContain('<polyline');
    // 7 points produce 7 coordinate pairs in the points attribute.
    const match = html.match(/points="([^"]+)"/);
    expect(match).not.toBeNull();
    const pairs = match![1]!.trim().split(' ').filter(Boolean);
    expect(pairs).toHaveLength(7);
    // No raw user input is interpolated; values are numbers only, no XSS vector.
    expect(html).not.toContain('<script');
    // Accessible label is present; aria-hidden is not.
    expect(html).toContain('role="img"');
    expect(html).toContain('aria-label=');
    expect(html).not.toContain('aria-hidden');
  });

  test('aria-label conveys coverage values for screen readers', () => {
    const html = renderUsageSparkline([80, 90]);
    expect(html).toContain('aria-label="RI coverage last 2 days: 80.0%, 90.0%"');
  });

  test('non-finite values are clamped to 0 in geometry and label', () => {
    const html = renderUsageSparkline([NaN, Infinity, -Infinity]);
    // All three are treated as 0; label reflects clamped values.
    expect(html).toContain('aria-label="RI coverage last 3 days: 0.0%, 0.0%, 0.0%"');
    // Geometry stays valid (cy must be 19.0 for 0%).
    expect(html).toContain('<polyline');
  });

  test('100% coverage maps point to top of SVG (y near pad)', () => {
    const html = renderUsageSparkline([100]);
    // With pad=1 and innerH=18: y = 1 + 18*(1-100/100) = 1.0
    expect(html).toContain('cy="1.0"');
  });

  test('0% coverage maps point to bottom of SVG (y near h-pad)', () => {
    const html = renderUsageSparkline([0]);
    // With pad=1 and innerH=18: y = 1 + 18*(1-0/100) = 19.0
    expect(html).toContain('cy="19.0"');
  });

  test('first and last x-coordinates span the full SVG width', () => {
    const pcts = [50, 60, 70, 80, 90, 85, 75];
    const html = renderUsageSparkline(pcts);
    const match = html.match(/points="([^"]+)"/);
    expect(match).not.toBeNull();
    const pairs = match![1]!.trim().split(' ').filter(Boolean);
    // First x must be 0.0 (i=0 => x = 0/(7-1)*56 = 0).
    expect(pairs[0]).toMatch(/^0\.0,/);
    // Last x must be 56.0 (i=6 => x = 6/6*56 = 56).
    expect(pairs[6]).toMatch(/^56\.0,/);
  });
});

// ============================================================================
// Issue #1417: sparkline integration — SVG renders inside the table
// ============================================================================
// The unit tests above verify renderUsageSparkline in isolation.  This block
// confirms the SVG actually appears inside the recommendations table HTML when
// a recommendation carries non-empty usage_history data.
describe('Issue #1417: usage-history sparkline renders in recommendations table', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <div id="opportunities-tab" class="tab-content active">
        <div id="recommendations-summary"></div>
        <div id="recommendations-list"></div>
      </div>
      <div id="purchase-modal" class="hidden">
        <div id="purchase-details"></div>
        <div class="modal-buttons">
          <button type="button" id="close-purchase-modal-btn">Cancel</button>
          <button type="button" id="execute-purchase-btn" class="primary">Send for approval</button>
        </div>
      </div>
    `;
    jest.clearAllMocks();
    jest.useFakeTimers();
    (state.getHiddenColumns as jest.Mock).mockReturnValue(new Set());
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getCurrentUser as jest.Mock).mockReturnValue({ id: 'u-admin', email: 'admin@example.com', groups: ['00000000-0000-5000-8000-000000000001'] });
    (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
      last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      last_collection_error: null,
    });
    (recsApi.refreshRecommendations as jest.Mock).mockResolvedValue({});
  });
  afterEach(() => {
    jest.useRealTimers();
  });

  test('SVG polyline appears in table row when rec has 7-day usage_history', async () => {
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: { total_count: 1, total_monthly_savings: 100, total_upfront_cost: 500, avg_payback_months: 5 },
      recommendations: [{
        id: 'rec-sparkline', provider: 'aws', cloud_account_id: 'acct-1',
        service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
        count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500,
        usage_history: [80, 85, 90, 70, 95, 100, 60],
      }],
      regions: [],
    });

    await loadRecommendations();

    const list = document.getElementById('recommendations-list');
    const html = list?.innerHTML ?? '';
    // The sparkline SVG must be present in the rendered table.
    expect(html).toContain('class="usage-sparkline"');
    // It must contain a polyline (7 points -> polyline, not circle).
    expect(html).toContain('<polyline');
    // The sparkline cell must immediately contain the SVG (not the em-dash).
    // This matches the literal pattern produced by renderColumnCell:
    //   <td class="usage-sparkline-cell" title="..."><svg ...>
    expect(html).toContain('usage-sparkline-cell" title="RI coverage last 7 days"><svg');
  });

  test('em-dash renders in table cell when usage_history is null', async () => {
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [{
        id: 'rec-no-sparkline', provider: 'aws', cloud_account_id: 'acct-1',
        service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
        count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500,
        usage_history: null,
      }],
      regions: [],
    });

    await loadRecommendations();

    const list = document.getElementById('recommendations-list');
    const html = list?.innerHTML ?? '';
    // Null usage_history must degrade to the em-dash fallback, not an SVG.
    expect(html).not.toContain('class="usage-sparkline"');
    expect(html).toContain('class="usage-sparkline-cell"');
  });
});

// ============================================================================
// Issue #163: Column filter localStorage persistence
// ============================================================================

describe('Column filters localStorage persistence (issue #163)', () => {
  beforeEach(() => {
    resetColumnFiltersState();
    // localStorageMock is reset by jest.clearAllMocks() in the global beforeEach
  });

  // --- loadColumnFilters ---

  describe('loadColumnFilters', () => {
    test('returns empty object when localStorage key is absent', () => {
      // Default mock: getItem returns null
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });

    test('returns empty object on JSON parse error', () => {
      localStorageMock.getItem.mockReturnValue('{not valid json}');
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });

    test('returns empty object when schemaVersion is wrong', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 99, filters: { region: { kind: 'set', values: ['us-east-1'] } } }),
      );
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });

    test('returns empty object when filters is not an object', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({ schemaVersion: 1, filters: 'not-an-object' }),
      );
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });

    test('restores a filter for every filterable column, including capacity, and drops visual columns', () => {
      const filterable = COLUMN_DEFS.filter((c) => c.kind !== 'visual').map((c) => c.key);
      const visual = COLUMN_DEFS.find((c) => c.kind === 'visual')!.key;
      const filters: Record<string, unknown> = {};
      for (const key of [...filterable, visual]) filters[key] = { kind: 'set', values: ['x'] };
      localStorageMock.getItem.mockReturnValue(JSON.stringify({ schemaVersion: 1, filters }));
      const result = loadColumnFilters();
      expect(Object.keys(result).sort()).toEqual([...filterable].sort());
      expect(result).not.toHaveProperty(visual);
    });

    test('restores a categorical (set) filter correctly', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({
          schemaVersion: 1,
          filters: { region: { kind: 'set', values: ['us-east-1', 'eu-west-1'] } },
        }),
      );
      const result = loadColumnFilters();
      expect(result.region).toEqual({ kind: 'set', values: ['us-east-1', 'eu-west-1'] });
      expect(Object.keys(result)).toHaveLength(1);
    });

    test('restores a numeric (expr) filter correctly', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({
          schemaVersion: 1,
          filters: { savings: { kind: 'expr', expr: '>100' } },
        }),
      );
      const result = loadColumnFilters();
      expect(result.savings).toEqual({ kind: 'expr', expr: '>100' });
    });

    test('silently drops unknown column keys', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({
          schemaVersion: 1,
          filters: {
            region: { kind: 'set', values: ['us-east-1'] },
            future_unknown_column: { kind: 'set', values: ['foo'] },
          },
        }),
      );
      const result = loadColumnFilters();
      expect(result.region).toBeDefined();
      expect((result as Record<string, unknown>)['future_unknown_column']).toBeUndefined();
      expect(Object.keys(result)).toHaveLength(1);
    });

    test('silently drops filters with malformed shape', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({
          schemaVersion: 1,
          filters: {
            region: { kind: 'set', values: 'not-an-array' },
            savings: { kind: 'expr', expr: 42 },
            count: { kind: 'unknown-kind', data: 'x' },
          },
        }),
      );
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });

    test('silently drops expr filter with empty string expr', () => {
      localStorageMock.getItem.mockReturnValue(
        JSON.stringify({
          schemaVersion: 1,
          filters: { savings: { kind: 'expr', expr: '' } },
        }),
      );
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });
  });

  // --- saveColumnFilters ---

  describe('saveColumnFilters', () => {
    test('writes correct JSON shape to localStorage', () => {
      saveColumnFilters({ region: { kind: 'set', values: ['us-east-1'] } });
      expect(localStorageMock.setItem).toHaveBeenCalledWith(
        'cudly.recs.columnFilters.v1',
        expect.stringContaining('"schemaVersion":1'),
      );
      const callArg = localStorageMock.setItem.mock.calls[0]?.[1] as string;
      const parsed = JSON.parse(callArg);
      expect(parsed.schemaVersion).toBe(1);
      expect(parsed.filters.region).toEqual({ kind: 'set', values: ['us-east-1'] });
    });

    test('writes empty filters object when no filters active', () => {
      saveColumnFilters({});
      const callArg = localStorageMock.setItem.mock.calls[0]?.[1] as string;
      const parsed = JSON.parse(callArg);
      expect(parsed.filters).toEqual({});
    });

    test('round-trips a numeric expr filter', () => {
      saveColumnFilters({ savings: { kind: 'expr', expr: '>500' } });
      const callArg = localStorageMock.setItem.mock.calls[0]?.[1] as string;
      const payload = JSON.parse(callArg);
      expect(payload.filters.savings).toEqual({ kind: 'expr', expr: '>500' });
    });
  });

  // --- round-trip and guard tests ---

  describe('persistence round-trip', () => {
    test('save then load restores same filter state', () => {
      const filters = {
        region: { kind: 'set' as const, values: ['ap-southeast-1'] },
        savings: { kind: 'expr' as const, expr: '>200' },
      };
      const store = new Map<string, string>();
      localStorageMock.getItem.mockImplementation((k: string) => store.get(k) ?? null);
      localStorageMock.setItem.mockImplementation((k: string, v: string) => { store.set(k, v); });
      saveColumnFilters(filters);
      const restored = loadColumnFilters();
      expect(restored.region).toEqual({ kind: 'set', values: ['ap-southeast-1'] });
      expect(restored.savings).toEqual({ kind: 'expr', expr: '>200' });
      expect(Object.keys(restored)).toHaveLength(2);
    });

    test('stored key with unknown column is silently dropped, no error', () => {
      const store = new Map<string, string>([
        ['cudly.recs.columnFilters.v1', JSON.stringify({
          schemaVersion: 1,
          filters: {
            region: { kind: 'set', values: ['us-east-1'] },
            deleted_account_col: { kind: 'set', values: ['123456789012'] },
          },
        })],
      ]);
      localStorageMock.getItem.mockImplementation((k: string) => store.get(k) ?? null);
      expect(() => loadColumnFilters()).not.toThrow();
      const result = loadColumnFilters();
      expect(result.region).toBeDefined();
      expect((result as Record<string, unknown>)['deleted_account_col']).toBeUndefined();
    });

    test('corrupted JSON in localStorage falls back to empty object, no error', () => {
      localStorageMock.getItem.mockReturnValue('}{invalid json][');
      expect(() => loadColumnFilters()).not.toThrow();
      const result = loadColumnFilters();
      expect(Object.keys(result)).toHaveLength(0);
    });
  });
});

//
// Issue #135: SP plan-type row grouping in the Recommendations table
//

describe('Issue #135: spGroupKey helper', () => {
  test('returns sp-group| prefix with provider, account_id, region', () => {
    const rec = mkSpRec('savings-plans-compute');
    expect(spGroupKey(rec)).toBe('sp-group|aws|acct-1|us-east-1');
  });

  test('uses empty string for absent cloud_account_id', () => {
    const rec = mkSpRec('savings-plans-compute', { cloud_account_id: undefined });
    expect(spGroupKey(rec)).toBe('sp-group|aws||us-east-1');
  });

  test('different providers produce different keys', () => {
    const awsRec   = mkSpRec('savings-plans-compute', { provider: 'aws'   as never });
    const azureRec = mkSpRec('savingsplans',           { provider: 'azure' as never });
    expect(spGroupKey(awsRec)).not.toBe(spGroupKey(azureRec));
  });
});

describe('Issue #135: groupSpCellKeys helper', () => {
  test('returns empty map when no SP recs are present', () => {
    const recs = [mkRec({ id: 'e1', service: 'ec2' })];
    const groups = groupRecsByCell(recs);
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    expect(result.size).toBe(0);
  });

  test('returns empty map for a single SP plan type (no grouping needed)', () => {
    const recs = [mkSpRec('savings-plans-compute')];
    const groups = groupRecsByCell(recs);
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    expect(result.size).toBe(0);
  });

  test('groups two SP plan types sharing provider+account+region into one entry', () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c' }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s' }),
    ];
    const groups = groupRecsByCell(recs);
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    expect(result.size).toBe(1);
    const [key, cellKeys] = Array.from(result.entries())[0]!;
    expect(key).toBe('sp-group|aws|acct-1|us-east-1');
    expect(cellKeys).toHaveLength(2);
  });

  test('preserves sort order within each group', () => {
    const recs = [
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 300 }),
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-ec2instance', { id: 'sp-e', savings: 200 }),
    ];
    const groups = groupRecsByCell(recs);
    // Present in insertion order (no sort) for this test
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    expect(result.size).toBe(1);
    const cellKeys = Array.from(result.values())[0]!;
    // order must match sortedKeys order (insertion order here)
    expect(cellKeys).toHaveLength(3);
    expect(cellKeys).toEqual(sortedKeys);
  });

  test('does not group SP recs from different accounts', () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-a', cloud_account_id: 'acct-A' }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-b', cloud_account_id: 'acct-B' }),
    ];
    const groups = groupRecsByCell(recs);
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    // Different accounts -> two singleton scopes -> no parent row
    expect(result.size).toBe(0);
  });

  test('does not group SP recs from different regions', () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-a', region: 'us-east-1' }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-b', region: 'eu-west-1' }),
    ];
    const groups = groupRecsByCell(recs);
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    expect(result.size).toBe(0);
  });

  test('non-SP recs in same scope are excluded from the SP group', () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c' }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s' }),
      mkRec({ id: 'ec2-1', service: 'ec2', cloud_account_id: 'acct-1', region: 'us-east-1' }),
    ];
    const groups = groupRecsByCell(recs);
    const sortedKeys = Array.from(groups.keys());
    const result = groupSpCellKeys(sortedKeys, groups);
    // Only the two SP cells form a group; EC2 is not in any SP group.
    expect(result.size).toBe(1);
    const cellKeys = Array.from(result.values())[0]!;
    expect(cellKeys).toHaveLength(2);
  });
});

describe('Issue #135: SP group parent row rendered in table', () => {
  const setupDom = (): void => {
    document.body.innerHTML = [
      '<div id="opportunities-tab" class="tab-content active">',
      '<div id="recommendations-summary"></div>',
      '<div id="recommendations-list"></div>',
      '</div>',
      '<div id="purchase-modal" class="hidden">',
      '<div id="purchase-details"></div>',
      '</div>',
    ].join('');
  };

  beforeEach(() => {
    setupDom();
    jest.clearAllMocks();
    (state.getHiddenColumns as jest.Mock).mockReturnValue(new Set());
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getCostPeriod as jest.Mock).mockReturnValue('monthly');
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
    (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
      last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      last_collection_error: null,
    });
    resetExpandedCells();
  });

  test('two SP plan types render one parent group row and no flat child rows (collapsed)', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    // One parent group row
    const groupRows = document.querySelectorAll('.rec-sp-group-row');
    expect(groupRows.length).toBe(1);

    // No flat recommendation-row children rendered yet (collapsed)
    const recRows = document.querySelectorAll('.recommendation-row');
    expect(recRows.length).toBe(0);
  });

  test('parent row service badge shows combined plan-type label', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    const badge = document.querySelector('.rec-sp-group-badge');
    expect(badge).not.toBeNull();
    const text = badge!.textContent ?? '';
    expect(text).toContain('Savings Plans');
    expect(text).toContain('Compute');
    expect(text).toContain('SageMaker');
  });

  test('parent row badge shows plan type count badge', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    const countBadge = document.querySelector('.rec-sp-plan-count');
    expect(countBadge).not.toBeNull();
    expect(countBadge!.textContent).toContain('2 plan types');
  });

  test('parent row chevron starts collapsed (aria-expanded=false)', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    const chevron = document.querySelector<HTMLButtonElement>('.rec-sp-group-chevron');
    expect(chevron).not.toBeNull();
    expect(chevron!.getAttribute('aria-expanded')).toBe('false');
  });

  test('clicking SP group chevron expands and shows child rows', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    const chevron = document.querySelector<HTMLButtonElement>('.rec-sp-group-chevron');
    chevron!.click();

    // After expand: child rows should appear
    const recRows = document.querySelectorAll('.recommendation-row');
    expect(recRows.length).toBe(2);

    // aria-expanded should flip
    const updatedChevron = document.querySelector<HTMLButtonElement>('.rec-sp-group-chevron');
    expect(updatedChevron!.getAttribute('aria-expanded')).toBe('true');

    // Collapse back: child rows should hide again and aria-expanded should reset.
    updatedChevron!.click();
    expect(document.querySelectorAll('.recommendation-row').length).toBe(0);
    const collapsedChevron = document.querySelector<HTMLButtonElement>('.rec-sp-group-chevron');
    expect(collapsedChevron!.getAttribute('aria-expanded')).toBe('false');
  });

  test('single SP plan type renders as a flat row with no group parent', async () => {
    const recs = [mkSpRec('savings-plans-compute', { id: 'sp-c', savings: 100 })];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    // No SP group parent row
    expect(document.querySelectorAll('.rec-sp-group-row').length).toBe(0);
    // One flat recommendation row rendered directly
    expect(document.querySelectorAll('.recommendation-row').length).toBe(1);
  });

  test('non-SP rows are not absorbed into the SP group', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
      mkRec({ id: 'ec2-1', service: 'ec2', cloud_account_id: 'acct-1', region: 'us-east-1', savings: 50 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    // One SP group parent row
    expect(document.querySelectorAll('.rec-sp-group-row').length).toBe(1);
    // The EC2 row renders as a flat recommendation-row (not inside SP group)
    expect(document.querySelectorAll('.recommendation-row').length).toBe(1);
  });

  test('SP group savings text shows aggregated savings from all plan types', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    await loadRecommendations();

    const groupSavings = document.querySelector('.rec-sp-group-savings');
    expect(groupSavings).not.toBeNull();
    // Should reflect sum: 100 + 200 = 300
    expect(groupSavings!.textContent).toContain('$300');
  });

  test('issue #252: SP group savings does not double-apply the cost-period factor', async () => {
    const recs = [
      mkSpRec('savings-plans-compute',   { id: 'sp-c', savings: 100 }),
      mkSpRec('savings-plans-sagemaker', { id: 'sp-s', savings: 200 }),
    ];
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);

    // Monthly total is $300. On "yearly" (factor 12) the correct figure is
    // $3,600/yr. Pre-fix, scaleCost was applied once outside and again
    // inside formatCostForPeriod, yielding $36,000/yr (12x too high).
    (state.getCostPeriod as jest.Mock).mockReturnValue('yearly');
    await loadRecommendations();

    const groupSavings = document.querySelector('.rec-sp-group-savings');
    expect(groupSavings).not.toBeNull();
    expect(groupSavings!.textContent).toContain('$3600');
    expect(groupSavings!.textContent).not.toContain('$36000');
  });
});

// ============================================================================
// Issue #1419: account display name shown for non-admin users
// ============================================================================
// loadRecommendations must call listAccountsMinimal() (which hits
// /api/accounts/list, accessible to all users with view:recommendations)
// rather than listAccounts() (which hits /api/accounts, admin-only).
// Non-admin users previously saw the raw UUID because listAccounts()
// returned 403, .catch(() => []) silenced it, and the empty cache caused
// the ID to fall through as the display value.
describe('Issue #1419: Opportunities Account column shows display name via listAccountsMinimal', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <div id="opportunities-tab" class="tab-content active">
        <div id="recommendations-summary"></div>
        <div id="recommendations-list"></div>
      </div>
      <div id="purchase-modal" class="hidden">
        <div id="purchase-details"></div>
        <div class="modal-buttons">
          <button type="button" id="close-purchase-modal-btn">Cancel</button>
          <button type="button" id="execute-purchase-btn" class="primary">Send for approval</button>
        </div>
      </div>
    `;
    jest.clearAllMocks();
    jest.useFakeTimers();
    (state.getHiddenColumns as jest.Mock).mockReturnValue(new Set());
    (state.getRecommendationsSort as jest.Mock).mockReturnValue({ column: 'savings', direction: 'desc' });
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getCurrentUser as jest.Mock).mockReturnValue({ id: 'u-viewer', email: 'viewer@example.com', groups: [] });
    (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
      last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      last_collection_error: null,
    });
    (recsApi.refreshRecommendations as jest.Mock).mockResolvedValue({});
  });
  afterEach(() => {
    jest.useRealTimers();
  });

  test('listAccountsMinimal() is called (not listAccounts) so non-admin users get account names', async () => {
    // Simulate listAccountsMinimal succeeding for a non-admin user.
    (api.listAccountsMinimal as jest.Mock).mockResolvedValue([
      { id: 'acct-uuid-1234', name: 'Production AWS', external_id: '123456789', provider: 'aws' },
    ]);
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [{
        id: 'rec-acct', provider: 'aws', cloud_account_id: 'acct-uuid-1234',
        service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
        count: 1, term: 1, payment: 'all-upfront', savings: 50, upfront_cost: 200,
      }],
      regions: [],
    });

    await loadRecommendations();

    // listAccountsMinimal must have been called (not the admin-only listAccounts).
    expect(api.listAccountsMinimal).toHaveBeenCalled();
    expect(api.listAccounts).not.toHaveBeenCalled();

    const list = document.getElementById('recommendations-list');
    const html = list?.innerHTML ?? '';
    // The display name must appear in the rendered row.
    expect(html).toContain('Production AWS');
    // The raw UUID must NOT appear as the cell value.
    expect(html).not.toContain('acct-uuid-1234');
  });

  test('falls back to account ID when listAccountsMinimal returns no matching entry', async () => {
    (api.listAccountsMinimal as jest.Mock).mockResolvedValue([
      { id: 'acct-other', name: 'Other Account', external_id: '999', provider: 'aws' },
    ]);
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [{
        id: 'rec-fallback', provider: 'aws', cloud_account_id: 'acct-uuid-no-match',
        service: 'ec2', resource_type: 't3.medium', region: 'us-east-1',
        count: 1, term: 1, payment: 'all-upfront', savings: 50, upfront_cost: 200,
      }],
      regions: [],
    });

    await loadRecommendations();

    const list = document.getElementById('recommendations-list');
    const html = list?.innerHTML ?? '';
    // No matching entry: falls back to the raw ID.
    expect(html).toContain('acct-uuid-no-match');
  });
});

describe('capacity scaling keeps Eff. % invariant (#136)', () => {
  const rec = {
    id: 'cap', provider: 'azure', service: 'vm', resource_type: 'D2s_v3', region: 'eastus',
    count: 4, term: 1, payment: 'no-upfront', savings: 900, upfront_cost: 0,
    monthly_cost: 1000, on_demand_cost: 1900,
  } as unknown as LocalRecommendation;

  test('on_demand_cost scales with count like savings and upfront', () => {
    const scaled = scaleRecForCapacity(rec, 50)!;
    expect(scaled.count).toBe(2);
    expect(scaled.savings).toBe(450);
    expect(scaled.on_demand_cost).toBe(950);
  });

  test.each([25, 50, 75, 100])('Eff. %% at %i%% capacity equals the unscaled 47.4%%', (capacity) => {
    const scaled = scaleRecForCapacity(rec, capacity)!;
    expect(displaySavingsPct(scaled)!.toFixed(1)).toBe('47.4');
    expect(weightedDisplaySavingsPct([scaled])!.toFixed(1)).toBe('47.4');
  });

  test('an absent on_demand_cost stays absent', () => {
    const scaled = scaleRecForCapacity({ ...rec, on_demand_cost: null } as LocalRecommendation, 50)!;
    expect(scaled.on_demand_cost).toBeNull();
  });
});
