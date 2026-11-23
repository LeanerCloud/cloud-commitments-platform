/**
 * Shared setup for the riexchange*.test.ts files: module mocks, the captured
 * subscription listeners and the modal fixture. Import it first so the mocks are
 * registered before ../riexchange loads.
 */

// Mock the api module defensively (riexchange.ts imports it)
jest.mock('../api', () => ({
  listConvertibleRIs: jest.fn(),
  listAccountsMinimal: jest.fn(),
  listExchangeableAzureRIs: jest.fn(),
  getRIUtilization: jest.fn(),
  getReshapeRecommendations: jest.fn(),
  getExchangeQuote: jest.fn(),
  executeExchange: jest.fn(),
  getRIExchangeHistoryResponse: jest.fn(),
  getRIExchangeConfig: jest.fn(),
  updateRIExchangeConfig: jest.fn(),
  // listTargetOfferings is called by populateAwsOfferings() in openExchangeModal.
  // Default to returning an empty list so tests that don't care about the picker
  // content remain unaffected. Override per-test for picker-content assertions.
  listTargetOfferings: jest.fn().mockResolvedValue([]),
}));

// Mock navigation to avoid loading dashboard/plans/... transitively.
jest.mock('../navigation', () => ({
  switchTab: jest.fn(),
  switchSettingsSubTab: jest.fn(),
}));

// Capture subscription callbacks so tests can fire them directly.
// These arrays are populated by the mock factory below. Declared with
// `let` so the reference is stable across the hoisted jest.mock call.
export let _providerListeners: Array<() => void> = [];
export let _accountListeners: Array<() => void> = [];
jest.mock('../state', () => ({
  subscribeProvider: jest.fn((cb: () => void) => {
    // _providerListeners may not be initialised yet at hoist time —
    // access it lazily via the closure over the outer `let`.
    _providerListeners = _providerListeners ?? [];
    _providerListeners.push(cb);
    return () => undefined;
  }),
  subscribeAccount: jest.fn((cb: () => void) => {
    _accountListeners = _accountListeners ?? [];
    _accountListeners.push(cb);
    return () => undefined;
  }),
  getCurrentProvider: jest.fn(() => 'aws'),
  getCurrentAccountIDs: jest.fn(() => []),
  getCurrentUser: jest.fn(() => ({ id: 'u', email: 'u@example.com', groups: ['00000000-0000-5000-8000-000000000001'] })),
  // RI Exchange column-filter slice (issue #166 follow-up). Tests don't
  // exercise the filter state directly; an empty record + no-op setters
  // keep renderRecommendations on its happy path.
  getRiExchangeColumnFilters: jest.fn(() => ({})),
  setRiExchangeColumnFilter: jest.fn(),
  clearAllRiExchangeColumnFilters: jest.fn(),
  // Active Convertible RIs column-filter slice (issue #1414).
  getActiveRiColumnFilters: jest.fn(() => ({})),
  setActiveRiColumnFilter: jest.fn(),
  clearAllActiveRiColumnFilters: jest.fn(),
}));

export function createModal(): HTMLDivElement {
  const modal = document.createElement('div');
  modal.id = 'ri-exchange-modal';
  modal.className = 'modal hidden';
  const content = document.createElement('div');
  content.className = 'modal-content';
  modal.appendChild(content);
  document.body.appendChild(modal);
  return modal;
}
