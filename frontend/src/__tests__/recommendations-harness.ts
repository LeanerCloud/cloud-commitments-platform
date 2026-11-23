/**
 * Shared module mocks for the recommendations suites (recommendations-*.test.ts split from
 * recommendations.test.ts). Import this module FIRST so the mocks register before the suites
 * load ../recommendations. This file is not a test (jest only collects *.test.ts).
 */

import * as api from '../api';
import * as state from '../state';
import * as recsApi from '../api/recommendations';
import { resetAutoRefreshInFlight } from '../recommendations';
import type { LocalRecommendation } from '../types';

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

/** The "Recommendations Module" describe's beforeEach, shared by every piece that re-opens it. */
export function setupRecommendationsTest(): void {
  // Reset DOM
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
  window.alert = jest.fn();

  // Default freshness for pre-#284 tests: fresh (1h ago) → auto-refresh
  // does NOT fire, so existing tests are unaffected by the new toast calls.
  (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
    last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
    last_collection_error: null,
  });
  (recsApi.refreshRecommendations as jest.Mock).mockResolvedValue({});
  mockShowToast.mockReturnValue({ dismiss: jest.fn() });

  // Reset the auto-refresh in-flight guard AND the once-per-session
  // latch so each test starts with a clean auto-refresh state. Without
  // this, the first stale-load test latches autoRefreshAttempted and
  // every later test in this block would silently skip the auto-refresh.
  resetAutoRefreshInFlight();
}

/** Helper to build a minimal LocalRecommendation fixture. */
export const mkRec = (overrides: Partial<LocalRecommendation> = {}): LocalRecommendation => ({
  id: 'rec-' + Math.random().toString(36).slice(2),
  provider: 'aws',
  service: 'ec2',
  resource_type: 'm5.large',
  region: 'us-east-1',
  count: 3,
  term: 1,
  payment: 'no-upfront',
  savings: 100,
  upfront_cost: 0,
  monthly_cost: 50,
  ...overrides,
} as unknown as LocalRecommendation);

/** Two recs sharing the same cell key (same provider/account/service/region/resource_type/engine). */
export const sameCell = (savingsA: number, savingsB: number): LocalRecommendation[] => [
  mkRec({ id: 'a1', savings: savingsA, term: 1, payment: 'no-upfront', upfront_cost: 0 }),
  mkRec({ id: 'a2', savings: savingsB, term: 3, payment: 'all-upfront', upfront_cost: 1200 }),
];

export const DOM_FOR_319 = (
  '<div id="opportunities-tab" class="tab-content active">'
  + '<div id="recommendations-summary"></div>'
  + '<div id="recommendations-list"></div>'
  + '</div>'
  + '<div id="purchase-modal" class="hidden">'
  + '<div id="purchase-details"></div>'
  + '<div class="modal-buttons">'
  + '<button type="button" id="close-purchase-modal-btn">Cancel</button>'
  + '<button type="button" id="execute-purchase-btn" class="primary">Send for approval</button>'
  + '</div></div>'
);

// Helper used by the issue-#479/#480/#481/#482/#483/#484 describes below to
// set up the same DOM the top-level "Recommendations Module" describe seeds
// in its beforeEach. Each describe block runs at module scope and therefore
// doesn't inherit that hook.
export function setupOpportunitiesTabDom(): void {
  document.body.replaceChildren();
  const recsTab = document.createElement('div');
  recsTab.id = 'opportunities-tab';
  recsTab.className = 'tab-content active';
  const summary = document.createElement('div');
  summary.id = 'recommendations-summary';
  const list = document.createElement('div');
  list.id = 'recommendations-list';
  recsTab.appendChild(summary);
  recsTab.appendChild(list);
  document.body.appendChild(recsTab);
  const purchaseModal = document.createElement('div');
  purchaseModal.id = 'purchase-modal';
  purchaseModal.className = 'hidden';
  const purchaseDetails = document.createElement('div');
  purchaseDetails.id = 'purchase-details';
  purchaseModal.appendChild(purchaseDetails);
  document.body.appendChild(purchaseModal);
}

export const mkSpRec = (service: string, overrides: Partial<LocalRecommendation> = {}): LocalRecommendation => ({
  id: 'sp-' + service + '-' + Math.random().toString(36).slice(2),
  provider: 'aws',
  service,
  resource_type: 'sp',
  region: 'us-east-1',
  cloud_account_id: 'acct-1',
  count: 1,
  term: 1,
  payment: 'no-upfront',
  savings: 100,
  upfront_cost: 0,
  ...overrides,
} as unknown as LocalRecommendation);
