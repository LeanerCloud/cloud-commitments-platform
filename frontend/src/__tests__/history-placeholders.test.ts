/** Issue #1681: Purchase History placeholders and cancelled-row identity. */

import { loadHistory } from '../history';

jest.mock('../api', () => ({
  getHistory: jest.fn(),
  getConfig: jest.fn().mockResolvedValue({ global: {} }),
  approvePurchase: jest.fn(),
  cancelPurchase: jest.fn(),
  getPurchaseDetails: jest.fn().mockResolvedValue({
    total_upfront_cost: 100,
    estimated_savings: 10,
    recommendations: [{ provider: 'aws', service: 'ec2', region: 'us-east-1', count: 1, term: 1, upfront_cost: 100, savings: 10 }],
  }),
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
  escapeHtml: jest.requireActual('../utils').escapeHtml,
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

jest.mock('../recommendations', () => ({
  getAccountName: jest.fn((id: string) => id),
}));

import * as api from '../api';
import { getCurrentUser } from '../state';
import { ADMINISTRATORS_GROUP_ID } from '../permissions';

const ADMIN_USER = { id: 'admin-uuid', email: 'admin@example.com', groups: [ADMINISTRATORS_GROUP_ID] };

function setupDOM(): void {
  while (document.body.firstChild) document.body.removeChild(document.body.firstChild);

  const mkInput = (id: string): HTMLInputElement => {
    const el = document.createElement('input');
    el.type = 'date';
    el.id = id;
    return el;
  };
  const mkDiv = (id: string): HTMLDivElement => {
    const el = document.createElement('div');
    el.id = id;
    return el;
  };

  document.body.appendChild(mkInput('history-start'));
  document.body.appendChild(mkInput('history-end'));
  document.body.appendChild(mkDiv('history-summary'));
  document.body.appendChild(mkDiv('history-list'));
  document.body.appendChild(mkDiv('purchases-approval-queue'));
}

function makeRow(overrides: Record<string, unknown>) {
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


const cell = (id: string, label: string): string =>
  document.querySelector(`#history-list tr[data-execution-id="${id}"] td[data-label="${label}"]`)?.textContent?.trim() ?? '';

describe('Purchase History placeholders (issue #1681)', () => {
  beforeEach(() => {
    setupDOM();
    jest.clearAllMocks();
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
  });

  test('a row without a plan shows the shared em dash placeholder, not a hyphen', async () => {
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'h-1', status: 'completed', plan_name: '' })],
    });
    await loadHistory();
    expect(cell('h-1', 'Plan')).toBe('—');
  });

  test('a cancelled no-commitment row still shows its known provider and plan', async () => {
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'c-1', status: 'cancelled', count: 0, provider: 'aws', plan_name: 'Prod plan' }),
        makeRow({ purchase_id: 'c-2', status: 'cancelled', count: 0, provider: 'multiple', plan_name: 'Prod plan' }),
      ],
    });
    await loadHistory();
    expect(cell('c-1', 'Provider')).toBe('AWS');
    expect(cell('c-1', 'Plan')).toBe('Prod plan');
    expect(cell('c-2', 'Provider')).toBe('\u2014');
  });
});
