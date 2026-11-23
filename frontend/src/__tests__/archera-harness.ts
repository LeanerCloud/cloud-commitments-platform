/**
 * Shared setup for the Archera suite (archera*.test.ts): the module mocks and
 * the overlay / offer-modal container builders. Import this module FIRST so the
 * mocks register before the suite loads ../archera. This file is not a test
 * (jest only collects *.test.ts).
 */
// ---------------------------------------------------------------------------
// Mocks required by recommendations.ts and plans.ts
// ---------------------------------------------------------------------------

jest.mock('../api', () => ({
  getRecommendations: jest.fn(),
  refreshRecommendations: jest.fn(),
  listAccounts: jest.fn().mockResolvedValue([]),
  getConfig: jest.fn().mockResolvedValue({ global: {} }),
  listAccountServiceOverrides: jest.fn().mockResolvedValue([]),
  getPlans: jest.fn(),
  getPlannedPurchases: jest.fn().mockResolvedValue({ purchases: [] }),
  listPlanAccounts: jest.fn().mockResolvedValue([]),
  setPlanAccounts: jest.fn().mockResolvedValue(undefined),
}));

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

jest.mock('../toast', () => ({
  showToast: jest.fn(() => ({ dismiss: jest.fn() })),
}));

jest.mock('../state', () => ({
  getCurrentProvider: jest.fn().mockReturnValue('all'),
  getRecommendations: jest.fn().mockReturnValue([]),
  getSelectedRecommendationIDs: jest.fn().mockReturnValue(new Set()),
  getVisibleRecommendations: jest.fn().mockReturnValue([]),
  setVisibleRecommendations: jest.fn(),
}));

jest.mock('../history', () => ({
  viewPlanHistory: jest.fn(),
}));

jest.mock('../commitmentOptions', () => ({
  populateTermSelect: jest.fn(),
  populatePaymentSelect: jest.fn(),
  isValidCombination: jest.fn().mockReturnValue(true),
  normalizePaymentValue: jest.fn((v: string) => v),
}));

jest.mock('../confirmDialog', () => ({
  confirmDialog: jest.fn(() => Promise.resolve(true)),
}));

// ---------------------------------------------------------------------------
// DOM setup helpers
// ---------------------------------------------------------------------------

/** Minimal DOM structure used by the education overlay tests. */
export function buildArcheraContainer(): void {
  const container = document.createElement('div');
  container.id = 'archera-page-container';
  container.className = 'hidden';
  document.body.appendChild(container);
}

/** Minimal DOM structure used by the offer modal tests. */
export function buildArcheraOfferContainer(): void {
  const container = document.createElement('div');
  container.id = 'archera-offer-modal-container';
  container.className = 'hidden';
  document.body.appendChild(container);
}
