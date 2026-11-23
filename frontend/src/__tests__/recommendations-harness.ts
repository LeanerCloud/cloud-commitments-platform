/**
 * Shared module mocks for the recommendations suites (recommendations-*.test.ts split from
 * recommendations.test.ts). Import this module FIRST so the mocks register before the suites
 * load ../recommendations. This file is not a test (jest only collects *.test.ts).
 */

import * as api from '../api';
import * as state from '../state';
import * as recsApi from '../api/recommendations';

// Mock the api module
jest.mock('../api', () => ({
  getRecommendations: jest.fn(),
  refreshRecommendations: jest.fn(),
  listAccounts: jest.fn().mockResolvedValue([]),
  listAccountsMinimal: jest.fn().mockResolvedValue([]),
  // issue #223: getConfig is fetched on page load to resolve GlobalConfig
  // defaults (DefaultTerm + DefaultPayment). Default-empty global config so
  // pre-#223 tests retain their hardcoded-fallback behavior without extra setup.
  getConfig: jest.fn().mockResolvedValue({ global: {} }),
  // Issue #111: openFanOutModal now pre-fetches per-account service
  // overrides to seed each bucket's Payment default. Default-empty so
  // pre-#111 tests retain their toolbar-seeded behavior without any
  // mock setup; tests that specifically exercise the override seed
  // override this mock per-test.
  listAccountServiceOverrides: jest.fn().mockResolvedValue([]),
}));

// Mock the per-id detail endpoint module so the drawer-fetch tests can
// assert on call shape without going through the apiRequest layer.
// Default resolution returns a benign empty payload so tests that
// merely open + close the drawer (and don't care about the detail
// fetch) don't trip on an undefined-promise return.
//
// Default freshness: fresh (1h ago) so pre-#284 tests that call
// loadRecommendations() don't inadvertently trigger auto-refresh and
// fire extra showToast() calls that would break existing assertions.
// NOTE: ONE_HOUR_AGO can't be used here because jest.mock() factory
// functions are hoisted before variable declarations. The date is
// computed inline; the beforeEach block resets it via the mock handle.
jest.mock('../api/recommendations', () => ({
  getRecommendationDetail: jest.fn().mockResolvedValue({
    id: 'rec-default',
    usage_history: [],
    confidence_bucket: 'low',
    provenance_note: '',
  }),
  getRecommendationsFreshness: jest.fn().mockResolvedValue({
    last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
    last_collection_error: null,
  }),
  refreshRecommendations: jest.fn().mockResolvedValue({}),
}));

// Mock showToast so auto-refresh (#284) tests can assert on toast calls
// without touching the DOM. Returns a dismiss handle per the ToastHandle
// interface so callers that invoke handle.dismiss() don't crash.
export const mockShowToast = jest.fn<{ dismiss: () => void }, [unknown]>(() => ({ dismiss: jest.fn() }));

jest.mock('../toast', () => ({
  showToast: (opts: unknown) => mockShowToast(opts),
}));

// Mock state module
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
  // Bundle A column-filter accessors (default empty filters → applyColumnFilters is a no-op).
  getRecommendationsColumnFilters: jest.fn().mockReturnValue({}),
  setRecommendationsColumnFilter: jest.fn(),
  clearAllRecommendationsColumnFilters: jest.fn(),
  getVisibleRecommendations: jest.fn().mockReturnValue([]),
  setVisibleRecommendations: jest.fn(),
  // issue #319: cost-period selector. Default to 'monthly' so pre-#319 tests
  // see unchanged behaviour (monthly is the identity factor).
  getCostPeriod: jest.fn().mockReturnValue('monthly'),
  setCostPeriod: jest.fn(),
  // issue #318: column visibility (default: all visible — empty hidden set)
  getHiddenColumns: jest.fn().mockReturnValue(new Set()),
  setHiddenColumns: jest.fn(),
  // Issue #365: mountBottomActionBox calls canAccess() which reads the
  // current user role. Default to admin so the bottom-bar button-presence
  // tests below see both #bulk-purchase-btn and #create-plan-btn
  // unconditionally. Permission-gating coverage lives in
  // recommendations-permissions.test.ts.
  getCurrentUser: jest.fn().mockReturnValue({ id: 'u-admin', email: 'admin@example.com', groups: ['00000000-0000-5000-8000-000000000001'] }),
  // Issue #477: setupRecommendationsHandlers subscribes to provider/account
  // changes; expose jest.fn() shims so tests can capture the callback and
  // simulate a change without going through the real listener set.
  subscribeProvider: jest.fn(),
  subscribeAccount: jest.fn(),
}));

// Mock utils
jest.mock('../utils', () => ({
  // Explicit digits (the non-monthly cost periods) render fixed decimals;
  // null/undefined render the shared NO_VALUE placeholder like the real one.
  formatCurrency: jest.fn((val, _currency = '$', digits?: number) => (
    val === null || val === undefined ? '\u2014' : digits ? `$${Number(val).toFixed(digits)}` : `$${val || 0}`
  )),
  NO_VALUE: '\u2014',
  formatTerm: jest.fn((years) => years == null ? '' : `${years} Year${years === 1 ? '' : 's'}`),
  escapeHtml: jest.fn((str) => str || ''),
  populateAccountFilter: jest.fn(() => Promise.resolve()),
  // CURRENCY_DEFAULT_DIGITS must be re-exported by the mock so that
  // recommendations.ts (which imports it for its filter precision logic)
  // sees the same default-digit count as the real utils.ts. Without this,
  // displayPrecision's currency-column branches return undefined and the
  // filter exact-match path breaks.
  CURRENCY_DEFAULT_DIGITS: 0,
}));

// Issue #803: setup.ts's jest.clearAllMocks() clears recorded calls but keeps
// any mockReturnValue / mockResolvedValue / mockImplementation a test
// installed, so an override (cost period, column filters, selection, user,
// account overrides, ...) leaked into whichever test jest ran next and the
// file failed under --randomize. Snapshot every module mock's factory default
// once, and restore it before each test, so each test starts from the
// defaults above whatever order the tests run in.
export const mockFactoryDefaults = [...[api, state, recsApi].flatMap((mod) => Object.values(mod)), mockShowToast]
  .filter((fn): fn is jest.Mock => jest.isMockFunction(fn))
  .map((fn) => [fn, fn.getMockImplementation()] as const);
