/**
 * Recommendations module tests: SP group helpers, SP group rows, the Account column display name and capacity scaling.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, mkRec, mkSpRec } from './recommendations-harness';
import {
  loadRecommendations,
  groupRecsByCell,
  resetExpandedCells,
  groupSpCellKeys,
} from '../recommendations';
import * as api from '../api';
import * as state from '../state';
import * as recsApi from '../api/recommendations';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
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
});
