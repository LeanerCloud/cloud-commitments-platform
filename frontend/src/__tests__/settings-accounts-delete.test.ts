/**
 * Settings account tests: account deletion confirms and the override modal Inherit labels. Split from
 * settings-accounts.test.ts to stay under the 500-line ceiling; the module
 * mocks and DOM helpers live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  mockShowToast,
  mockConfirmDialog,
  buildAccountsDOM,
} from './settings-accounts-harness';
import {
  loadAccountsForProvider,
} from '../settings';

import * as api from '../api';

// ---------------------------------------------------------------------------
// Issue #734 — the delete confirms name the account and list the cascade
// ---------------------------------------------------------------------------

describe('deleteAccount — named target and listed cascade (issue #734)', () => {
  beforeEach(() => {
    buildAccountsDOM();
    jest.clearAllMocks();
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'acc-1', name: 'Prod', external_id: '111111111111', enabled: true, provider: 'aws' },
    ]);
  });

  function pendingError(ids: string[]): Error & { status: number; details: Record<string, unknown> } {
    const err = new Error('cannot delete account') as Error & { status: number; details: Record<string, unknown> };
    err.status = 409;
    err.details = { pending_count: ids.length, pending_execution_ids: ids, reason: 'pending_executions' };
    return err;
  }

  async function clickDelete(): Promise<void> {
    const buttons = Array.from(document.getElementById('aws-accounts-list')!.querySelectorAll('button'));
    buttons.find(b => b.textContent === 'Delete')!.click();
    for (let i = 0; i < 10; i++) await new Promise(r => setTimeout(r, 0));
  }

  function confirmBody(call: number): HTMLElement {
    return (mockConfirmDialog.mock.calls[call]![0] as { body: HTMLElement }).body;
  }

  test('first confirm names the account by name and external ID', async () => {
    mockConfirmDialog.mockResolvedValueOnce(false);
    await loadAccountsForProvider('aws');
    await clickDelete();

    const opts = mockConfirmDialog.mock.calls[0]![0] as { title: string; body: string };
    expect(opts.title).toBe('Delete account "Prod" (111111111111)?');
    expect(opts.body).toContain('"Prod" (111111111111)');
    expect(api.deleteAccount).not.toHaveBeenCalled();
  });

  test('a plain delete shows a success toast naming the account', async () => {
    mockConfirmDialog.mockResolvedValueOnce(true);
    (api.deleteAccount as jest.Mock).mockResolvedValueOnce(undefined);
    await loadAccountsForProvider('aws');
    await clickDelete();

    expect(mockShowToast).toHaveBeenCalledWith({ message: 'Deleted account "Prod" (111111111111).', kind: 'success' });
  });

  test('cascade confirm lists each pending purchase with date and upfront cost, plus the total', async () => {
    mockConfirmDialog.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    (api.deleteAccount as jest.Mock).mockRejectedValueOnce(pendingError(['exec-1', 'exec-2']));
    (api.getPurchaseDetails as jest.Mock).mockImplementation((id: string) => Promise.resolve(
      id === 'exec-1'
        ? { execution_id: id, status: 'pending', plan_name: 'Weekly EC2', step_number: 2, scheduled_date: '2026-11-02', total_upfront_cost: 1200, estimated_savings: 0, recommendations: [{}, {}] }
        : { execution_id: id, status: 'notified', scheduled_date: '2026-11-09', total_upfront_cost: 300, estimated_savings: 0, recommendations: [{}] },
    ));
    await loadAccountsForProvider('aws');
    await clickDelete();

    expect(api.getPurchaseDetails).toHaveBeenCalledTimes(2);
    const items = Array.from(confirmBody(1).querySelectorAll('li')).map(li => li.textContent);
    expect(items).toEqual([
      'Weekly EC2, step 2 · 2 commitments · scheduled Nov 2, 2026 · $1,200 upfront',
      'Manual purchase · 1 commitment · scheduled Nov 9, 2026 · $300 upfront',
    ]);
    expect(confirmBody(1).textContent).toContain('Account "Prod" (111111111111) has 2 pending purchases');
    expect(confirmBody(1).querySelector('.account-delete-pending-total')?.textContent)
      .toBe('Total upfront cost cancelled: $1,500');
  });

  test('a failed purchase lookup still lists the ID and drops the total', async () => {
    mockConfirmDialog.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    (api.deleteAccount as jest.Mock).mockRejectedValueOnce(pendingError(['exec-1']));
    (api.getPurchaseDetails as jest.Mock).mockRejectedValueOnce(new Error('forbidden'));
    await loadAccountsForProvider('aws');
    await clickDelete();

    expect(confirmBody(1).querySelector('li')?.textContent).toBe('Purchase exec-1 (details unavailable)');
    expect(confirmBody(1).querySelector('.account-delete-pending-total')).toBeNull();
  });

  test('more than 10 pending purchases: looks up 10 and summarizes the rest', async () => {
    const ids = Array.from({ length: 13 }, (_, i) => `exec-${i}`);
    mockConfirmDialog.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    (api.deleteAccount as jest.Mock).mockRejectedValueOnce(pendingError(ids));
    (api.getPurchaseDetails as jest.Mock).mockResolvedValue({
      execution_id: 'x', status: 'pending', scheduled_date: '2026-11-02', total_upfront_cost: 10, estimated_savings: 0, recommendations: [],
    });
    await loadAccountsForProvider('aws');
    await clickDelete();

    expect(api.getPurchaseDetails).toHaveBeenCalledTimes(10);
    const items = confirmBody(1).querySelectorAll('li');
    expect(items).toHaveLength(11);
    expect(items[10]!.textContent).toBe('and 3 more (see the History page)');
    expect(confirmBody(1).querySelector('.account-delete-pending-total')).toBeNull();
    expect((mockConfirmDialog.mock.calls[1]![0] as { confirmLabel: string }).confirmLabel).toBe('Cancel All 13 Pending + Delete');
  });
});
