/**
 * Shared module mocks for the plans suites (plans*.test.ts split from plans.test.ts).
 * Import this module FIRST so the mocks register before the suites load ../plans.
 * This file is not a test (jest only collects *.test.ts).
 */

// Mock the api module
jest.mock('../api', () => ({
  getPlans: jest.fn(),
  getPlan: jest.fn(),
  getConfig: jest.fn().mockResolvedValue({ global: { auto_purchase_enabled: false } }),
  createPlan: jest.fn(),
  updatePlan: jest.fn(),
  patchPlan: jest.fn(),
  deletePlan: jest.fn(),
  getPlannedPurchases: jest.fn(),
  runPlannedPurchase: jest.fn(),
  pausePlannedPurchase: jest.fn(),
  resumePlannedPurchase: jest.fn(),
  deletePlannedPurchase: jest.fn(),
  createPlannedPurchases: jest.fn(),
  listPlanAccounts: jest.fn().mockResolvedValue([]),
  setPlanAccounts: jest.fn().mockResolvedValue(undefined),
  listAccounts: jest.fn().mockResolvedValue([]),
  // #949/#951: plan-account search + commitment-target prefill now use the
  // minimal-disclosure list (view:recommendations) so Standard / Read-Only
  // users get a populated list instead of a 403 from the view:accounts paths.
  listAccountsMinimal: jest.fn().mockResolvedValue([]),
  getAccount: jest.fn().mockResolvedValue(null)
}));

// Mock state module
jest.mock('../state', () => ({
  getRecommendations: jest.fn().mockReturnValue([]),
  getSelectedRecommendationIDs: jest.fn().mockReturnValue(new Set()),
  // Bundle B (column-filter UX overhaul): savePlan now reads the
  // post-filter visible set so plans never include filtered-out rows.
  // Default to empty so tests that don't seed a target keep their old
  // behaviour (no recommendations attached to plan).
  getVisibleRecommendations: jest.fn().mockReturnValue([]),
  setVisibleRecommendations: jest.fn(),
  // Issue #344 T2: plans.ts reads the global filter via state and
  // subscribes to changes so the list re-renders when the topbar chips
  // change. The topbar's "All providers" chip writes '' to state, which
  // is the falsy "no filter" value plans.ts checks for.
  getCurrentProvider: jest.fn().mockReturnValue(''),
  setCurrentProvider: jest.fn(),
  getCurrentAccountIDs: jest.fn().mockReturnValue([]),
  setCurrentAccountIDs: jest.fn(),
  subscribeProvider: jest.fn().mockReturnValue(() => {}),
  subscribeAccount: jest.fn().mockReturnValue(() => {}),
  // Issue #365: plans.ts now reads getCurrentUser via the permissions
  // helper to decide which row actions to render. Default the existing
  // suite to admin so every legacy assertion (button-text expectations,
  // click-handler counts, plan-card layout) keeps passing unchanged.
  // The permission-gating tests in plans-permissions.test.ts override
  // this per-case to exercise readonly / user / null sessions.
  // ADMINISTRATORS_GROUP_ID literal used here (not imported) because jest.mock
  // factories are hoisted before imports; jest.requireActual also fails here
  // because permissions.ts has a top-level import of ./state which is the very
  // module being mocked (circular init). permissions.test.ts pins the value.
  // Include PURCHASER_GROUP_ID literal alongside ADMINISTRATORS_GROUP_ID so
  // execute:purchases (carved out of admin:*) resolves true for the default
  // admin session. Both are literals to avoid circular-init issues (see
  // comment above). Matches PURCHASER_GROUP_ID from permissions.ts.
  getCurrentUser: jest.fn().mockReturnValue({ id: 'u-admin', email: 'admin@example.com', groups: ['00000000-0000-5000-8000-000000000001', '00000000-0000-5000-8000-000000000007'] }),
  // Issue #166 follow-up: plans.ts now reads per-column filter state via
  // these accessors. Default to "no filters" so legacy assertions keep
  // passing; tests that exercise the popover override per-case.
  getPlansColumnFilters: jest.fn().mockReturnValue({}),
  setPlansColumnFilter: jest.fn(),
  clearAllPlansColumnFilters: jest.fn(),
}));

// Mock history module
jest.mock('../history', () => ({
  viewPlanHistory: jest.fn()
}));

// Mock commitmentOptions module
jest.mock('../commitmentOptions', () => ({
  populateTermSelect: jest.fn(),
  populatePaymentSelect: jest.fn(),
  isValidCombination: jest.fn().mockReturnValue(true),
  normalizePaymentValue: jest.fn((value) => value)
}));

// Mock archera: savePlan must NOT call openArcheraOfferModal after #499.
export const mockOpenArcheraOfferModal = jest.fn();
jest.mock('../archera', () => ({
  openArcheraOfferModal: (...args: unknown[]) => mockOpenArcheraOfferModal(...args),
}));

// Q7: plans.ts migrated alert() → showToast and destructive confirm() →
// confirmDialog. Mock both so tests can assert calls and control confirm.
export const mockShowToast = jest.fn<{ dismiss: () => void }, [unknown]>(() => ({ dismiss: jest.fn() }));
jest.mock('../toast', () => ({
  showToast: (opts: unknown) => mockShowToast(opts),
}));
export const mockConfirmDialog = jest.fn<Promise<boolean>, [unknown]>(() => Promise.resolve(true));
jest.mock('../confirmDialog', () => ({
  confirmDialog: (opts: unknown) => mockConfirmDialog(opts),
}));

// Mock utils
jest.mock('../utils', () => ({
  NO_VALUE: '\u2014',
  formatDate: jest.fn((val) => val ? new Date(val).toLocaleDateString() : ''),
  formatTerm: jest.fn((years) => years == null ? '' : `${years} Year${years === 1 ? '' : 's'}`),
  formatRampSchedule: jest.fn((val) => val || 'Unknown'),
  getStatusBadge: jest.fn(() => ({ class: 'success', label: 'Active' })),
  escapeHtml: jest.fn((str) => str || ''),
  escapeHtmlAttr: jest.fn((str: string | null | undefined) => {
    if (!str) return '';
    return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }),
  formatCurrency: jest.fn((val) => `$${val || 0}`),
  populateAccountFilter: jest.fn(() => Promise.resolve()),
  // providerBadgeHtml added for H1 fix: returns a deterministic span so XSS
  // regression tests can assert neutralisation without a real DOM escaper.
  providerBadgeHtml: jest.fn((p) => {
    const cls = ['aws','azure','gcp'].includes((p||'').toLowerCase()) ? (p||'').toLowerCase() : '';
    const label = (p||'').toUpperCase()
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
    return `<span class="provider-badge ${cls}">${label}</span>`;
  }),
  CURRENCY_DEFAULT_DIGITS: 0,
}));
