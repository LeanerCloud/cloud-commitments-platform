/**
 * Shared setup for the settings account suites (settings-accounts*.test.ts):
 * the module mocks, the mock handles and the DOM helpers. Import this module
 * FIRST so the mocks register before the suites load ../settings. This file is
 * not a test (jest only collects *.test.ts).
 */

jest.mock('../api', () => ({
  getConfig: jest.fn(),
  updateConfig: jest.fn(),
  listAccounts: jest.fn(),
  createAccount: jest.fn(),
  updateAccount: jest.fn(),
  deleteAccount: jest.fn(),
  testAccountCredentials: jest.fn(),
  saveAccountCredentials: jest.fn(),
  listAccountServiceOverrides: jest.fn(),
  saveAccountServiceOverride: jest.fn(),
  deleteAccountServiceOverride: jest.fn(),
  // Issue #606 — Cancel-All-Then-Delete UX calls cancelPurchase per
  // pending execution before retrying the account delete.
  cancelPurchase: jest.fn(),
  // Issue #734 — the cascade confirm lists each pending purchase.
  getPurchaseDetails: jest.fn()
}));

export const mockShowToast = jest.fn<{ dismiss: () => void }, [unknown]>(() => ({ dismiss: jest.fn() }));
jest.mock('../toast', () => ({
  showToast: (opts: unknown) => mockShowToast(opts)
}));

export const mockConfirmDialog = jest.fn<Promise<boolean>, [unknown]>();
jest.mock('../confirmDialog', () => ({
  confirmDialog: (opts: unknown) => mockConfirmDialog(opts)
}));

// Mock the recommendations module to keep the settings tests focused on
// the settings flows. Issue #196 wires settings.ts to call loadRecommendations
// after override mutations; without this mock the test bundle would pull in
// the full recommendations page module (~2k LOC) and its DOM expectations.
export const mockLoadRecommendations = jest.fn<Promise<void>, []>().mockResolvedValue(undefined);
jest.mock('../recommendations', () => ({
  loadRecommendations: () => mockLoadRecommendations()
}));
