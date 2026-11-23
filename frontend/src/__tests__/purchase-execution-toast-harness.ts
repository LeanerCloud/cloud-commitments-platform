/**
 * Shared setup for the purchase execution toast suites
 * (purchase-execution-toast*.test.ts): the module mocks and the helpers.
 * Import this module FIRST so the mocks register before the suites load ../app.
 * This file is not a test (jest only collects *.test.ts).
 */

// ── mocks (must precede imports) ─────────────────────────────────────────────

jest.mock('../api', () => ({
  initAuth: jest.fn(),
  isAuthenticated: jest.fn(),
  getCurrentUser: jest.fn(),
  executePurchase: jest.fn(),
}));

jest.mock('../state', () => ({
  setCurrentUser: jest.fn(),
  setCurrentProvider: jest.fn(),
}));

jest.mock('../auth', () => ({
  showLoginModal: jest.fn(),
  updateUserUI: jest.fn(),
}));

jest.mock('../dashboard', () => ({
  loadDashboard: jest.fn().mockResolvedValue(undefined),
  setupDashboardHandlers: jest.fn(),
}));

jest.mock('../navigation', () => ({
  switchTab: jest.fn(),
  applyTabFromPath: jest.fn().mockReturnValue('dashboard'),
  initRouter: jest.fn(),
  switchSettingsSubTab: jest.fn(),
  getSettingsSubTabFromPath: jest.fn().mockReturnValue('general'),
}));

jest.mock('../recommendations', () => ({
  setupRecommendationsHandlers: jest.fn(),
  getPurchaseModalRecommendations: jest.fn(),
  getPurchaseModalSession: jest.fn().mockReturnValue(1),
  clearPurchaseModalRecommendations: jest.fn(),
  getFanOutBuckets: jest.fn(),
  clearFanOutBuckets: jest.fn(),
  // app.ts calls getExecuteMode() and clearExecuteMode() in
  // handleExecutePurchase (issue #289 / PR #924 carve-out).
  // Default getExecuteMode to '' (approval path) so existing toast tests
  // that don't exercise the direct-execute path are unaffected.
  getExecuteMode: jest.fn().mockReturnValue(''),
  clearExecuteMode: jest.fn(),
  fanOutBucketAccountLabel: jest.fn(
    (b: { recs: Array<{ cloud_account_id?: string }> }) => b.recs[0]?.cloud_account_id || 'Unattributed',
  ),
  // The confirm summaries (single and fan-out, #707/#711) name each line's account.
  getAccountName: jest.fn((id: string) => id),
}));

jest.mock('../plans', () => ({
  savePlan: jest.fn(),
  setupPlanHandlers: jest.fn(),
  closePlanModal: jest.fn(),
  openNewPlanModal: jest.fn(),
  closePurchaseModal: jest.fn(),
}));

jest.mock('../settings', () => ({
  saveGlobalSettings: jest.fn(),
  setupSettingsHandlers: jest.fn(),
  resetSettings: jest.fn(),
}));

jest.mock('../riexchange', () => ({
  setupRIExchangeHandlers: jest.fn(),
  saveAutomationSettings: jest.fn(),
}));

jest.mock('../users', () => ({
  setupUserHandlers: jest.fn(),
}));

jest.mock('../apikeys', () => ({
  initApiKeys: jest.fn(),
}));

jest.mock('../history', () => ({
  loadHistory: jest.fn(),
  setupHistoryHandlers: jest.fn(),
}));

jest.mock('../modules/savings-history', () => ({
  initSavingsHistory: jest.fn(),
}));

jest.mock('../purchases-deeplink', () => ({
  handlePurchaseDeeplink: jest.fn(),
}));

jest.mock('../modal', () => ({
  closeModal: jest.fn(),
}));

// confirmDialog defaults to true (user confirms); individual tests can override
jest.mock('../confirmDialog', () => ({
  confirmDialog: jest.fn().mockResolvedValue(true),
}));

// Archera offer modal: after issue #499 follow-up the offer must fire on the
// success path of the approval-submission call (right after the user approves
// the pre-purchase confirmation), not after the async execution completes.
jest.mock('../archera', () => ({
  handleArcheraDeeplink: jest.fn(),
  openArcheraOfferModal: jest.fn(),
}));

export {};
