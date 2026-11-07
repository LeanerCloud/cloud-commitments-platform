/**
 * #1050: the History Approve button reads the approve body. A 200 can carry a
 * failed exchange or one whose outcome AWS never confirmed; only "completed"
 * may read as success. Only the network and session are mocked: the real
 * confirm dialog and toasts are driven.
 */

jest.mock('../api', () => ({
  listConvertibleRIs: jest.fn().mockResolvedValue([]),
  getRIUtilization: jest.fn().mockResolvedValue([]),
  getReshapeRecommendations: jest.fn().mockResolvedValue({ recommendations: [], recs_staleness: '', recs_collected_at: null }),
  getRIExchangeHistory: jest.fn(),
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

const ID = '33333333-3333-3333-3333-333333333333';

const PENDING = {
  id: ID, account_id: 'a', exchange_id: '', region: 'us-east-1', source_ri_ids: ['ri-1'],
  source_instance_type: 'm5.large', source_count: 2, target_offering_id: 'o', target_instance_type: 'm5.xlarge',
  target_count: 1, payment_due: '12.34', status: 'pending', mode: 'manual',
  created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
};

const flush = (): Promise<void> => new Promise(resolve => setTimeout(resolve, 0));

function dialogButton(label: string): HTMLButtonElement | undefined {
  return Array.from(document.querySelectorAll<HTMLButtonElement>('.modal-confirm-actions button'))
    .find(b => b.textContent === label);
}

describe('History Approve button outcome (#1050)', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    const ids = ['ri-exchange-instances-list', 'ri-exchange-recommendations-list', 'ri-exchange-history-list'];
    document.body.replaceChildren(...ids.map(id => Object.assign(document.createElement('div'), { id })));
    (api.getRIExchangeHistory as jest.Mock).mockResolvedValue([PENDING]);
  });

  async function approveFromHistory(): Promise<void> {
    await loadRIExchange();
    document.querySelector<HTMLButtonElement>(`.riexchange-approve-btn[data-approve-id="${ID}"]`)!.click();
    await flush();
    dialogButton('Approve')!.click();
    await flush();
    await flush();
  }

  it.each([
    ['outcome_unknown', { status: 'outcome_unknown', message: 'outcome unknown: check ri-1 at AWS' }, '.toast--warning', 'outcome unknown: check ri-1 at AWS'],
    ['failed', { status: 'failed', reason: 'daily cap exceeded' }, '.toast--error', 'RI exchange failed: daily cap exceeded'],
    ['a missing status', {}, '.toast--error', 'unexpected approve response'],
  ])('shows %s, never success', async (_label, body, selector, text) => {
    (api.approveRIExchange as jest.Mock).mockResolvedValue(body);
    await approveFromHistory();

    expect(api.approveRIExchange).toHaveBeenCalledWith(ID);
    expect(document.querySelector(selector)?.textContent).toContain(text);
    expect(document.querySelector('.toast--success')).toBeNull();
  });

  it('shows success only for a completed exchange', async () => {
    (api.approveRIExchange as jest.Mock).mockResolvedValue({ status: 'completed' });
    await approveFromHistory();

    expect(document.querySelector('.toast--success')?.textContent).toContain('RI exchange approved and executed.');
  });
});
