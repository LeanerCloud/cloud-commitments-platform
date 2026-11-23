/**
 * Shared setup for the history Revoke button suite: the module mocks, the user
 * fixtures, the future/past timestamps, the history DOM builder, the purchase
 * row factory and the revoke-button id reader. Import this module FIRST so the
 * mocks register before the suite loads ../history. This file is not a test
 * (jest only collects *.test.ts).
 */
jest.mock('../api', () => ({
  getHistory: jest.fn(),
  getConfig: jest.fn().mockResolvedValue({ global: {} }),
  revokePurchase: jest.fn(),
  getRevokeQuote: jest.fn(),
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
// canAccess('admin', '*') / canAccess('revoke-any', 'purchases') in the
// fallback branch don't grant. Seeded-group GUID, not the label string.
const ADMIN_GROUP_ID = '00000000-0000-5000-8000-000000000001';
export const ADMIN_USER = { id: 'admin-uuid', email: 'admin@example.com', groups: [ADMIN_GROUP_ID] };
// Plain authenticated user with no revoke verbs in their effective set --
// used by the RBAC regression test below.
export const NON_REVOKER_USER = {
  id: 'plain-uuid',
  email: 'plain@example.com',
  groups: [],
  effectivePermissions: [
    { action: 'view', resource: 'history' },
  ],
};

// revoke-own only (the default User grant), no admin group (#320).
export const REVOKE_OWN_USER = {
  id: 'own-uuid',
  email: 'own@example.com',
  groups: [],
  effectivePermissions: [
    { action: 'view', resource: 'history' },
    { action: 'revoke-own', resource: 'purchases' },
  ],
};

export const FUTURE = new Date(Date.now() + 5 * 24 * 60 * 60 * 1000).toISOString(); // +5 days
export const PAST = new Date(Date.now() - 1 * 60 * 60 * 1000).toISOString(); // -1 hour

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

export function makeRow(overrides: Record<string, unknown>) {
  return {
    purchase_id: 'commit-1',
    timestamp: '2024-01-15T00:00:00Z',
    provider: 'azure',
    service: 'compute',
    resource_type: 'Standard_D2s_v3',
    region: 'eastus',
    count: 1,
    term: 1,
    upfront_cost: 100,
    estimated_savings: 50,
    plan_name: '',
    status: 'completed',
    ...overrides,
  };
}

export function revokeIds(): (string | undefined)[] {
  const list = document.getElementById('history-list')!;
  const buttons = list.querySelectorAll<HTMLButtonElement>('.history-revoke-btn');
  return Array.from(buttons).map((b) => b.dataset['revokeId']);
}
