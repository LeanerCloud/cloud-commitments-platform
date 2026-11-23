/**
 * Shared setup for the history Marketplace Sell button suite: the module mocks,
 * the admin user fixture, the history DOM builder, the completed-RI row factory
 * and the sell-button id reader. Import this module FIRST so the mocks register
 * before the suite loads ../history. This file is not a test (jest only collects
 * *.test.ts).
 */
jest.mock('../api', () => ({
  getHistory: jest.fn(),
  getConfig: jest.fn().mockResolvedValue({ global: {} }),
  createMarketplaceListing: jest.fn(),
  cancelMarketplaceListing: jest.fn(),
}));

jest.mock('../navigation', () => ({
  switchTab: jest.fn(),
}));

jest.mock('../utils', () => ({
  NO_VALUE: '\u2014',
  formatCurrency: jest.fn((val) => `$${val || 0}`),
  formatDate: jest.fn((val) => (val ? new Date(val).toLocaleDateString() : '')),
  formatTerm: jest.fn((years) => (years == null ? '' : `${years} Year${years === 1 ? '' : 's'}`)),
  escapeHtml: jest.fn((str) => str || ''),
  escapeHtmlAttr: jest.fn((str) => str || ''),
  amortizedMonthly: jest.fn((monthly) => monthly),
  populateAccountFilter: jest.fn(() => Promise.resolve()),
}));

jest.mock('../confirmDialog', () => ({
  confirmDialog: jest.fn(),
}));

jest.mock('../toast', () => ({
  showToast: jest.fn(),
}));

jest.mock('../state', () => ({
  getCurrentUser: jest.fn(),
  getCurrentProvider: jest.fn().mockReturnValue(''),
  setCurrentProvider: jest.fn(),
  getCurrentAccountIDs: jest.fn().mockReturnValue([]),
  setCurrentAccountIDs: jest.fn(),
  subscribeProvider: jest.fn().mockReturnValue(() => {}),
  subscribeAccount: jest.fn().mockReturnValue(() => {}),
  getAmortizeUpfront: jest.fn().mockReturnValue(false),
  setAmortizeUpfront: jest.fn(),
  subscribeAmortizeUpfront: jest.fn().mockReturnValue(() => {}),
  getPurchaseHistoryColumnFilters: jest.fn().mockReturnValue({}),
  setPurchaseHistoryColumnFilter: jest.fn(),
  clearAllPurchaseHistoryColumnFilters: jest.fn(),
  getApprovalQueueColumnFilters: jest.fn().mockReturnValue({}),
  setApprovalQueueColumnFilter: jest.fn(),
  clearAllApprovalQueueColumnFilters: jest.fn(),
}));

// Administrators group GUID -- mirrors ADMINISTRATORS_GROUP_ID in
// frontend/src/permissions.ts. Without this, isAdmin() returns false and
// canAccess('admin', '*') in canSellOnMarketplace's fallback branch does not
// grant. Seeded-group GUID, not the label string.
const ADMIN_GROUP_ID = '00000000-0000-5000-8000-000000000001';
export const ADMIN_USER = { id: 'admin-uuid', email: 'admin@example.com', groups: [ADMIN_GROUP_ID] };

// A recent purchase with plenty of term left so the remaining-months guard in
// canSellOnMarketplace (needs >= 1 month remaining) always passes.
const RECENT = new Date(Date.now() - 30 * 24 * 60 * 60 * 1000).toISOString(); // -30 days

export function setupDOM(): void {
  while (document.body.firstChild) document.body.removeChild(document.body.firstChild);

  const mkInput = (id: string): HTMLInputElement => {
    const el = document.createElement('input');
    el.type = 'date';
    el.id = id;
    return el;
  };
  const mkSelect = (id: string): HTMLSelectElement => {
    const el = document.createElement('select');
    el.id = id;
    const opt = document.createElement('option');
    opt.value = '';
    opt.textContent = 'All';
    el.appendChild(opt);
    return el;
  };
  const mkDiv = (id: string): HTMLDivElement => {
    const el = document.createElement('div');
    el.id = id;
    return el;
  };

  document.body.appendChild(mkInput('history-start'));
  document.body.appendChild(mkInput('history-end'));
  document.body.appendChild(mkSelect('history-provider-filter'));
  document.body.appendChild(mkSelect('history-account-filter'));
  document.body.appendChild(mkDiv('history-summary'));
  document.body.appendChild(mkDiv('history-list'));
  document.body.appendChild(mkDiv('purchases-approval-queue'));
}

// A completed AWS EC2 RI row with a long remaining term. Overrides tune the
// offering_class / provider / service / listing_state for each case.
export function makeRow(overrides: Record<string, unknown>) {
  return {
    purchase_id: 'ri-1',
    timestamp: RECENT,
    provider: 'aws',
    service: 'ec2',
    resource_type: 't3.large',
    region: 'us-east-1',
    count: 1,
    // term is stored in YEARS (1 or 3) in purchase_history; canSellOnMarketplace
    // and the pricing modal multiply by 12 to get months.
    term: 3,
    upfront_cost: 1200,
    estimated_savings: 300,
    plan_name: '',
    status: 'completed',
    offering_class: 'standard',
    ...overrides,
  };
}

export function sellIds(): (string | undefined)[] {
  const list = document.getElementById('history-list')!;
  const buttons = list.querySelectorAll<HTMLButtonElement>('.history-marketplace-sell-btn');
  return Array.from(buttons).map((b) => b.dataset['marketplaceSellId']);
}
