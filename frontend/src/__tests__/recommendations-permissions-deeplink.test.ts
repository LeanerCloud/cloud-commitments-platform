/**
 * Recommendations permission gating tests, part 2: the per-row Plan button
 * deep-link (issue #120) and SP-group child-row checkbox gating (issue #135 +
 * #869). Part 1 is recommendations-permissions.test.ts; the mocks and user
 * builders live in recommendations-permissions-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockOpenCreatePlanModal, mockUser, setupDom, sampleRec } from './recommendations-permissions-harness';
import { loadRecommendations, resetExpandedCells } from '../recommendations';
import * as api from '../api';
import * as state from '../state';

// Shared rec fixture used by the per-row Plan button tests below.
const sampleRec120 = {
  id: 'rec-120',
  provider: 'aws',
  cloud_account_id: 'acc-1',
  service: 'ec2',
  resource_type: 't3.small',
  region: 'us-east-1',
  count: 2,
  term: 1,
  payment: 'all-upfront',
  savings: 150,
  upfront_cost: 500,
};

// Helper: seed the api + state mocks with one rec so the table renders a row.
const seedOneRec = () => {
  (api.getRecommendations as jest.Mock).mockResolvedValue({
    summary: {},
    recommendations: [sampleRec120],
    regions: [],
  });
  (state.getRecommendations as jest.Mock).mockReturnValue([sampleRec120]);
  (state.getVisibleRecommendations as jest.Mock).mockReturnValue([sampleRec120]);
  (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
};

describe('Per-row Plan button deep-link (issue #120)', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    mockOpenCreatePlanModal.mockReset();
    setupDom();
  });

  test('admin sees a per-row Plan button when rows are rendered', async () => {
    mockUser('admin');
    seedOneRec();
    await loadRecommendations();
    const planBtns = document.querySelectorAll<HTMLButtonElement>('button.rec-plan-btn');
    expect(planBtns.length).toBe(1);
    expect(planBtns[0]!.dataset['recId']).toBe('rec-120');
  });

  test('readonly role has no per-row Plan buttons (create:plans gated)', async () => {
    mockUser('readonly');
    seedOneRec();
    await loadRecommendations();
    const planBtns = document.querySelectorAll<HTMLButtonElement>('button.rec-plan-btn');
    expect(planBtns.length).toBe(0);
  });

  test('clicking per-row Plan button calls openCreatePlanModal with the rec pre-seeded', async () => {
    mockUser('admin');
    seedOneRec();
    await loadRecommendations();
    const planBtn = document.querySelector<HTMLButtonElement>('button.rec-plan-btn[data-rec-id="rec-120"]');
    expect(planBtn).not.toBeNull();
    planBtn!.click();
    // openCreatePlanFromBottomBox does `await import('./plans')` which in
    // CommonJS (ts-jest) compiles to Promise.resolve().then(() => require(...)).
    // Flush the microtask queue with a few awaits so the dynamic import
    // resolves and openCreatePlanModal is called before we assert.
    for (let i = 0; i < 5; i++) await Promise.resolve();
    expect(mockOpenCreatePlanModal).toHaveBeenCalledTimes(1);
    const calledWith = mockOpenCreatePlanModal.mock.calls[0]![0] as unknown[];
    expect(Array.isArray(calledWith)).toBe(true);
    expect((calledWith as { id: string }[])[0]!.id).toBe('rec-120');
  });
});

// Issue #135 + #869: SP plan-type group child rows must honor showCheckboxes.
// Two SP recs of different plan types in the same (provider, account, region)
// scope group under one SP parent row. When expanded, each per-plan-type child
// cell renders a variant row via buildVariantRowMarkup. Those nested calls must
// forward showCheckboxes so readonly sessions get no checkbox cells, matching
// the non-SP path and the column header (which omits the select-all checkbox).
describe('Recommendations SP-group child-row checkbox gating (issue #135 + #869)', () => {
  // Two AWS savings-plans recs with distinct plan-type slugs but the same
  // scope -> two cell keys -> one SP group with 2 plan types.
  const spRecCompute = {
    ...sampleRec,
    id: 'sp1',
    service: 'savings-plans-compute',
    resource_type: 'compute',
    savings: 300,
  };
  const spRecEc2 = {
    ...sampleRec,
    id: 'sp2',
    service: 'savings-plans-ec2instance',
    resource_type: 'ec2instance',
    savings: 250,
  };

  beforeEach(() => {
    jest.clearAllMocks();
    // expandedSpGroups is module-level state; reset so the SP group starts
    // collapsed in every test and the chevron click reliably expands it.
    resetExpandedCells();
    setupDom();
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [spRecCompute, spRecEc2],
      regions: [],
    });
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([spRecCompute, spRecEc2]);
  });

  const expandSpGroup = (): HTMLTableElement => {
    const list = document.getElementById('recommendations-list');
    const table = list?.querySelector('table') as HTMLTableElement | null;
    expect(table).not.toBeNull();
    const chevron = table!.querySelector<HTMLButtonElement>('.rec-sp-group-chevron');
    expect(chevron).not.toBeNull();
    chevron!.click();
    return list!.querySelector('table') as HTMLTableElement;
  };

  test('readonly role: expanded SP-group child variant rows have an empty leading checkbox-col but no checkbox input', async () => {
    mockUser('readonly');
    await loadRecommendations();
    const table = expandSpGroup();

    // The SP group expanded into per-plan-type child variant rows.
    const childRows = table.querySelectorAll('tr.rec-variant-row');
    expect(childRows.length).toBeGreaterThan(0);

    // Issue #1006: variant rows carry an empty td.checkbox-col so columns
    // stay aligned with the leading cell in the summary/header rows.  No
    // checkbox input or action buttons appear for readonly sessions.
    for (const row of Array.from(childRows)) {
      expect(row.querySelectorAll('td.checkbox-col').length).toBe(1);
      // The checkbox-col must be the FIRST cell and contain no text (position + empty-content check).
      const firstTd = row.querySelector('td:first-child');
      expect(firstTd?.classList.contains('checkbox-col')).toBe(true);
      expect(firstTd?.textContent?.trim()).toBe('');
      expect(row.querySelectorAll('input[data-rec-id]').length).toBe(0);
    }
  });

  test('admin role: expanded SP-group child variant rows retain checkbox-col', async () => {
    mockUser('admin');
    await loadRecommendations();
    const table = expandSpGroup();

    const childRows = table.querySelectorAll('tr.rec-variant-row');
    expect(childRows.length).toBeGreaterThan(0);

    for (const row of Array.from(childRows)) {
      expect(row.querySelectorAll('td.checkbox-col').length).toBe(1);
    }
  });
});
