/**
 * Shared setup for the plan range validation suites (plans-range-validation*.test.ts):
 * the module mocks and the DOM builders. Import this module FIRST so the mocks
 * register before the suite loads ../plans. This file is not a test (jest only
 * collects *.test.ts).
 */
import type { ToastOptions } from '../toast';

jest.mock('../api', () => ({
  getPlans: jest.fn(),
  getPlannedPurchases: jest.fn(),
  getPlan: jest.fn(),
  createPlan: jest.fn(),
  updatePlan: jest.fn(),
  patchPlan: jest.fn(),
  deletePlan: jest.fn(),
  runPlannedPurchase: jest.fn(),
  pausePlannedPurchase: jest.fn(),
  resumePlannedPurchase: jest.fn(),
  deletePlannedPurchase: jest.fn(),
  createPlannedPurchases: jest.fn(),
  listPlanAccounts: jest.fn().mockResolvedValue([]),
  setPlanAccounts: jest.fn().mockResolvedValue(undefined),
  listAccounts: jest.fn().mockResolvedValue([]),
}));

jest.mock('../state', () => ({
  getRecommendations: jest.fn().mockReturnValue([]),
  getSelectedRecommendationIDs: jest.fn().mockReturnValue(new Set()),
  getVisibleRecommendations: jest.fn().mockReturnValue([]),
  setVisibleRecommendations: jest.fn(),
  getCurrentProvider: jest.fn().mockReturnValue(''),
  setCurrentProvider: jest.fn(),
  getCurrentAccountIDs: jest.fn().mockReturnValue([]),
  setCurrentAccountIDs: jest.fn(),
  subscribeProvider: jest.fn().mockReturnValue(() => {}),
  subscribeAccount: jest.fn().mockReturnValue(() => {}),
  getCurrentUser: jest.fn().mockReturnValue({ id: 'u-admin', email: 'admin@example.com', groups: ['00000000-0000-5000-8000-000000000001'] }),
  getPlansColumnFilters: jest.fn().mockReturnValue({}),
  setPlansColumnFilter: jest.fn(),
  clearAllPlansColumnFilters: jest.fn(),
}));

jest.mock('../history', () => ({ viewPlanHistory: jest.fn() }));

jest.mock('../commitmentOptions', () => ({
  populateTermSelect: jest.fn(),
  populatePaymentSelect: jest.fn(),
  isValidCombination: jest.fn().mockReturnValue(true),
  normalizePaymentValue: jest.fn((value) => value),
}));

export const mockShowToast = jest.fn((_opts: ToastOptions) => ({ dismiss: jest.fn() }));
jest.mock('../toast', () => ({
  showToast: (opts: ToastOptions) => mockShowToast(opts),
}));

jest.mock('../confirmDialog', () => ({
  confirmDialog: jest.fn(() => Promise.resolve(true)),
}));

jest.mock('../utils', () => ({
  NO_VALUE: '\u2014',
  formatDate: jest.fn((v) => v ?? ''),
  formatTerm: jest.fn((y) => `${y}yr`),
  formatRampSchedule: jest.fn((v) => v ?? ''),
  getStatusBadge: jest.fn(() => ({ class: 'success', label: 'Active' })),
  escapeHtml: jest.fn((s) => s ?? ''),
  escapeHtmlAttr: jest.fn((str: string | null | undefined) => {
    if (str == null) return '';
    return str
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }),
  formatCurrency: jest.fn((v) => `$${v}`),
  populateAccountFilter: jest.fn(() => Promise.resolve()),
}));
