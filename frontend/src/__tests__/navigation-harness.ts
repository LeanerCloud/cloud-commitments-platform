/**
 * Shared setup for the navigation suites (navigation*.test.ts): the module mocks
 * and the pathname helper. Import this module FIRST so the mocks register before
 * the suite loads ../navigation. This file is not a test (jest only collects
 * *.test.ts).
 */

// Mock the dependent modules
jest.mock('../dashboard', () => ({
  loadDashboard: jest.fn().mockResolvedValue(undefined)
}));
jest.mock('../recommendations', () => ({
  loadRecommendations: jest.fn().mockResolvedValue(undefined)
}));
jest.mock('../plans', () => ({
  loadPlans: jest.fn().mockResolvedValue(undefined)
}));
jest.mock('../history', () => ({
  initHistoryDateRange: jest.fn(),
  // Issue #340 sub-task: switchTab('purchases') now auto-loads
  // history so the Approval queue card populates without waiting for
  // the "Load History" button click. Stub it so the navigation tests
  // don't make real fetch calls.
  loadHistory: jest.fn().mockResolvedValue(undefined),
}));
jest.mock('../settings', () => ({
  loadGlobalSettings: jest.fn().mockResolvedValue(undefined),
  isUnsavedChanges: jest.fn().mockReturnValue(false),
  loadAccountsTab: jest.fn().mockResolvedValue(undefined),
}));
jest.mock('../ladder-timeline', () => ({
  disposeLadderTimeline: jest.fn(),
}));
jest.mock('../riexchange', () => ({
  loadRIExchange: jest.fn().mockResolvedValue(undefined),
  loadAutomationSettings: jest.fn().mockResolvedValue(undefined),
}));
jest.mock('../auth', () => ({
  isAdmin: jest.fn().mockReturnValue(true),
}));
// Default: all users can view purchases. Individual tests override this.
jest.mock('../permissions', () => ({
  canAccess: jest.fn().mockReturnValue(true),
}));
// Mock inventory so navigation tests stay focused on routing/history and
// don't pull in the real fetch/render machinery. switchInventorySubSection
// must still resolve+return the sub-section (default-first) because
// navigation.switchInventorySubTab uses the return value to build the URL.
jest.mock('../inventory', () => {
  const VALID = ['active-commitments', 'coverage', 'trends', 'ri-exchange'];
  const DEFAULT = 'active-commitments';
  const isValid = (n: string): boolean => VALID.includes(n);
  return {
    DEFAULT_INVENTORY_SUB_SECTION: DEFAULT,
    isValidInventorySubSection: isValid,
    switchInventorySubSection: jest.fn((n: string) => (isValid(n) ? n : DEFAULT)),
    loadInventory: jest.fn(),
  };
});

export function setPathname(pathname: string): void {
  const url = new URL(window.location.href);
  url.pathname = pathname;
  window.history.replaceState(window.history.state, '', url);
}
