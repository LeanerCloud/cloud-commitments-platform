/**
 * Issue #713 (second half): the Home upcoming-purchases widget ignored the
 * topbar chips. The request now carries the provider and account chips and
 * the server filters, for both the initial load and Retry.
 *
 * Drives loadDashboard() so the production render path is exercised.
 */

jest.mock('../toast', () => ({ showToast: jest.fn(() => ({ dismiss: jest.fn() })) }));
jest.mock('../confirmDialog', () => ({ confirmDialog: jest.fn(() => Promise.resolve(true)) }));
jest.mock('chart.js', () => {
  const MockChart = jest.fn().mockImplementation(() => ({ destroy: jest.fn() }));
  (MockChart as unknown as { register: jest.Mock }).register = jest.fn();
  return { Chart: MockChart, registerables: [] };
});
jest.mock('../recommendations', () => ({
  groupRecsByCell: jest.fn(() => new Map()),
  pageLevelRange: jest.fn(() => ({ savingsMin: 0, savingsMax: 0, cellCount: 0 })),
  formatSavingsRange: jest.fn((min: number, max: number) => `$${min}-$${max}`),
  triggerAutoRefreshIfStale: jest.fn(() => Promise.resolve()),
}));
jest.mock('../api', () => ({
  getDashboardSummary: jest.fn().mockResolvedValue({ potential_monthly_savings: 0, by_service: {} }),
  getUpcomingPurchases: jest.fn(),
  getSavingsAnalytics: jest.fn().mockResolvedValue({ data_points: [] }),
  getRecommendations: jest.fn().mockResolvedValue([]),
}));
jest.mock('../state', () => ({
  getCurrentProvider: jest.fn().mockReturnValue(''),
  getCurrentAccountIDs: jest.fn().mockReturnValue([]),
  getSavingsChart: jest.fn().mockReturnValue(null),
  setSavingsChart: jest.fn(),
  subscribeProvider: jest.fn().mockReturnValue(() => {}),
  subscribeAccount: jest.fn().mockReturnValue(() => {}),
  getCurrentUser: jest.fn().mockReturnValue(null),
}));

import { loadDashboard } from '../dashboard';
import * as api from '../api';
import * as state from '../state';
import { rowMatchesProvider, upcomingEmptyMessage } from '../lib/upcoming-scope';
import type { UpcomingPurchase } from '../types';

const row = (execution_id: string, provider: UpcomingPurchase['provider'], plan_name: string): UpcomingPurchase => ({
  execution_id,
  plan_id: `plan-${execution_id}`,
  plan_name,
  scheduled_date: '2026-11-01',
  provider,
  service: 'compute',
  step_number: 1,
  total_steps: 2,
  estimated_savings: 50,
});

const ROWS = [row('e-aws', 'aws', 'AWS Plan'), row('e-az', 'azure', 'Azure Plan')];

const setScope = (provider: string, accountIDs: string[] = []) => {
  (state.getCurrentProvider as jest.Mock).mockReturnValue(provider);
  (state.getCurrentAccountIDs as jest.Mock).mockReturnValue(accountIDs);
};

const list = () => document.getElementById('upcoming-list') as HTMLElement;
const planNames = () => Array.from(list().querySelectorAll('.upcoming-details h4')).map(h => h.textContent);

describe('upcoming-scope helpers (#713)', () => {
  test('provider filter matches case-insensitively', () => {
    expect(rowMatchesProvider(ROWS[1]!, 'AZURE')).toBe(true);
    expect(rowMatchesProvider(ROWS[0]!, 'azure')).toBe(false);
  });

  test('a cross-provider bound row matches each of its providers (#1746)', () => {
    const multi = { ...row('e-multi', 'multiple', 'Bound Plan'), providers: ['aws', 'azure'] };
    expect(rowMatchesProvider(multi, 'aws')).toBe(true);
    expect(rowMatchesProvider(multi, 'azure')).toBe(true);
    expect(rowMatchesProvider(multi, 'gcp')).toBe(false);
  });

  test('empty message names the provider only when one is filtered', () => {
    expect(upcomingEmptyMessage('')).toBe('No upcoming scheduled purchases');
    expect(upcomingEmptyMessage('all')).toBe('No upcoming scheduled purchases');
    expect(upcomingEmptyMessage('gcp')).toBe('No upcoming scheduled purchases for GCP');
  });
});

