/**
 * Shared setup for the dashboard suites (dashboard*.test.ts): the module mocks and
 * the mock handles they expose. Import this module FIRST so the mocks register
 * before the suites load ../dashboard. This file is not a test (jest only collects
 * *.test.ts).
 */

// Mock Chart.js - must be done before import
// Q4/Q7: dashboard now uses showToast and confirmDialog instead of
// alert/confirm. Mock both so tests can assert on calls.
export const mockShowToast = jest.fn<{ dismiss: () => void }, [unknown]>(() => ({ dismiss: jest.fn() }));
jest.mock('../toast', () => ({
  showToast: (opts: unknown) => mockShowToast(opts),
}));
export const mockConfirmDialog = jest.fn<Promise<boolean>, [unknown]>(() => Promise.resolve(true));
jest.mock('../confirmDialog', () => ({
  confirmDialog: (opts: unknown) => mockConfirmDialog(opts),
}));

jest.mock('chart.js', () => {
  const MockChart = jest.fn().mockImplementation(() => ({
    destroy: jest.fn()
  }));
  (MockChart as unknown as { register: jest.Mock }).register = jest.fn();
  return {
    Chart: MockChart,
    registerables: []
  };
});

// #293: mock the recommendations helpers imported by dashboard.ts so
// tests can control the range computation without loading the full
// recommendations module (which itself imports chart.js etc.).
export const mockGroupRecsByCell = jest.fn((recs: unknown[]) => new Map(recs.length ? [['cell-1', recs]] : []));
export const mockPageLevelRange = jest.fn((groups: Map<string, unknown[]>) => {
  if (groups.size === 0) return { savingsMin: 0, savingsMax: 0, cellCount: 0 };
  return { savingsMin: 300, savingsMax: 400, cellCount: groups.size };
});
export const mockFormatSavingsRange = jest.fn((min: number, max: number) => min === max ? `$${min}` : `$${min} – $${max}`);
jest.mock('../recommendations', () => ({
  groupRecsByCell: (recs: unknown[]) => mockGroupRecsByCell(recs),
  pageLevelRange: (groups: Map<string, unknown[]>) => mockPageLevelRange(groups),
  formatSavingsRange: (min: number, max: number) => mockFormatSavingsRange(min, max),
  // Freshness-bar deletion (#284 follow-up): dashboard's loadDashboard
  // now calls triggerAutoRefreshIfStale(loadDashboard) so the 24h
  // auto-refresh + collection-error toast also fire on Home, not just
  // on Opportunities. Stub it as a resolved no-op so dashboard tests
  // don't see a real /freshness round-trip.
  triggerAutoRefreshIfStale: jest.fn(() => Promise.resolve()),
}));
// Mock the api module. `getSavingsAnalytics` is re-exported from
// ../api/history via ../api/index, so mock it here for dashboard's
// `import * as api from './api'` usage in loadSavingsTrendChart.
jest.mock('../api', () => ({
  getDashboardSummary: jest.fn(),
  getUpcomingPurchases: jest.fn(),
  getPurchaseDetails: jest.fn(),
  cancelPurchase: jest.fn(),
  // Plan-level cancel — the dashboard upcoming-list targets plan
  // endpoints, not execution endpoints. PR #207 first wired this to
  // deletePlannedPurchase but that handler still operates on
  // purchase_executions; the correct endpoint is DELETE /api/plans/{id}
  // via api.deletePlan (issues #204 / #205 / #208).
  deletePlannedPurchase: jest.fn(),
  deletePlan: jest.fn(),
  listAccounts: jest.fn().mockResolvedValue([]),
  getSavingsAnalytics: jest.fn().mockResolvedValue({ data_points: [] }),
  // #293: dashboard fetches recs for per-cell savings range. Default to
  // empty list so existing tests that don't care about the savings card
  // value don't need to be updated.
  getRecommendations: jest.fn().mockResolvedValue([]),
  // #180: Home applies the Settings -> Enabled Providers filter, like the
  // Opportunities page. Default to "no preference" (no filter).
  getConfig: jest.fn().mockResolvedValue({ global: {} }),
}));

// Mock state module
jest.mock('../state', () => ({
  // Issue #344 T2: AppState.currentProvider is `api.Provider | ''` —
  // '' is the topbar's "All Providers" sentinel, not the string 'all'.
  getCurrentProvider: jest.fn().mockReturnValue(''),
  setCurrentProvider: jest.fn(),
  getCurrentAccountIDs: jest.fn().mockReturnValue([]),
  setCurrentAccountIDs: jest.fn(),
  getSavingsChart: jest.fn().mockReturnValue(null),
  setSavingsChart: jest.fn(),
  // Global topbar filter subscriptions. Each section calls subscribe*
  // during setup to register its reload callback.
  subscribeProvider: jest.fn().mockReturnValue(() => {}),
  subscribeAccount: jest.fn().mockReturnValue(() => {}),
  // Issue #950: the dashboard upcoming-purchase widget now gates the
  // Cancel button on creator-scope ownership (canCancelUpcomingPurchase
  // -> canAccess + getCurrentUser). Default the session to an admin so
  // the pre-#950 cancel-flow tests below keep exercising the click path;
  // the dedicated ownership tests override this per-test.
  getCurrentUser: jest.fn().mockReturnValue({
    id: 'admin-user',
    email: 'admin@example.com',
    groups: ['00000000-0000-5000-8000-000000000001'],
    effectivePermissions: [{ action: 'admin', resource: '*' }],
  }),
}));

// Mock utils
jest.mock('../utils', () => ({
  NO_VALUE: '\u2014',
  formatCurrency: jest.fn((val) => {
    const n = typeof val === 'number' ? val : Number(val);
    if (val == null || !Number.isFinite(n)) return '—';
    return `$${n}`;
  }),
  getDateParts: jest.fn(() => ({ day: 15, month: 'Jan' })),
  escapeHtml: jest.fn((str) => str || ''),
  populateAccountFilter: jest.fn(() => Promise.resolve()),
  // providerBadgeClass added for L4/D1 fix: whitelist-based CSS class helper.
  providerBadgeClass: jest.fn((p) => ['aws', 'azure', 'gcp'].includes((p || '').toLowerCase()) ? (p || '').toLowerCase() : ''),
}));

import { dashboardState } from '../dashboardflow/dashboardState';
import { setupSavingsTrendHandlers } from '../dashboardflow/trend';

/**
 * Reset the dashboard module state that survives between tests in one file
 * (jest.clearAllMocks does not touch it): the chart instances, which gate the
 * loading skeletons (a chart drawn by an earlier test suppresses them), and the
 * trend range (#1972). The range has no public reset, so click a lone 90d
 * button; with no trend canvas in the DOM the click only records the range.
 * Call it first in beforeEach, before the test DOM is built.
 */
export function resetDashboardState(): void {
  dashboardState.savingsTrendChart = null;
  dashboardState.savingsByServiceChart = null;
  dashboardState.lastSavingsByServiceArgs = null;
  const b90 = document.createElement('button');
  b90.className = 'trend-range';
  b90.dataset['range'] = '90';
  document.body.replaceChildren(b90);
  setupSavingsTrendHandlers();
  b90.click();
}
