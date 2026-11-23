/**
 * Shared setup for the history Approve button suite: the module mocks, the user
 * and details fixtures, the deferred/settle helpers, the history DOM builder and
 * the purchase row factory. Import this module FIRST so the mocks register
 * before the suite loads ../history. This file is not a test (jest only
 * collects *.test.ts).
 */
import { ADMINISTRATORS_GROUP_ID, PURCHASER_GROUP_ID } from '../permissions';

jest.mock('../api', () => ({
  getHistory: jest.fn(),
  getConfig: jest.fn().mockResolvedValue({ global: {} }),
  approvePurchase: jest.fn(),
  cancelPurchase: jest.fn(),
  getPurchaseDetails: jest.fn(),
  listAccounts: jest.fn().mockResolvedValue([]),
  getDeploymentInfo: jest.fn().mockResolvedValue({}),
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
  escapeHtmlAttr: jest.fn((str: string | null | undefined) => {
    if (!str) return '';
    return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }),
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
  // Issue #344 T2: history.ts reads provider/account from the global
  // topbar filter via state. Default provider 'all' + no account scope
  // preserves the pre-topbar behaviour these tests assume.
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

// Admin user includes Purchaser membership (mirrors the auto-migration for
// existing admins on first deploy of issue #923). approve-any:purchases is
// carved out of admin:* and requires Purchaser group membership.
export const ADMIN_USER = { id: 'admin-uuid', email: 'admin@example.com', groups: [ADMINISTRATORS_GROUP_ID, PURCHASER_GROUP_ID] };
// Admin without Purchaser membership and without effectivePermissions
// for any carved-out spending verb. Issue #923 explicitly carves
// approve-any:purchases / retry-any:purchases / execute:purchases OUT
// of admin:*, so this user MUST NOT see Approve / Retry buttons on
// rows they did not create. Regression guard for CR #924 F5 — if a
// future refactor reintroduces isAdmin() as the gate, this test
// catches it.
export const ADMIN_NO_PURCH = { id: 'admin-no-purch-uuid', email: 'admin-no-purch@example.com', groups: [ADMINISTRATORS_GROUP_ID] };
// REG_USER carries the default-user effective permission set (approve-own
// + cancel-own + retry-own on purchases) so canAccess returns true for
// own-row actions without needing the bootstrap fetch. The previous
// `groups: []` shape relied on the pre-#917 role-string gate that no
// longer exists; the post-rebase canAccess only honors the loaded
// effectivePermissions, so the test fixture must populate it.
export const REG_USER = {
  id: 'user-uuid',
  email: 'user@example.com',
  groups: [],
  effectivePermissions: [
    { action: 'approve-own', resource: 'purchases' },
    { action: 'cancel-own', resource: 'purchases' },
    { action: 'retry-own', resource: 'purchases' },
    { action: 'view', resource: 'history' },
  ],
};
export const OTHER_UUID = 'other-uuid';
export const DETAILS = {
  status: 'pending',
  total_upfront_cost: 100,
  estimated_savings: 10,
  recommendations: [{ provider: 'aws', service: 'ec2', region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront', upfront_cost: 100, savings: 10 }],
};

export function deferred<T>(): { promise: Promise<T>; resolve(value: T): void; reject(error: Error): void } {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

export const settleActions = (): Promise<void> => new Promise(resolve => setTimeout(resolve, 0));

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
  // Issue #340 sub-task: loadHistory now also paints the approval-queue
  // card. The container must exist so renderApprovalQueue's
  // getElementById lookup succeeds.
  document.body.appendChild(mkDiv('purchases-approval-queue'));
}

export function makeRow(overrides: Record<string, unknown>) {
  return {
    purchase_id: 'exec-1',
    timestamp: '2024-01-15T00:00:00Z',
    provider: 'aws',
    service: 'ec2',
    resource_type: 't3.medium',
    region: 'us-east-1',
    count: 1,
    term: 1,
    upfront_cost: 100,
    estimated_savings: 50,
    plan_name: '',
    status: 'pending',
    ...overrides,
  };
}
