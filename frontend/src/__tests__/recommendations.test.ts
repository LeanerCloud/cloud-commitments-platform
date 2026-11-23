/**
 * Recommendations module tests
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, mkRec, mkSpRec } from './recommendations-harness';
import {
  loadRecommendations,
  displaySavingsPct,
  groupRecsByCell,
  resetExpandedCells,
  COLUMN_DEFS,
  loadColumnFilters,
  saveColumnFilters,
  resetColumnFiltersState,
  spGroupKey,
  groupSpCellKeys,
  scaleRecForCapacity,
  weightedDisplaySavingsPct,
} from '../recommendations';
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
