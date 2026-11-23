/**
 * Inventory Active commitments: fetch + render flow. Split from
 * inventory.test.ts to stay under the 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import { buildInventoryDOM, makeCommitment, clearDOM, numColumns } from './inventory-harness';
import { loadActiveCommitments } from '../inventory';
import * as api from '../api';
import * as state from '../state';

describe('loadActiveCommitments — fetch + render flow', () => {
  beforeEach(() => {
    buildInventoryDOM();
    (api.listActiveCommitments as jest.Mock).mockReset();
    (state.subscribeProvider as jest.Mock).mockReturnValue(jest.fn());
    (state.subscribeAccount as jest.Mock).mockReturnValue(jest.fn());
    (state.getCurrentProvider as jest.Mock).mockReturnValue('');
    (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
  });

  afterEach(() => {
    clearDOM();
  });

  test('renders a table with header columns and one row per commitment', async () => {
    (api.listActiveCommitments as jest.Mock).mockResolvedValue([
      makeCommitment({ id: 'a:1', account_name: 'Prod', service: 'ec2', region: 'us-east-1', count: 4, term_years: 1, monthly_cost: 240.5 }),
      makeCommitment({ id: 'a:2', account_name: 'Stg', service: 'rds', region: 'eu-west-1', count: 1, term_years: 3, monthly_cost: 60.0 }),
    ]);

    await loadActiveCommitments();

    const list = document.getElementById('active-commitments-list')!;
    const table = list.querySelector('table');
    expect(table).not.toBeNull();

    // Header columns lock the table shape so a refactor that re-orders
    // (or drops) a column trips the test.
    const headers = Array.from(list.querySelectorAll('thead th')).map(th => th.textContent);
    expect(headers).toEqual([
      'Provider', 'Account', 'Service', 'Resource Type', 'Region', 'Count', 'Term', 'Payment', 'Monthly Cost', 'Monthly Savings', 'Expires',
    ]);

    const rows = list.querySelectorAll('tbody tr');
    expect(rows.length).toBe(2);
    // Account cell contains the account name AND its monospaced ID.
    expect(rows[0]!.textContent).toContain('Prod');
    expect(rows[0]!.textContent).toContain('ec2');

    // Monthly Savings column (10th, 0-indexed 9) must render the estimated_savings value.
    // makeCommitment defaults estimated_savings to 80.0; formatCurrency mock returns "$80".
    const savingsCell = rows[0]!.querySelector('td:nth-child(10)');
    expect(savingsCell).not.toBeNull();
    expect(savingsCell!.textContent).toContain('80');
  });

  test('renders an empty paragraph when no commitments are returned', async () => {
    (api.listActiveCommitments as jest.Mock).mockResolvedValue([]);

    await loadActiveCommitments();

    const list = document.getElementById('active-commitments-list')!;
    expect(list.querySelector('table')).toBeNull();
    const empty = list.querySelector('.empty');
    expect(empty).not.toBeNull();
    expect(empty!.textContent).toMatch(/no active commitments/i);
  });

  test('renders an error paragraph when the API rejects, and tears down the skeleton', async () => {
    (api.listActiveCommitments as jest.Mock).mockRejectedValue(new Error('boom'));

    await loadActiveCommitments();

    const list = document.getElementById('active-commitments-list')!;
    expect(list.querySelector('table')).toBeNull();
    // Skeleton marker should be gone — error path must call teardownSkeleton
    // before rendering the error paragraph.
    expect(list.dataset['skeletonActive']).toBeUndefined();
    const err = list.querySelector('.error');
    expect(err).not.toBeNull();
    expect(err!.textContent).toContain('boom');
  });

  test('refresh button click re-invokes the fetch', async () => {
    (api.listActiveCommitments as jest.Mock).mockResolvedValue([]);

    await loadActiveCommitments();
    expect(api.listActiveCommitments).toHaveBeenCalledTimes(1);

    const btn = document.getElementById('active-commitments-refresh-btn')!;
    btn.click();
    // Click fires loadActiveCommitments; let the microtasks drain.
    await Promise.resolve();
    expect(api.listActiveCommitments).toHaveBeenCalledTimes(2);
  });

  test('does not repeat the account id when the name already contains it (#179)', async () => {
    (api.listActiveCommitments as jest.Mock).mockResolvedValue([
      makeCommitment({ account_name: 'CUDly host (acct-1234)', account_id: 'acct-1234' }),
    ]);

    await loadActiveCommitments();

    const accountCell = document.querySelector('#active-commitments-list tbody tr td:nth-child(2)');
    expect(accountCell?.textContent).toBe('CUDly host (acct-1234)');
  });

  test('falls back to monospaced account_id when account_name is missing', async () => {
    (api.listActiveCommitments as jest.Mock).mockResolvedValue([
      makeCommitment({ account_name: undefined, account_id: 'acc-no-name' }),
    ]);

    await loadActiveCommitments();

    const list = document.getElementById('active-commitments-list')!;
    const accountCell = list.querySelector('tbody tr td:nth-child(2)');
    expect(accountCell?.textContent).toContain('acc-no-name');
    expect(accountCell?.querySelector('.monospace')).not.toBeNull();
  });

  test('money and count columns are right-aligned with tabular digits (issue #739)', async () => {
    (api.listActiveCommitments as jest.Mock).mockResolvedValue([
      makeCommitment({ id: 'a:1', count: 4, monthly_cost: 240.5 }),
      makeCommitment({ id: 'a:2', count: 1, monthly_cost: null }),
    ]);
    await loadActiveCommitments();
    const list = document.getElementById('active-commitments-list')!;
    expect(numColumns(list)).toEqual({ headers: ['Count', 'Monthly Cost', 'Monthly Savings'], rowsMatch: true });
  });
});
