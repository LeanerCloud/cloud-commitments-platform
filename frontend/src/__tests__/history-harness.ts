/**
 * Shared setup for the history module suites: the module mocks and the
 * provider/account mock handles. Import this module FIRST so the mocks
 * register before a suite loads ../history. This file is not a test (jest
 * only collects *.test.ts).
 */
// Mock the dependent modules
jest.mock('../api', () => ({
  getHistory: jest.fn(),
  getConfig: jest.fn().mockResolvedValue({ global: {} }),
}));

jest.mock('../navigation', () => ({
  switchTab: jest.fn()
}));

// Default: the session may view purchases. viewPlanHistory consults this
// before rendering into the Purchases tab, because switchTab renders a
// no-access placeholder there for sessions without view:purchases.
jest.mock('../permissions', () => ({
  canAccess: jest.fn().mockReturnValue(true),
}));

jest.mock('../utils', () => ({
  NO_VALUE: '\u2014',
  // Mirrors the real formatCurrency behaviour: null/undefined/NaN -> '—', numbers -> '$<val>'
  formatCurrency: jest.fn((val) => (val === null || val === undefined || isNaN(val)) ? '—' : `$${val}`),
  formatDate: jest.fn((val) => val ? new Date(val).toLocaleDateString() : ''),
  formatTerm: jest.fn((years) => years == null ? '' : `${years} Year${years === 1 ? '' : 's'}`),
  escapeHtml: jest.fn((str) => str || ''),
  // escapeHtmlAttr must encode " and ' to block attribute-boundary injection.
  // This mock mirrors the real implementation so tests catch regressions where
  // code reverts to the quote-transparent escapeHtml in attribute context.
  escapeHtmlAttr: jest.fn((str: string | null | undefined) => {
    if (!str) return '';
    return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }),
  amortizedMonthly: jest.requireActual('../utils').amortizedMonthly,
  populateAccountFilter: jest.fn(() => Promise.resolve())
}));

// Issue #344 T2: history.ts reads provider/account from the global
// topbar filter via state — mock the module so each test can pre-set
// the values it wants `loadHistory` to read.
export const mockGetCurrentProvider = jest.fn<string, []>().mockReturnValue('all');
export const mockGetCurrentAccountIDs = jest.fn<string[], []>().mockReturnValue([]);
jest.mock('../state', () => ({
  getCurrentUser: jest.fn(),
  getCurrentProvider: () => mockGetCurrentProvider(),
  setCurrentProvider: jest.fn(),
  getCurrentAccountIDs: () => mockGetCurrentAccountIDs(),
  setCurrentAccountIDs: jest.fn(),
  subscribeProvider: jest.fn().mockReturnValue(() => {}),
  subscribeAccount: jest.fn().mockReturnValue(() => {}),
  getAmortizeUpfront: jest.fn().mockReturnValue(false),
  setAmortizeUpfront: jest.fn(),
  subscribeAmortizeUpfront: jest.fn().mockReturnValue(() => {}),
  // History per-column filter accessors (issue #166): tests only exercise
  // empty filter state, so each getter returns {} and each setter is a no-op.
  getPurchaseHistoryColumnFilters: jest.fn().mockReturnValue({}),
  setPurchaseHistoryColumnFilter: jest.fn(),
  clearAllPurchaseHistoryColumnFilters: jest.fn(),
  getApprovalQueueColumnFilters: jest.fn().mockReturnValue({}),
  setApprovalQueueColumnFilter: jest.fn(),
  clearAllApprovalQueueColumnFilters: jest.fn(),
}));
