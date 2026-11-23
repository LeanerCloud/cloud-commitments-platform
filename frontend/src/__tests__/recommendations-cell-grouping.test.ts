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
  });
});
