/** Issue #1717: History and approval-queue Service cells show display names. */

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

import type { HistoryPurchase } from '../types';
import * as state from '../state';
import * as api from '../api';
import { getCurrentUser } from '../state';
import { describeHistoryPurchase } from '../history-confirm-body';
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


const SLUGS: Array<[string, string]> = [
  ['rds', 'RDS'],
  ['compute', 'Compute'],
  ['relational-db', 'Relational DB'],
  ['mystery-svc', 'mystery-svc'],
];

describe('History service display names (issue #1717)', () => {
  beforeEach(() => {
    setupDOM();
    jest.clearAllMocks();
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    const rows = SLUGS.flatMap(([slug]) => [
      makeRow({ purchase_id: `h-${slug}`, status: 'completed', service: slug }),
      makeRow({ purchase_id: `q-${slug}`, status: 'pending', service: slug, created_by_user_id: ADMIN_USER.id }),
    ]);
    (api.getHistory as jest.Mock).mockResolvedValue({ summary: {}, purchases: rows });
  });

  const serviceCell = (root: string, id: string): string =>
    document.querySelector(`${root} tr[data-execution-id="${id}"] td[data-label="Service"]`)?.textContent?.trim() ?? '';

  test.each(SLUGS)('Purchase History and queue cells show the name for %s', async (slug, label) => {
    await loadHistory();
    expect(serviceCell('#history-list', `h-${slug}`)).toBe(label);
    expect(serviceCell('#purchases-approval-queue', `q-${slug}`)).toBe(label);
  });

  test.each([['#history-list', 'history'], ['#purchases-approval-queue', 'queue']])(
    'the %s Service filter lists names but commits the slug',
    async (root, scope) => {
      await loadHistory();
      document.querySelector<HTMLElement>(`${root} .history-column-filter-btn[data-column="service"]`)!.click();
      const pop = document.querySelector('.column-filter-popover')!;
      const labels = Array.from(pop.querySelectorAll('label')).map((l) => l.textContent?.trim());
      expect(labels).toEqual(expect.arrayContaining(['RDS', 'Compute', 'Relational DB', 'mystery-svc']));
      expect(labels).not.toContain('rds');
      const boxes = Array.from(pop.querySelectorAll<HTMLInputElement>('input[data-value]'));
      expect(boxes.map((b) => b.dataset['value'])).toEqual(
        expect.arrayContaining(['rds', 'compute', 'relational-db', 'mystery-svc']),
      );
      const all = pop.querySelector<HTMLInputElement>('input[data-role="all"]')!;
      all.checked = false;
      all.dispatchEvent(new Event('change'));
      const rds = boxes.find((b) => b.dataset['value'] === 'rds')!;
      rds.checked = true;
      rds.dispatchEvent(new Event('change'));
      const setter = scope === 'history' ? state.setPurchaseHistoryColumnFilter : state.setApprovalQueueColumnFilter;
      expect(setter).toHaveBeenCalledWith('service', { kind: 'set', values: ['rds'] });
    },
  );

  test('an unknown slug with markup is escaped in both cells and an empty service renders an empty cell, as before', async () => {
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'h-x', status: 'completed', service: '<b>x</b>&y' }),
        makeRow({ purchase_id: 'q-x', status: 'pending', service: '<b>x</b>&y', created_by_user_id: ADMIN_USER.id }),
        makeRow({ purchase_id: 'h-empty', status: 'completed', service: '' }),
      ],
    });
    await loadHistory();
    for (const [root, id] of [['#history-list', 'h-x'], ['#purchases-approval-queue', 'q-x']]) {
      const cell = document.querySelector(`${root} tr[data-execution-id="${id}"] td[data-label="Service"]`)!;
      expect(cell.querySelector('b')).toBeNull();
      expect(cell.textContent).toBe('<b>x</b>&y');
    }
    expect(serviceCell('#history-list', 'h-empty')).toBe('');
  });

  test('the confirm-dialog summary uses the display name', () => {
    const p = makeRow({ service: 'relational-db' }) as unknown as HistoryPurchase;
    expect(describeHistoryPurchase(p)).toBe('AWS Relational DB t3.medium in us-east-1');
  });
});
