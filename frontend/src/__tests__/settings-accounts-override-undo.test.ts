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
});
