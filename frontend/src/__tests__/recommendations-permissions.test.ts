/**
 * Recommendations permission gating tests.
 *
 * Issue #365: bottom-action-box CTA gating.
 *   The bottom-action box stays visible for every signed-in role
 *   (read-only browsing is in scope), but the two mutating CTAs are
 *   hidden when the role lacks the underlying verb:
 *     * #bulk-purchase-btn ("Purchase" one-off) -- `execute:purchases`
 *     * #create-plan-btn   ("Create Plan")       -- `create:plans`
 *
 * Issue #869: checkbox column + row-click gating for viewer (readonly).
 *   Viewer has no purchase/plan actions so the Select-all checkbox,
 *   per-row checkboxes, and row-click selection are all hidden/inert.
 *   Admin and user (operator) roles are unchanged.
 *
 * Issue #120 (per-row Plan deep-link) and the SP-group checkbox gating live in
 * recommendations-permissions-deeplink.test.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import {
  mockUser,
  mockUserWithGroups,
  setupDom,
  sampleRec,
  sampleRecVariantA,
  sampleRecVariantB,
} from './recommendations-permissions-harness';
import { loadRecommendations } from '../recommendations';
import * as api from '../api';
import * as state from '../state';
import { ADMINISTRATORS_GROUP_ID, PURCHASER_GROUP_ID } from '../permissions';

describe('Recommendations action-box permission gating (issue #365)', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    setupDom();
  });

  test('admin sees both Purchase and Create Plan buttons', async () => {
    mockUser('admin');
    await loadRecommendations();
    const purchase = document.getElementById('bulk-purchase-btn') as HTMLButtonElement;
    const plan = document.getElementById('create-plan-btn') as HTMLButtonElement;
    expect(purchase).not.toBeNull();
    expect(plan).not.toBeNull();
    expect(purchase.hidden).toBe(false);
    expect(plan.hidden).toBe(false);
  });

  test('non-admin user hides both Purchase and Create Plan (PR #912: no /me/permissions yet)', async () => {
    // PR #912: canAccess() returns false for non-admin users until the
    // /me/permissions endpoint lands. All non-admin users see the same
    // read-only view as before.
    mockUser('user');
    await loadRecommendations();
    const purchase = document.getElementById('bulk-purchase-btn') as HTMLButtonElement;
    const plan = document.getElementById('create-plan-btn') as HTMLButtonElement;
    expect(purchase.hidden).toBe(true);
    expect(plan.hidden).toBe(true);
  });

  test('readonly role hides both Purchase and Create Plan', async () => {
    mockUser('readonly');
    await loadRecommendations();
    const purchase = document.getElementById('bulk-purchase-btn') as HTMLButtonElement;
    const plan = document.getElementById('create-plan-btn') as HTMLButtonElement;
    expect(purchase.hidden).toBe(true);
    expect(plan.hidden).toBe(true);
  });

  test('null user hides both buttons', async () => {
    mockUser(null);
    await loadRecommendations();
    const purchase = document.getElementById('bulk-purchase-btn') as HTMLButtonElement;
    const plan = document.getElementById('create-plan-btn') as HTMLButtonElement;
    expect(purchase.hidden).toBe(true);
    expect(plan.hidden).toBe(true);
  });

  test('admin WITHOUT Purchaser hides Purchase, keeps Create Plan, shows no-Purchaser notice (CR #924 F3)', async () => {
    // Issue #923 + CR #924 F3: execute:purchases is carved out of
    // admin:*. A bare admin (no Purchaser group, no effectivePermissions
    // yet) sees the Create Plan button (create:plans is NOT carved out)
    // but NOT the Purchase one-off button. The no-Purchaser banner must
    // appear, driven by canAccess('execute', 'purchases') so it stays
    // in lockstep with the Purchase CTA.
    mockUserWithGroups([ADMINISTRATORS_GROUP_ID]);
    await loadRecommendations();
    const purchase = document.getElementById('bulk-purchase-btn') as HTMLButtonElement;
    const plan = document.getElementById('create-plan-btn') as HTMLButtonElement;
    expect(purchase).not.toBeNull();
    expect(plan).not.toBeNull();
    expect(purchase.hidden).toBe(true);
    expect(plan.hidden).toBe(false);
    // No-Purchaser banner present.
    const actionBox = document.getElementById('recommendations-action-box')!;
    const banners = actionBox.querySelectorAll('.info-banner');
    expect(banners.length).toBe(1);
    expect(banners[0]?.textContent).toContain('not execute purchases directly');
    // Row 550: must not imply the user can't plan, and must use the real nav
    // name ("Admin", not "Settings"), and must not claim non-admins can
    // self-serve in Admin → Users.
    expect(banners[0]?.textContent).toContain('Admin → Users');
    expect(banners[0]?.textContent).not.toContain('Settings → Users');
    expect(banners[0]?.textContent).toContain('view and plan');
  });

  test('custom (non-seeded) group with explicit execute:purchases shows Purchase + no banner (CR #924 F3/F4)', async () => {
    // CR #924 F3 + F4: the banner must NOT appear when a custom group
    // (not PURCHASER_GROUP_ID) carries execute:purchases via
    // effectivePermissions. The previous isPurchaser()-only predicate
    // would have shown the contradictory "you can view but not execute"
    // notice alongside a live Purchase CTA.
    const customGid = '00000000-0000-5000-8000-00000000abcd';
    mockUserWithGroups([customGid], [
      { action: 'execute', resource: 'purchases' },
      { action: 'create', resource: 'plans' },
      { action: 'view', resource: 'recommendations' },
    ]);
    await loadRecommendations();
    const purchase = document.getElementById('bulk-purchase-btn') as HTMLButtonElement;
    expect(purchase.hidden).toBe(false);
    // No banner -- the predicate matches the live CTA.
    const actionBox = document.getElementById('recommendations-action-box')!;
    const banners = actionBox.querySelectorAll('.info-banner');
    expect(banners.length).toBe(0);
  });

  // Issue #20: mountBottomActionBox returns early once the box exists, so
  // gating evaluated there was a first-render snapshot. A permission change
  // made in-session must show up on the next render without a reload.
  describe('re-syncs gating when permissions change between renders (#20)', () => {
    const purchaserPerms = [
      { action: 'execute', resource: 'purchases' },
      { action: 'create', resource: 'plans' },
      { action: 'view', resource: 'recommendations' },
    ];
    const viewerPerms = [{ action: 'view', resource: 'recommendations' }];
    const customGid = '00000000-0000-5000-8000-00000000abcd';
    const banners = () =>
      document.getElementById('recommendations-action-box')!.querySelectorAll('.info-banner');
    const purchase = () => document.getElementById('bulk-purchase-btn') as HTMLButtonElement;
    const plan = () => document.getElementById('create-plan-btn') as HTMLButtonElement;

    test('gaining execute:purchases reveals Purchase and drops the banner', async () => {
      mockUserWithGroups([ADMINISTRATORS_GROUP_ID]);
      await loadRecommendations();
      const box = document.getElementById('recommendations-action-box');
      expect(purchase().hidden).toBe(true);
      expect(banners()).toHaveLength(1);

      mockUserWithGroups([ADMINISTRATORS_GROUP_ID, PURCHASER_GROUP_ID], [{ action: 'admin', resource: '*' }, ...purchaserPerms]);
      await loadRecommendations();

      // Same box: the fix must not rebuild the DOM (that would drop an
      // in-progress Capacity value).
      expect(document.getElementById('recommendations-action-box')).toBe(box);
      expect(purchase().hidden).toBe(false);
      expect(banners()).toHaveLength(0);
    });

    test('losing execute:purchases hides Purchase and shows exactly one banner', async () => {
      mockUserWithGroups([customGid], purchaserPerms);
      await loadRecommendations();
      expect(purchase().hidden).toBe(false);
      expect(banners()).toHaveLength(0);

      mockUserWithGroups([customGid], [{ action: 'create', resource: 'plans' }, ...viewerPerms]);
      await loadRecommendations();
      expect(purchase().hidden).toBe(true);
      expect(banners()).toHaveLength(1);

      // A further render with the same permissions must not stack banners.
      await loadRecommendations();
      expect(banners()).toHaveLength(1);
      // The banner sits right after the Purchase button, where it was before.
      expect(purchase().nextElementSibling).toBe(banners()[0]);
    });

    test('Create Plan follows create:plans across renders', async () => {
      mockUserWithGroups([customGid], viewerPerms);
      await loadRecommendations();
      expect(plan().hidden).toBe(true);

      mockUserWithGroups([customGid], [...viewerPerms, { action: 'create', resource: 'plans' }]);
      await loadRecommendations();
      expect(plan().hidden).toBe(false);

      mockUserWithGroups([customGid], viewerPerms);
      await loadRecommendations();
      expect(plan().hidden).toBe(true);
    });
  });

  test('the action-box capacity input stays visible for all sessions', async () => {
    // Non-mutating elements stay visible regardless of group membership;
    // only the action CTAs gate on permissions.
    for (const role of ['admin', 'user', 'readonly']) {
      setupDom();
      mockUser(role);
      await loadRecommendations();
      expect(document.getElementById('bulk-purchase-capacity')).not.toBeNull();
      expect(document.getElementById('recommendations-action-summary')).not.toBeNull();
    }
  });
});

describe('Recommendations checkbox + row-click gating for viewer role (issue #869)', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    setupDom();
    // Seed one recommendation so the table actually renders rows.
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [sampleRec],
      regions: [],
    });
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([sampleRec]);
  });

  test('readonly role: no select-all checkbox in table header', async () => {
    mockUser('readonly');
    await loadRecommendations();
    expect(document.getElementById('select-all-recs')).toBeNull();
  });

  test('readonly role: no per-row checkboxes in table body', async () => {
    mockUser('readonly');
    await loadRecommendations();
    const list = document.getElementById('recommendations-list');
    const rowCheckboxes = list?.querySelectorAll('input[data-rec-id]') ?? [];
    expect(rowCheckboxes.length).toBe(0);
  });

  test('readonly role: row click does not trigger selection state change', async () => {
    mockUser('readonly');
    await loadRecommendations();
    const list = document.getElementById('recommendations-list');
    const row = list?.querySelector<HTMLTableRowElement>('tr.recommendation-row');
    expect(row).not.toBeNull();
    // Simulate a click on the row body (not on a checkbox, which is absent).
    row!.click();
    expect(state.addSelectedRecommendation).not.toHaveBeenCalled();
  });

  test('admin role: select-all checkbox is present', async () => {
    mockUser('admin');
    await loadRecommendations();
    expect(document.getElementById('select-all-recs')).not.toBeNull();
  });

  test('admin role: per-row checkbox is present', async () => {
    mockUser('admin');
    await loadRecommendations();
    const list = document.getElementById('recommendations-list');
    const rowCheckboxes = list?.querySelectorAll('input[data-rec-id]') ?? [];
    expect(rowCheckboxes.length).toBeGreaterThan(0);
  });

  test('non-admin user: no select-all checkbox (PR #912: canAccess returns false without /me/permissions)', async () => {
    // PR #912: only Administrators-group members get checkboxes.
    // The /me/permissions endpoint that would restore Standard Users'
    // checkbox access is deferred to a follow-up.
    mockUser('user');
    await loadRecommendations();
    expect(document.getElementById('select-all-recs')).toBeNull();
  });

  test('non-admin user: no per-row checkboxes (PR #912: canAccess returns false without /me/permissions)', async () => {
    mockUser('user');
    await loadRecommendations();
    const list = document.getElementById('recommendations-list');
    const rowCheckboxes = list?.querySelectorAll('input[data-rec-id]') ?? [];
    expect(rowCheckboxes.length).toBe(0);
  });

  test('readonly role: grouped-row summary has chevron in leading checkbox-col and column span is aligned (closes #1006)', async () => {
    // Two variants share the same cell key -- buildListMarkup renders a summary row.
    // Issue #1006: the expand chevron must sit in td.checkbox-col at the far-left
    // (before Provider), not inline inside the content cell.
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [sampleRecVariantA, sampleRecVariantB],
      regions: [],
    });
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([sampleRecVariantA, sampleRecVariantB]);
    mockUser('readonly');
    await loadRecommendations();

    const list = document.getElementById('recommendations-list');
    const table = list?.querySelector('table');
    expect(table).not.toBeNull();

    const summaryRow = table!.querySelector('tr.rec-cell-summary-row');
    expect(summaryRow).not.toBeNull();

    // Header must have a leading th.checkbox-col (empty for viewers, aligns the chevron column).
    const headerCheckboxCols = table!.querySelectorAll('thead tr th.checkbox-col');
    expect(headerCheckboxCols.length).toBe(1);
    // The leading checkbox-col must be the FIRST header cell (position check).
    const firstHeaderTh = table!.querySelector('thead tr th:first-child');
    expect(firstHeaderTh?.classList.contains('checkbox-col')).toBe(true);

    // Summary row must have a td.checkbox-col at the far-left containing the chevron button.
    const summaryCheckboxCols = summaryRow!.querySelectorAll('td.checkbox-col');
    expect(summaryCheckboxCols.length).toBe(1);
    // The leading checkbox-col must be the FIRST summary cell (position check).
    const firstSummaryTd = summaryRow!.querySelector('td:first-child');
    expect(firstSummaryTd?.classList.contains('checkbox-col')).toBe(true);

    // The chevron button lives inside the leading td.checkbox-col, not inline in the content.
    const chevron = summaryCheckboxCols[0]!.querySelector<HTMLButtonElement>('.rec-cell-chevron');
    expect(chevron).not.toBeNull();
    expect(summaryRow!.querySelector('.rec-cell-summary-content .rec-cell-chevron')).toBeNull();

    // Effective column count: sum of colspan values in each row must match header th count.
    const headerColCount = table!.querySelectorAll('thead tr th').length;
    const summaryEffectiveCols = Array.from(summaryRow!.querySelectorAll('td'))
      .reduce((sum, td) => sum + (td.colSpan || 1), 0);
    expect(summaryEffectiveCols).toBe(headerColCount);
  });

  test('admin role: grouped-row summary retains checkbox-col and column span is aligned', async () => {
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: [sampleRecVariantA, sampleRecVariantB],
      regions: [],
    });
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([sampleRecVariantA, sampleRecVariantB]);
    mockUser('admin');
    await loadRecommendations();

    const list = document.getElementById('recommendations-list');
    const table = list?.querySelector('table');
    expect(table).not.toBeNull();

    const summaryRow = table!.querySelector('tr.rec-cell-summary-row');
    expect(summaryRow).not.toBeNull();

    // Header must have a checkbox-col th for admin.
    const headerCheckboxCols = table!.querySelectorAll('thead tr th.checkbox-col');
    expect(headerCheckboxCols.length).toBe(1);

    // Summary row must have a checkbox-col td for admin.
    const summaryCheckboxCols = summaryRow!.querySelectorAll('td.checkbox-col');
    expect(summaryCheckboxCols.length).toBe(1);

    // Effective column count must match.
    const headerColCount = table!.querySelectorAll('thead tr th').length;
    const summaryEffectiveCols = Array.from(summaryRow!.querySelectorAll('td'))
      .reduce((sum, td) => sum + (td.colSpan || 1), 0);
    expect(summaryEffectiveCols).toBe(headerColCount);
  });
});
