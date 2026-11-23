/**
 * Settings account tests: account list, account modal and account form submit.
 * Split from settings-accounts.test.ts to stay under the 500-line ceiling; the
 * module mocks and DOM helpers live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  buildAccountsDOM,
} from './settings-accounts-harness';
import {
  loadAccountsForProvider,
} from '../settings';

import * as api from '../api';

// ---------------------------------------------------------------------------
// loadAccountsForProvider
// ---------------------------------------------------------------------------

describe('loadAccountsForProvider', () => {
  beforeEach(() => {
    buildAccountsDOM();
    jest.clearAllMocks();
  });

  test('renders account rows when API returns accounts', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'id1', name: 'Prod',    external_id: '111111111111', enabled: true },
      { id: 'id2', name: 'Dev',     external_id: '222222222222', enabled: false }
    ]);

    await loadAccountsForProvider('aws');

    const container = document.getElementById('aws-accounts-list')!;
    // P3 replaced inline .account-row divs with an accounts-table.
    const table = container.querySelector('table.accounts-table');
    expect(table).not.toBeNull();
    expect(table?.querySelectorAll('tbody tr')).toHaveLength(2);
    expect(container.textContent).toContain('Prod');
    expect(container.textContent).toContain('Disabled');
  });

  test('action labels do not repeat the ID when the name already contains it (#1691)', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'id1', name: 'CUDly host (111111111111)', external_id: '111111111111', enabled: true },
      { id: 'id2', name: 'Dev', external_id: '222222222222', enabled: true }
    ]);

    await loadAccountsForProvider('aws');

    const labels = Array.from(
      document.getElementById('aws-accounts-list')!.querySelectorAll('tbody button[aria-label]')
    ).map(b => b.getAttribute('aria-label')!);
    expect(labels).toContain('Edit CUDly host (111111111111)');
    expect(labels).toContain('Delete CUDly host (111111111111)');
    expect(labels.some(l => l.includes('(111111111111) (111111111111)'))).toBe(false);
    expect(labels).toContain('Edit Dev (222222222222)');
  });

  test('renders "No accounts configured." for empty list', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([]);

    await loadAccountsForProvider('aws');

    // P3 wraps the empty copy in a dedicated <p.accounts-empty>.
    const container = document.getElementById('aws-accounts-list')!;
    expect(container.querySelector('.accounts-empty')?.textContent).toBe('No accounts configured.');
  });

  test('renders error message on API failure', async () => {
    (api.listAccounts as jest.Mock).mockRejectedValue(new Error('Network error'));

    await loadAccountsForProvider('azure');

    expect(document.getElementById('azure-accounts-list')!.textContent).toBe('Failed to load accounts.');
  });

  test('does nothing when container element is missing', async () => {
    document.body.replaceChildren();
    (api.listAccounts as jest.Mock).mockResolvedValue([]);

    await expect(loadAccountsForProvider('aws')).resolves.toBeUndefined();
    expect(api.listAccounts).not.toHaveBeenCalled();
  });

  test('passes provider filter to listAccounts', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([]);

    await loadAccountsForProvider('gcp');

    expect(api.listAccounts).toHaveBeenCalledWith({ provider: 'gcp' });
  });

  test('renders status-chip row with counts and All active by default', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: '1', name: 'Prod', provider: 'aws', external_id: '111', enabled: true },
      { id: '2', name: 'Stage', provider: 'aws', external_id: '222', enabled: false },
      { id: '3', name: 'Dev', provider: 'aws', external_id: '333', enabled: true },
    ]);

    await loadAccountsForProvider('aws');

    const container = document.getElementById('aws-accounts-list')!;
    const chips = Array.from(container.querySelectorAll('.status-chip'));
    expect(chips).toHaveLength(3);
    expect(chips[0]!.textContent).toBe('All (3)');
    expect(chips[1]!.textContent).toBe('Active (2)');
    expect(chips[2]!.textContent).toBe('Disabled (1)');
    expect(chips[0]!.classList.contains('active')).toBe(true);
    expect(chips[1]!.classList.contains('active')).toBe(false);
    expect(container.querySelectorAll('tbody tr')).toHaveLength(3);
  });

  test('clicking Active chip filters out disabled rows', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: '1', name: 'Prod', provider: 'aws', external_id: '111', enabled: true },
      { id: '2', name: 'Stage', provider: 'aws', external_id: '222', enabled: false },
    ]);

    await loadAccountsForProvider('aws');
    const container = document.getElementById('aws-accounts-list')!;
    const chips = Array.from(container.querySelectorAll('.status-chip')) as HTMLButtonElement[];
    chips[1]!.click();

    const rows = Array.from(container.querySelectorAll('tbody tr'));
    expect(rows).toHaveLength(1);
    expect(rows[0]!.textContent).toContain('Prod');
    expect(rows[0]!.textContent).not.toContain('Stage');
    const afterChips = Array.from(container.querySelectorAll('.status-chip')) as HTMLButtonElement[];
    expect(afterChips[1]!.classList.contains('active')).toBe(true);
  });

  test('clicking All restores full row set after a filter', async () => {
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: '1', name: 'Prod', provider: 'aws', external_id: '111', enabled: true },
      { id: '2', name: 'Stage', provider: 'aws', external_id: '222', enabled: false },
    ]);

    await loadAccountsForProvider('aws');
    const container = document.getElementById('aws-accounts-list')!;
    const chips = () => Array.from(container.querySelectorAll('.status-chip')) as HTMLButtonElement[];
    chips()[2]!.click();
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1);
    chips()[0]!.click();
    expect(container.querySelectorAll('tbody tr')).toHaveLength(2);
  });
});
