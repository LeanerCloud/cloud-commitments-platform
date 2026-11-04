/**
 * Admin pages UX nits (issue #1691).
 */
import { accountDisplayLabel } from '../lib/account-label';
import { loadAccountsForProvider } from '../settings';

jest.mock('../api', () => ({
  listAccounts: jest.fn(),
  listAccountsMinimal: jest.fn(),
}));
jest.mock('../toast', () => ({ showToast: jest.fn(() => ({ dismiss: jest.fn() })) }));
jest.mock('../confirmDialog', () => ({ confirmDialog: jest.fn() }));
jest.mock('../recommendations', () => ({ loadRecommendations: jest.fn() }));

import * as api from '../api';
import { initTopbarFilters } from '../topbar-filters';

describe('1. accountDisplayLabel', () => {
  test('appends the id when the name lacks it', () => {
    expect(accountDisplayLabel('Prod', '111')).toBe('Prod (111)');
  });
  test('does not repeat an id the name already contains', () => {
    expect(accountDisplayLabel('CUDly host (909)', '909')).toBe('CUDly host (909)');
  });
  test('returns the id alone for an empty name', () => {
    expect(accountDisplayLabel('', '909')).toBe('909');
  });

  test('accounts table action labels do not duplicate the id', async () => {
    document.body.innerHTML = '<div id="aws-accounts-list"></div>';
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'a1', name: 'CUDly host (909)', provider: 'aws', external_id: '909', enabled: true },
    ]);
    await loadAccountsForProvider('aws');
    expect(document.querySelector('button[aria-label="Edit CUDly host (909)"]')).not.toBeNull();
  });

  test('topbar account chip option does not duplicate the id', async () => {
    document.body.innerHTML = '<div id="topbar-filters"></div>';
    (api.listAccountsMinimal as jest.Mock).mockResolvedValue([
      { id: 'a1', name: 'CUDly host (909)', external_id: '909', provider: 'aws' },
    ]);
    initTopbarFilters();
    await new Promise((r) => setTimeout(r, 0));
    const triggers = document.querySelectorAll<HTMLButtonElement>('.chip-select');
    triggers[1]!.click();
    const labels = Array.from(document.querySelectorAll('.chip-select-option')).map((o) => o.textContent);
    expect(labels).toContain('CUDly host (909)');
  });
});
