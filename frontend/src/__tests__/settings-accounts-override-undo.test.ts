/**
 * Settings account tests: per-account overrides modal delete, undo and commitmentOptions parity. Split from
 * settings-accounts.test.ts to stay under the 500-line ceiling; the module
 * mocks and DOM helpers live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  mockConfirmDialog,
  buildAccountsDOM,
} from './settings-accounts-harness';
import {
  loadOverridesPanel,
  setupSettingsHandlers,
} from '../settings';

import * as api from '../api';
import { formatDateTime } from '../utils';

// ---------------------------------------------------------------------------
// Per-account overrides modal — issue #122
// ---------------------------------------------------------------------------

describe('Account overrides modal', () => {
  beforeEach(() => {
    buildAccountsDOM();
    setupSettingsHandlers();
    jest.clearAllMocks();
  });

  test('Delete-override button + dialog wording match the actual data semantics (issue #114)', async () => {
    // The action DELETEs the override row; pre-#114 the dialog said
    // "Reset … will be replaced" which implied a stuck-around row with
    // new values. Pin the post-fix wording so a future regression doesn't
    // silently re-introduce the mismatch.
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      {
        account_id: 'acc-1',
        provider: 'aws',
        service: 'ec2',
        term: 1,
        payment: 'no-upfront',
        coverage: 80,
      },
    ]);
    mockConfirmDialog.mockResolvedValue(false); // user cancels — we don't need the API call to fire

    const panel = document.createElement('div');
    document.body.appendChild(panel);
    await loadOverridesPanel('acc-1', panel, 'aws');

    // The action button now reads "Delete" (not "Reset").
    const buttons = Array.from(panel.querySelectorAll('button'));
    const deleteBtn = buttons.find(b => b.textContent === 'Delete');
    expect(deleteBtn).toBeDefined();
    expect(buttons.find(b => b.textContent === 'Reset')).toBeUndefined();

    // Click it and inspect the confirmDialog opts.
    deleteBtn!.click();
    await Promise.resolve(); // let the click handler's await chain start
    expect(mockConfirmDialog).toHaveBeenCalledTimes(1);
    const opts = mockConfirmDialog.mock.calls[0]![0] as {
      title: string;
      body: string;
      confirmLabel: string;
    };
    expect(opts.title).toBe('Delete override?');
    expect(opts.confirmLabel).toBe('Delete override');
    expect(opts.body).toContain('Delete the aws/ec2 override');
    expect(opts.body).toContain('revert to the global default');
    expect(opts.body).toContain('removed');
    // Pre-#114 wording must not appear.
    expect(opts.body).not.toContain('replaced');
    expect(opts.body).not.toContain('Reset');
  });

  test('override rows show when each override was last updated (issue #8)', async () => {
    (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
      {
        account_id: 'acc-1', provider: 'aws', service: 'ec2', term: 1, coverage: 80,
        created_at: '2026-04-01T08:00:00Z', updated_at: '2026-04-27T12:34:56Z',
      },
      // Older API or never-saved row: no timestamp, no stamp.
      { account_id: 'acc-1', provider: 'aws', service: 'rds', term: 3 },
      // Go's zero time.Time must not render as year 1.
      { account_id: 'acc-1', provider: 'aws', service: 'elasticache', updated_at: '0001-01-01T00:00:00Z' },
    ]);

    const panel = document.createElement('div');
    document.body.appendChild(panel);
    await loadOverridesPanel('acc-1', panel, 'aws');

    const rows = Array.from(panel.querySelectorAll('table.overrides-table tbody tr'));
    expect(rows).toHaveLength(3);
    const serviceCells = rows.map(r => (r as HTMLTableRowElement).cells[0]!);

    const stamp = serviceCells[0]!.querySelector<HTMLTimeElement>('time.last-updated-stamp');
    expect(stamp).not.toBeNull();
    expect(stamp!.textContent).toBe(`Updated ${formatDateTime('2026-04-27T12:34:56Z')}`);
    expect(stamp!.dateTime).toBe('2026-04-27T12:34:56.000Z');
    expect(stamp!.title).toBe('Last updated 2026-04-27 12:34 UTC');
    // The service name stays the cell's leading text.
    expect(serviceCells[0]!.firstChild!.textContent).toBe('aws/ec2');

    expect(serviceCells[1]!.querySelector('.last-updated-stamp')).toBeNull();
    expect(serviceCells[1]!.textContent).toBe('aws/rds');
    expect(serviceCells[2]!.querySelector('.last-updated-stamp')).toBeNull();
  });
});
