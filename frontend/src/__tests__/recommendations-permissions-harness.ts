/**
 * Shared setup for the recommendations permission suites
 * (recommendations-permissions*.test.ts): the module mocks, the mocked-user
 * builders, the recommendations tab DOM builder and the sample rec fixtures.
 * Import this module FIRST in each suite so the mocks register before the
 * suite loads ../recommendations. This file is not a test (jest only collects
 * *.test.ts).
 */
jest.mock('../api', () => ({
  getRecommendations: jest.fn().mockResolvedValue({ summary: {}, recommendations: [], regions: [] }),
  refreshRecommendations: jest.fn(),
  getConfig: jest.fn().mockResolvedValue({ global: {} }),
  listAccounts: jest.fn().mockResolvedValue([]),
  listAccountsMinimal: jest.fn().mockResolvedValue([]),
  listAccountServiceOverrides: jest.fn().mockResolvedValue([]),
}));

jest.mock('../api/recommendations', () => ({
  getRecommendationsFreshness: jest.fn().mockResolvedValue({
    last_collected_at: new Date().toISOString(),
    last_collection_error: null,
  }),
  refreshRecommendations: jest.fn().mockResolvedValue({}),
}));

jest.mock('../state', () => ({
  getCurrentProvider: jest.fn().mockReturnValue('all'),
  setCurrentProvider: jest.fn(),
  getCurrentAccountIDs: jest.fn().mockReturnValue([]),
  setCurrentAccountIDs: jest.fn(),
  getRecommendations: jest.fn().mockReturnValue([]),
  getRecommendationByID: jest.fn().mockReturnValue(undefined),
  setRecommendations: jest.fn(),
  getSelectedRecommendationIDs: jest.fn().mockReturnValue(new Set()),
  clearSelectedRecommendations: jest.fn(),
  addSelectedRecommendation: jest.fn(),
  removeSelectedRecommendation: jest.fn(),
  getRecommendationsSort: jest.fn().mockReturnValue({ column: 'savings', direction: 'desc' }),
  setRecommendationsSort: jest.fn(),
  getRecommendationsColumnFilters: jest.fn().mockReturnValue({}),
  setRecommendationsColumnFilter: jest.fn(),
  clearAllRecommendationsColumnFilters: jest.fn(),
  getVisibleRecommendations: jest.fn().mockReturnValue([]),
  setVisibleRecommendations: jest.fn(),
  getCostPeriod: jest.fn().mockReturnValue('monthly'),
  setCostPeriod: jest.fn(),
  getHiddenColumns: jest.fn().mockReturnValue(new Set()),
  setHiddenColumns: jest.fn(),
  getCurrentUser: jest.fn(),
}));

jest.mock('../toast', () => ({
  showToast: jest.fn().mockReturnValue({ dismiss: jest.fn() }),
}));

// openCreatePlanFromBottomBox calls `await import('./plans')` at runtime.
// Mock the module so the per-row Plan button click test can assert on
// the call without rendering the full modal DOM.
export const mockOpenCreatePlanModal = jest.fn();
jest.mock('../plans', () => ({
  openCreatePlanModal: (...args: unknown[]) => mockOpenCreatePlanModal(...args),
  setupPlanHandlers: jest.fn(),
  loadPlans: jest.fn().mockResolvedValue(undefined),
  closePlanModal: jest.fn(),
  closePurchaseModal: jest.fn(),
  openNewPlanModal: jest.fn(),
}));

import * as state from '../state';
import { ADMINISTRATORS_GROUP_ID, PURCHASER_GROUP_ID } from '../permissions';

export const mockUser = (role: string | null) => {
  // 'admin' represents a fully-capable admin: Administrators + Purchaser
  // (mirrors the auto-migration that adds existing admins to Purchaser on
  // first deploy of issue #923). Tests that want to assert admin-alone
  // behaviour (no spending access) should call mockUserWithGroups directly.
  (state.getCurrentUser as jest.Mock).mockReturnValue(
    role === null
      ? null
      : { id: 'u', email: 'u@example.com', groups: role === 'admin' ? [ADMINISTRATORS_GROUP_ID, PURCHASER_GROUP_ID] : [] },
  );
};

// Direct group-set mocking for tests that need to assert specific
// group combinations (e.g. admin WITHOUT Purchaser for the carve-out
// regression checks per CR #924 F5).
export const mockUserWithGroups = (groups: string[], effectivePermissions?: { action: string; resource: string }[]) => {
  (state.getCurrentUser as jest.Mock).mockReturnValue(
    { id: 'u', email: 'u@example.com', groups, effectivePermissions },
  );
};

export const setupDom = () => {
  const recsTab = document.createElement('div');
  recsTab.id = 'opportunities-tab';
  recsTab.className = 'tab-content active';
  const summary = document.createElement('div');
  summary.id = 'recommendations-summary';
  const list = document.createElement('div');
  list.id = 'recommendations-list';
  recsTab.appendChild(summary);
  recsTab.appendChild(list);
  document.body.replaceChildren(recsTab);
};

// Minimal recommendation fixture for issue #869 checkbox tests.
export const sampleRec = {
  id: 'r1',
  provider: 'aws',
  cloud_account_id: 'acct1',
  service: 'ec2',
  resource_type: 't3.medium',
  region: 'us-east-1',
  count: 1,
  term: 1,
  payment: 'all-upfront',
  savings: 200,
  upfront_cost: 1000,
  monthly_cost: null,
  on_demand_cost: null,
};

// Two variants sharing a cell key -- same resource_type/region/service/account but
// different term -- so buildListMarkup groups them into a summary row.
export const sampleRecVariantA = { ...sampleRec, id: 'r1', term: 1, payment: 'all-upfront', savings: 200 };
export const sampleRecVariantB = { ...sampleRec, id: 'r2', term: 3, payment: 'no-upfront', savings: 350 };
