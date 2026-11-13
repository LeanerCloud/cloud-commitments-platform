/**
 * #1883: direct (interactive) RI exchanges show in the History card,
 * read-only, beside the approval history.
 */

jest.mock('../api', () => ({
  listConvertibleRIs: jest.fn().mockResolvedValue([]),
  getRIUtilization: jest.fn().mockResolvedValue([]),
  getReshapeRecommendations: jest.fn().mockResolvedValue({ recommendations: [], recs_staleness: '', recs_collected_at: null }),
  getRIExchangeHistoryResponse: jest.fn(),
  listTargetOfferings: jest.fn(),
  approveRIExchange: jest.fn(),
  listAccountsMinimal: jest.fn().mockResolvedValue([]),
}));

jest.mock('../navigation', () => ({
  switchTab: jest.fn(),
  switchSettingsSubTab: jest.fn(),
}));

jest.mock('../permissions', () => ({ canAccess: jest.fn(() => true) }));

jest.mock('../state', () => ({
  getCurrentUser: jest.fn(() => ({ id: 'u1' })),
  getCurrentProvider: jest.fn(() => 'aws'),
  getCurrentAccountIDs: jest.fn(() => []),
  getRiExchangeColumnFilters: jest.fn(() => ({})),
  setRiExchangeColumnFilter: jest.fn(),
  clearAllRiExchangeColumnFilters: jest.fn(),
  getActiveRiColumnFilters: jest.fn(() => ({})),
  setActiveRiColumnFilter: jest.fn(),
  clearAllActiveRiColumnFilters: jest.fn(),
}));

import * as api from '../api';
import { loadRIExchange } from '../riexchange';

const base = {
  region: '', cloud_account_id: null, provider_account_id: null, targets: [{ sku: 'x' }],
  max_payment_due: '100', max_payment_currency: 'USD', net_payable: null, net_payable_currency: '',
  error_summary: '', executed_by: 'op@example.com', created_at: '2026-11-01T10:00:00Z',
  completed_at: null, resolved_status: '', resolved_at: null,
};
const AZURE = { ...base, id: 'd1', provider: 'azure', status: 'completed', source_ids: ['/res/a'], net_payable: '-12.5', net_payable_currency: 'EUR' };
const AWS = { ...base, id: 'd2', provider: 'aws', status: 'unknown', source_ids: ['ri-1', 'ri-2'], resolved_status: 'failed', error_summary: 'outcome unknown' };

const flush = (): Promise<void> => new Promise(resolve => setTimeout(resolve, 0));

function history(): HTMLElement {
  return document.getElementById('ri-exchange-history-list')!;
}

function mockHistory(records: unknown[], direct: unknown[]): void {
  (api.getRIExchangeHistoryResponse as jest.Mock).mockResolvedValue({ records, direct_executions: direct });
}

describe('Direct exchanges in History (#1883)', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    const ids = ['ri-exchange-instances-list', 'ri-exchange-recommendations-list', 'ri-exchange-history-list'];
    document.body.replaceChildren(...ids.map(id => Object.assign(document.createElement('div'), { id })));
    mockHistory([], [AZURE, AWS]);
  });

  it('lists each direct row with provider, amount, status and resolved state', async () => {
    await loadRIExchange();
    await flush();
    const rows = history().querySelectorAll('.ri-exchange-direct-history tbody tr');
    expect(rows).toHaveLength(2);
    expect(rows[0]!.textContent).toContain('AZURE');
    expect(rows[0]!.textContent).toContain('-€12.50');
    expect(rows[1]!.textContent).toContain('AWS');
    expect(rows[1]!.textContent).toContain('ri-1, ri-2');
    expect(rows[1]!.textContent).toContain('cap $100.00');
    expect(rows[1]!.textContent).toContain('Resolved by operator: failed');
    expect(rows[1]!.textContent).toContain('outcome unknown');
    expect(history().textContent).not.toContain('No exchange history found');
  });

  it('shows a dash, not USD, when the currency is empty or missing', async () => {
    const noCurrency = { ...AZURE, id: 'd3', net_payable: '5', net_payable_currency: '' };
    const missing = { ...AWS, id: 'd4', max_payment_currency: undefined };
    mockHistory([], [noCurrency, missing]);
    await loadRIExchange();
    await flush();
    const cells = Array.from(history().querySelectorAll('.ri-exchange-direct-history tbody tr'))
      .map(tr => tr.querySelector('td.num')!.textContent);
    expect(cells).toEqual(['—', 'cap —']);
  });

  it('is read-only: no buttons or links in the direct section', async () => {
    await loadRIExchange();
    await flush();
    expect(history().querySelectorAll('.ri-exchange-direct-history button, .ri-exchange-direct-history a')).toHaveLength(0);
  });

  it('reads both kinds of rows from one history request', async () => {
    await loadRIExchange();
    await flush();
    expect(api.getRIExchangeHistoryResponse).toHaveBeenCalledTimes(1);
  });

  it('shows the single error line when the history request fails', async () => {
    (api.getRIExchangeHistoryResponse as jest.Mock).mockRejectedValue(new Error('boom'));
    await loadRIExchange();
    await flush();
    expect(history().querySelector('p.error')?.textContent).toBe('Failed to load exchange history: boom');
  });

  it('shows the empty state only when there are neither kinds of rows', async () => {
    mockHistory([], []);
    await loadRIExchange();
    await flush();
    expect(history().textContent).toContain('No exchange history found');
  });
});
