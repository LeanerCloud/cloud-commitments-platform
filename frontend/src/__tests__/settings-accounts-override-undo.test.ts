/**
 * Settings account tests: per-account overrides modal delete, undo and commitmentOptions parity. Split from
 * settings-accounts.test.ts to stay under the 500-line ceiling; the module
 * mocks and DOM helpers live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  mockShowToast,
  mockConfirmDialog,
  mockLoadRecommendations,
  buildAccountsDOM,
} from './settings-accounts-harness';
import {
  loadOverridesPanel,
  openOverrideModal,
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

  describe('toast-undo for Delete override (issue #113)', () => {
    const overrideFixture = {
      account_id: 'acc-1',
      provider: 'aws',
      service: 'rds',
      term: 1,
      payment: 'all-upfront',
      coverage: 75,
      enabled: true,
    };

    test('Delete shows info toast with Undo action and clears row (click-Undo path)', async () => {
      (api.listAccountServiceOverrides as jest.Mock)
        .mockResolvedValueOnce([overrideFixture])  // initial render
        .mockResolvedValue([]);                     // reload after delete + reload after undo
      (api.deleteAccountServiceOverride as jest.Mock).mockResolvedValue(undefined);
      (api.saveAccountServiceOverride as jest.Mock).mockResolvedValue(overrideFixture);
      mockConfirmDialog.mockResolvedValue(true);

      const panel = document.createElement('div');
      document.body.appendChild(panel);
      await loadOverridesPanel('acc-1', panel, 'aws');

      const deleteBtn = Array.from(panel.querySelectorAll('button'))
        .find(b => b.textContent === 'Delete') as HTMLButtonElement;
      expect(deleteBtn).toBeDefined();

      deleteBtn.click();
      // Flush the confirm + delete + reload chain.
      for (let i = 0; i < 5; i++) { await new Promise(r => setTimeout(r, 0)); }

      expect(api.deleteAccountServiceOverride).toHaveBeenCalledWith('acc-1', 'aws', 'rds');

      // An info toast with an Undo action must have been shown.
      const toastCalls = mockShowToast.mock.calls.map(c => c[0]) as Array<{
        kind?: string;
        message?: string;
        actions?: Array<{ label: string; onClick: () => void }>;
        timeout?: number | null;
      }>;
      const undoToast = toastCalls.find(t => t.kind === 'info' && t.message?.includes('aws/rds'));
      expect(undoToast).toBeDefined();
      expect(undoToast!.actions).toBeDefined();
      expect(undoToast!.actions!.length).toBe(1);
      expect(undoToast!.actions![0]!.label).toBe('Undo');
      // 10-second TTL (issue #728; was 5 s per issue #113).
      expect(undoToast!.timeout).toBe(10_000);

      // Simulate clicking Undo.
      undoToast!.actions![0]!.onClick();
      for (let i = 0; i < 5; i++) { await new Promise(r => setTimeout(r, 0)); }

      // saveAccountServiceOverride must have been called with the original snapshot.
      expect(api.saveAccountServiceOverride).toHaveBeenCalledWith(
        'acc-1', 'aws', 'rds',
        expect.objectContaining({
          term: 1,
          payment: 'all-upfront',
          coverage: 75,
          enabled: true,
        }),
      );
    });

    test('Undo toast appears immediately, not gated behind a slow recommendations refresh (issue #1415)', async () => {
      (api.listAccountServiceOverrides as jest.Mock)
        .mockResolvedValueOnce([overrideFixture])  // initial render
        .mockResolvedValue([]);                     // reload after delete
      (api.deleteAccountServiceOverride as jest.Mock).mockResolvedValue(undefined);
      mockConfirmDialog.mockResolvedValue(true);

      // Simulate a slow recommendations refresh that has not resolved by the
      // time we assert. Pre-fix, showToast was awaited behind this call, so a
      // slow refresh meant the Undo toast never appeared for the user; post-fix
      // the toast fires right after the delete resolves, independent of it.
      let resolveRefresh: () => void = () => {};
      const refreshGate = new Promise<void>(res => { resolveRefresh = res; });
      mockLoadRecommendations.mockImplementationOnce(() => refreshGate);

      const panel = document.createElement('div');
      document.body.appendChild(panel);
      await loadOverridesPanel('acc-1', panel, 'aws');

      const deleteBtn = Array.from(panel.querySelectorAll('button'))
        .find(b => b.textContent === 'Delete') as HTMLButtonElement;
      deleteBtn.click();

      // Flush the confirm + delete + panel-reload chain. The recommendations
      // refresh is still pending (resolveRefresh has NOT been called).
      for (let i = 0; i < 5; i++) { await new Promise(r => setTimeout(r, 0)); }

      // The Undo toast must already be visible even though the refresh is
      // still in flight.
      const undoToast = (mockShowToast.mock.calls.map(c => c[0]) as Array<{
        kind?: string;
        message?: string;
        actions?: Array<{ label: string }>;
        timeout?: number | null;
      }>).find(t => t.kind === 'info' && t.message?.includes('aws/rds'));
      expect(undoToast).toBeDefined();
      expect(undoToast!.actions?.[0]?.label).toBe('Undo');
      expect(undoToast!.timeout).toBe(10_000);

      // Let the pending refresh resolve so the async chain completes cleanly.
      resolveRefresh();
      for (let i = 0; i < 3; i++) { await new Promise(r => setTimeout(r, 0)); }
    });

    test('a delete API failure shows an error toast and no Undo (issue #1415)', async () => {
      (api.listAccountServiceOverrides as jest.Mock)
        .mockResolvedValueOnce([overrideFixture])
        .mockResolvedValue([]);
      (api.deleteAccountServiceOverride as jest.Mock)
        .mockRejectedValue(new Error('boom'));
      mockConfirmDialog.mockResolvedValue(true);

      const panel = document.createElement('div');
      document.body.appendChild(panel);
      await loadOverridesPanel('acc-1', panel, 'aws');

      const deleteBtn = Array.from(panel.querySelectorAll('button'))
        .find(b => b.textContent === 'Delete') as HTMLButtonElement;
      deleteBtn.click();
      for (let i = 0; i < 5; i++) { await new Promise(r => setTimeout(r, 0)); }

      const toastCalls = mockShowToast.mock.calls.map(c => c[0]) as Array<{
        kind?: string;
        message?: string;
        actions?: unknown[];
      }>;
      // The failure surfaces as an error toast with the delete-failure copy.
      expect(toastCalls.some(t => t.kind === 'error' && t.message?.includes('Failed to delete override'))).toBe(true);
      // No info/Undo toast — nothing was deleted, so there is nothing to undo.
      expect(toastCalls.some(t => t.kind === 'info' && !!t.actions)).toBe(false);
      // A failed delete must not trigger the recommendations refresh.
      expect(mockLoadRecommendations).not.toHaveBeenCalled();
    });

    test('let-it-expire path: override is permanently gone after toast timeout', async () => {
      (api.listAccountServiceOverrides as jest.Mock)
        .mockResolvedValueOnce([overrideFixture])
        .mockResolvedValue([]);
      (api.deleteAccountServiceOverride as jest.Mock).mockResolvedValue(undefined);
      mockConfirmDialog.mockResolvedValue(true);

      const panel = document.createElement('div');
      document.body.appendChild(panel);
      await loadOverridesPanel('acc-1', panel, 'aws');

      const deleteBtn = Array.from(panel.querySelectorAll('button'))
        .find(b => b.textContent === 'Delete') as HTMLButtonElement;
      deleteBtn.click();
      for (let i = 0; i < 5; i++) { await new Promise(r => setTimeout(r, 0)); }

      // Undo was NOT clicked; saveAccountServiceOverride must not have been called.
      expect(api.saveAccountServiceOverride).not.toHaveBeenCalled();
      // The delete did fire.
      expect(api.deleteAccountServiceOverride).toHaveBeenCalledTimes(1);
      // The panel reloaded to show the empty state (no more override rows).
      const table = panel.querySelector('table.overrides-table');
      expect(table).toBeNull();
    });
  });

  describe('override commitmentOptions parity (issue #107)', () => {
    test('inline payment selector hides invalid options for RDS term=3 row', async () => {
      // RDS rejects 3yr no-upfront per commitmentOptions invalidCombinations.
      // The inline payment selector on an existing RDS override with term=3
      // must not list no-upfront — the global Settings card already hides
      // it; this PR brings the override surfaces in line.
      (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
        {
          account_id: 'acc-1',
          provider: 'aws',
          service: 'rds',
          term: 3,
          payment: 'all-upfront',
          coverage: 80,
        },
      ]);

      const panel = document.createElement('div');
      document.body.appendChild(panel);
      await loadOverridesPanel('acc-1', panel, 'aws');

      const select = panel.querySelector<HTMLSelectElement>('select.override-payment-select');
      expect(select).not.toBeNull();
      const options = Array.from(select!.options).map(o => o.value);
      expect(options).toContain('all-upfront');
      expect(options).toContain('partial-upfront');
      expect(options).not.toContain('no-upfront');
    });

    test('inline payment selector shows full list when term is unset', async () => {
      // Without a term we can't pre-validate; fall back to the full list
      // and let the user pick. The submit-side guard would catch any
      // invalid combo.
      (api.listAccountServiceOverrides as jest.Mock).mockResolvedValue([
        {
          account_id: 'acc-1',
          provider: 'aws',
          service: 'rds',
          term: 0,
          payment: '',
          coverage: 80,
        },
      ]);

      const panel = document.createElement('div');
      document.body.appendChild(panel);
      await loadOverridesPanel('acc-1', panel, 'aws');

      const select = panel.querySelector<HTMLSelectElement>('select.override-payment-select');
      const options = Array.from(select!.options).map(o => o.value);
      expect(options).toContain('all-upfront');
      expect(options).toContain('partial-upfront');
      expect(options).toContain('no-upfront');
    });

    test('override-create modal hides invalid payment options on service+term change', async () => {
      // Build the override modal DOM the production app provides via createElement
      // (avoid innerHTML; same pattern as the rest of this test file).
      document.body.replaceChildren();
      const modal = document.createElement('div');
      modal.id = 'override-modal';
      modal.className = 'modal hidden';

      const form = document.createElement('form');
      form.id = 'override-form';

      const acctIdInput = document.createElement('input');
      acctIdInput.type = 'hidden';
      acctIdInput.id = 'override-account-id';
      form.appendChild(acctIdInput);

      const provInput = document.createElement('input');
      provInput.type = 'hidden';
      provInput.id = 'override-provider';
      form.appendChild(provInput);

      const svcSel = document.createElement('select');
      svcSel.id = 'override-service';
      form.appendChild(svcSel);

      const termSel = document.createElement('select');
      termSel.id = 'override-term';
      for (const v of ['', '1', '3']) {
        const o = document.createElement('option');
        o.value = v;
        termSel.appendChild(o);
      }
      form.appendChild(termSel);

      const paySel = document.createElement('select');
      paySel.id = 'override-payment';
      form.appendChild(paySel);

      const covInput = document.createElement('input');
      covInput.type = 'number';
      covInput.id = 'override-coverage';
      form.appendChild(covInput);

      const errEl = document.createElement('p');
      errEl.id = 'override-form-error';
      form.appendChild(errEl);

      const submitBtn = document.createElement('button');
      submitBtn.type = 'submit';
      form.appendChild(submitBtn);

      modal.appendChild(form);
      document.body.appendChild(modal);

      const panel = document.createElement('div');
      openOverrideModal('acc-1', 'aws', [], panel);

      const svc = document.getElementById('override-service') as HTMLSelectElement;
      const term = document.getElementById('override-term') as HTMLSelectElement;
      const pay = document.getElementById('override-payment') as HTMLSelectElement;

      // Pick rds + 3yr — payment dropdown must drop no-upfront.
      svc.value = 'rds';
      svc.dispatchEvent(new Event('change'));
      term.value = '3';
      term.dispatchEvent(new Event('change'));

      const options = Array.from(pay.options).map(o => o.value);
      expect(options).toContain(''); // Inherit
      expect(options).toContain('all-upfront');
      expect(options).toContain('partial-upfront');
      expect(options).not.toContain('no-upfront');

      // Switch term to 1 — no-upfront becomes valid again.
      term.value = '1';
      term.dispatchEvent(new Event('change'));
      const opts2 = Array.from(pay.options).map(o => o.value);
      expect(opts2).toContain('no-upfront');
    });
  });
});