describe('Home upcoming widget follows the topbar chips (#713)', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    document.body.innerHTML = `
      <div id="summary"></div>
      <section id="savings-by-service-section">
        <canvas id="savings-by-service-chart"></canvas>
        <p id="savings-by-service-empty" class="empty hidden"></p>
      </section>
      <div id="upcoming-list"></div>
    `;
    (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: ROWS });
  });

  test('no chips: requests with empty scope and renders every row', async () => {
    setScope('');
    await loadDashboard();
    expect(api.getUpcomingPurchases).toHaveBeenCalledWith('', []);
    expect(planNames()).toEqual(['AWS Plan', 'Azure Plan']);
  });

  test('provider and account chips are sent with the request', async () => {
    setScope('azure', ['acct-1', 'acct-2']);
    (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [ROWS[1]] });
    await loadDashboard();
    expect(api.getUpcomingPurchases).toHaveBeenCalledWith('azure', ['acct-1', 'acct-2']);
    expect(planNames()).toEqual(['Azure Plan']);
    expect(list().querySelector('.upcoming-scope-note')).toBeNull();
  });

  test('empty result under a provider chip shows a filter-aware empty state', async () => {
    setScope('gcp');
    (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
    await loadDashboard();
    expect(list().querySelector('.empty')?.textContent).toBe('No upcoming scheduled purchases for GCP');
  });

  test('Retry after a failed fetch re-sends the current chips', async () => {
    setScope('aws', ['acct-1']);
    (api.getUpcomingPurchases as jest.Mock).mockRejectedValueOnce(new Error('API error: 503'));
    await loadDashboard();
    expect(list().querySelector('.error')).not.toBeNull();
    (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [ROWS[0]] });
    const retry = list().querySelector('button[data-action="retry-upcoming"]') as HTMLButtonElement;
    retry.click();
    await new Promise(resolve => setTimeout(resolve, 0));
    expect(api.getUpcomingPurchases).toHaveBeenLastCalledWith('aws', ['acct-1']);
    expect(planNames()).toEqual(['AWS Plan']);
  });

  test('an older overlapping load resolving last does not overwrite the newer chip (#713)', async () => {
    let resolveAws!: (v: unknown) => void;
    (api.getUpcomingPurchases as jest.Mock)
      .mockImplementationOnce(() => new Promise(r => { resolveAws = r; }))
      .mockResolvedValueOnce({ purchases: [ROWS[1]] });
    setScope('aws');
    const loadA = loadDashboard();
    setScope('azure');
    await loadDashboard();
    expect(planNames()).toEqual(['Azure Plan']);
    resolveAws({ purchases: [ROWS[0]] });
    await loadA;
    expect(planNames()).toEqual(['Azure Plan']);
  });

  test('a Retry resolving after a newer load does not overwrite the newer render (#713)', async () => {
    setScope('aws');
    (api.getUpcomingPurchases as jest.Mock).mockRejectedValueOnce(new Error('API error: 503'));
    await loadDashboard();
    let resolveRetry!: (v: unknown) => void;
    (api.getUpcomingPurchases as jest.Mock)
      .mockImplementationOnce(() => new Promise(r => { resolveRetry = r; }))
      .mockResolvedValueOnce({ purchases: [ROWS[1]] });
    (list().querySelector('button[data-action="retry-upcoming"]') as HTMLButtonElement).click();
    setScope('azure');
    await loadDashboard();
    expect(planNames()).toEqual(['Azure Plan']);
    resolveRetry({ purchases: [ROWS[0]] });
    await new Promise(resolve => setTimeout(resolve, 0));
    expect(planNames()).toEqual(['Azure Plan']);
  });
});
