/**
 * History inline Approve button tests (issue #286).
 *
 * Mirror of the backend RBAC matrix in
 * internal/api/handler_purchases_approve.go::authorizeSessionApprove — keep
 * both sides in sync. The backend remains authoritative; these tests
 * only verify the UX gate (don't render buttons users can't use).
 *
 * Tested matrix:
 *   1. admin sees Approve on every pending row, regardless of creator.
 *   2. regular user sees Approve only on rows they themselves created.
 *   3. Anonymous (no current user cached) sees no Approve buttons.
 *   4. Approve button absent for non-pending rows (completed, cancelled, ...).
 *   5. Click + decline confirmDialog → no API call.
 *   6. Click + accept → approvePurchase + reload + toast.
 *   7. approvePurchase rejects → toast.error + button re-enabled.
 *   8. Approve and Cancel render together for pending rows where the
 *      session qualifies for both verbs.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  ADMIN_USER,
  ADMIN_NO_PURCH,
  REG_USER,
  OTHER_UUID,
  DETAILS,
  setupDOM,
  makeRow,
} from './history-approve-button-harness';
import { loadHistory } from '../history';
import * as api from '../api';
import { confirmDialog } from '../confirmDialog';
import { showToast } from '../toast';
import { getCurrentUser } from '../state';
import { flushPromises } from './flush-promises';

describe('History inline Approve button (issue #286)', () => {
  beforeEach(() => {
    setupDOM();
    jest.clearAllMocks();
    (api.getPurchaseDetails as jest.Mock).mockResolvedValue(DETAILS);
  });

  test('admin sees Approve on every pending row, regardless of creator', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'exec-mine', created_by_user_id: ADMIN_USER.id }),
        makeRow({ purchase_id: 'exec-other', created_by_user_id: OTHER_UUID }),
        makeRow({ purchase_id: 'exec-legacy', created_by_user_id: undefined }),
      ],
    });

    await loadHistory();

    // The Approval queue card is the only approval surface (issue #183).
    const list = document.getElementById('purchases-approval-queue')!;
    const buttons = list.querySelectorAll<HTMLButtonElement>('.history-approve-btn');
    const ids = Array.from(buttons).map((b) => b.dataset['approveId']);
    expect(ids).toEqual(expect.arrayContaining(['exec-mine', 'exec-other', 'exec-legacy']));
    expect(ids).toHaveLength(3);
  });

  test('regular user sees Approve only on rows they created', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(REG_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'exec-mine', created_by_user_id: REG_USER.id }),
        makeRow({ purchase_id: 'exec-other', created_by_user_id: OTHER_UUID }),
        makeRow({ purchase_id: 'exec-legacy', created_by_user_id: undefined }),
      ],
    });

    await loadHistory();

    // The Approval queue card is the only approval surface (issue #183).
    const list = document.getElementById('purchases-approval-queue')!;
    const buttons = list.querySelectorAll<HTMLButtonElement>('.history-approve-btn');
    const ids = Array.from(buttons).map((b) => b.dataset['approveId']);
    expect(ids).toEqual(['exec-mine']);
  });

  test('renders no Approve buttons when no current user is cached', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(null);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ created_by_user_id: 'anything' })],
    });

    await loadHistory();

    expect(document.querySelectorAll('.history-approve-btn')).toHaveLength(0);
  });

  test('Approve button absent on non-pending rows even for admin', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'exec-completed', status: 'completed', created_by_user_id: ADMIN_USER.id }),
        makeRow({ purchase_id: 'exec-cancelled', status: 'cancelled', created_by_user_id: ADMIN_USER.id }),
        makeRow({ purchase_id: 'exec-failed', status: 'failed', created_by_user_id: ADMIN_USER.id }),
        // Sanity: notified is treated like pending and SHOULD render the button.
        makeRow({ purchase_id: 'exec-notified', status: 'notified', created_by_user_id: ADMIN_USER.id }),
      ],
    });

    await loadHistory();

    // The Approval queue card is the only approval surface (issue #183).
    const list = document.getElementById('purchases-approval-queue')!;
    const ids = Array.from(list.querySelectorAll<HTMLButtonElement>('.history-approve-btn'))
      .map((b) => b.dataset['approveId']);
    expect(ids).toEqual(['exec-notified']);
  });

  test('Approve and Cancel render side-by-side for pending rows the session qualifies for', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(REG_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'exec-1', created_by_user_id: REG_USER.id })],
    });

    await loadHistory();

    // The Approval queue card is the only approval surface (issue #183).
    const list = document.getElementById('purchases-approval-queue')!;
    const approveBtns = list.querySelectorAll<HTMLButtonElement>('.history-approve-btn[data-approve-id]');
    const cancelBtns = list.querySelectorAll<HTMLButtonElement>('.history-cancel-btn[data-cancel-id]');
    expect(approveBtns).toHaveLength(1);
    expect(cancelBtns).toHaveLength(1);
    expect(approveBtns[0]?.dataset['approveId']).toBe('exec-1');
    expect(cancelBtns[0]?.dataset['cancelId']).toBe('exec-1');
  });

  test('declined confirm dialog skips API call', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(false);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'exec-1', created_by_user_id: ADMIN_USER.id })],
    });

    await loadHistory();
    const btn = document.querySelector<HTMLButtonElement>('.history-approve-btn');
    btn?.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(confirmDialog).toHaveBeenCalled();
    expect(api.approvePurchase).not.toHaveBeenCalled();
  });

  test('accepted confirm posts approve + reloads + toasts', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(true);
    (api.approvePurchase as jest.Mock).mockResolvedValue(undefined);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'exec-1', created_by_user_id: ADMIN_USER.id })],
    });

    await loadHistory();
    expect(api.getHistory).toHaveBeenCalledTimes(1);

    const btn = document.querySelector<HTMLButtonElement>('.history-approve-btn');
    btn?.click();
    await flushPromises();

    expect(api.approvePurchase).toHaveBeenCalledWith('exec-1');
    // Reload was triggered → second getHistory call.
    expect(api.getHistory).toHaveBeenCalledTimes(2);
    expect(showToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'success' }));
  });

  test('approve API failure surfaces toast and re-enables the button', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(true);
    (api.approvePurchase as jest.Mock).mockRejectedValue(new Error('boom'));
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'exec-1', created_by_user_id: ADMIN_USER.id })],
    });
    console.error = jest.fn();

    await loadHistory();
    const btn = document.querySelector<HTMLButtonElement>('.history-approve-btn');
    btn?.click();
    await flushPromises();

    expect(showToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));
    expect(btn?.disabled).toBe(false);
  });

  test('approve click disables BOTH Approve and Cancel for the row while in flight (CR pass)', async () => {
    // Issue #286 + CR pass on PR #299: Approve and Cancel can render
    // together. Clicking Approve should disable BOTH buttons until the
    // request completes — otherwise a quick double-click could fire
    // a conflicting Cancel on the same row before the reload runs.
    (getCurrentUser as jest.Mock).mockReturnValue(REG_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(true);
    let resolveApprove: (() => void) | undefined;
    (api.approvePurchase as jest.Mock).mockReturnValue(
      new Promise<void>((r) => { resolveApprove = r; }),
    );
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'exec-1', created_by_user_id: REG_USER.id })],
    });

    await loadHistory();
    // Scope to the queue for the disable assertion. The click
    // handler scopes its disable to the clicked button's containing
    // <td> (sameRowActions), so we need both buttons from the SAME row
    // of the SAME view to verify the pair-disable behaviour.
    const list = document.getElementById('purchases-approval-queue')!;
    const approveBtn = list.querySelector<HTMLButtonElement>('.history-approve-btn');
    const cancelBtn = list.querySelector<HTMLButtonElement>('.history-cancel-btn');
    expect(approveBtn).not.toBeNull();
    expect(cancelBtn).not.toBeNull();
    expect(approveBtn?.disabled).toBe(false);
    expect(cancelBtn?.disabled).toBe(false);

    approveBtn?.click();
    // Let the click handler reach the disable step (microtasks for the
    // confirmDialog await chain).
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));

    // While the API call is unresolved, BOTH buttons are disabled.
    expect(approveBtn?.disabled).toBe(true);
    expect(cancelBtn?.disabled).toBe(true);

    // Resolve the in-flight promise + let the success path complete.
    resolveApprove?.();
    await flushPromises();
  });

  test('approve API failure re-enables BOTH Approve and Cancel for the row (CR pass)', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(REG_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(true);
    (api.approvePurchase as jest.Mock).mockRejectedValue(new Error('boom'));
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'exec-1', created_by_user_id: REG_USER.id })],
    });
    console.error = jest.fn();

    await loadHistory();
    const approveBtn = document.querySelector<HTMLButtonElement>('.history-approve-btn');
    const cancelBtn = document.querySelector<HTMLButtonElement>('.history-cancel-btn');
    approveBtn?.click();
    await flushPromises();

    // After the API rejects, BOTH buttons must be re-enabled.
    expect(approveBtn?.disabled).toBe(false);
    expect(cancelBtn?.disabled).toBe(false);
    expect(showToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));
  });

  test('user without any approve permission sees NO Approve button even on their own rows (issue #1407 four-eyes)', async () => {
    // Regression guard for issue #1407. Before the fix, canApprovePendingRow
    // returned true based on ownership alone (created_by_user_id === user.id)
    // without verifying that the session holds approve-own or approve-any.
    // This caused Viewer-role users (no approve-* in effectivePermissions) to
    // see the Approve button on purchases they submitted.
    //
    // After the fix, approve-own must be checked explicitly before ownership is
    // evaluated; a session without it sees no Approve buttons at all.
    const VIEWER_USER = {
      id: 'viewer-uuid',
      email: 'viewer@example.com',
      groups: [],
      effectivePermissions: [
        { action: 'view', resource: 'recommendations' },
        { action: 'view', resource: 'plans' },
        { action: 'view', resource: 'history' },
        // cancel-own / retry-own / revoke-own present; approve-own intentionally absent.
        { action: 'cancel-own', resource: 'purchases' },
      ],
    };

    (getCurrentUser as jest.Mock).mockReturnValue(VIEWER_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        // Own row: before fix this showed Approve; after fix it must NOT.
        makeRow({ purchase_id: 'exec-mine', created_by_user_id: VIEWER_USER.id }),
        makeRow({ purchase_id: 'exec-other', created_by_user_id: OTHER_UUID }),
      ],
    });

    await loadHistory();

    // The Approval queue card is the only approval surface (issue #183).
    const list = document.getElementById('purchases-approval-queue')!;
    const buttons = list.querySelectorAll<HTMLButtonElement>('.history-approve-btn');
    // No Approve buttons must appear for any row when the session has no
    // approve-own or approve-any permission.
    expect(buttons).toHaveLength(0);
  });

  test('admin WITHOUT Purchaser membership does not see Approve on rows they did not create (CR #924 F5)', async () => {
    // Issue #923 + CR #924 F5: approve-any:purchases is carved out of
    // admin:*. canApprovePendingRow must gate on
    // canAccess('approve-any', 'purchases'), NOT on isAdmin() or
    // isPurchaser() group membership alone. A bare admin (no Purchaser
    // group, no effectivePermissions yet) is exactly the case where
    // the carve-out matters: the legacy implementation would have
    // shown Approve on every pending row.
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_NO_PURCH);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'exec-mine', created_by_user_id: ADMIN_NO_PURCH.id }),
        makeRow({ purchase_id: 'exec-other', created_by_user_id: OTHER_UUID }),
        makeRow({ purchase_id: 'exec-legacy', created_by_user_id: undefined }),
      ],
    });

    await loadHistory();

    // The Approval queue card is the only approval surface (issue #183).
    const list = document.getElementById('purchases-approval-queue')!;
    const buttons = list.querySelectorAll<HTMLButtonElement>('.history-approve-btn');
    const ids = Array.from(buttons).map((b) => b.dataset['approveId']);
    // Approve renders only via the approve-own fallback (matching
    // created_by_user_id), NOT approve-any.
    expect(ids).toEqual(['exec-mine']);
  });
});

// #320: _fourEyesMode starts ON, matching the fail-closed default that
// refreshFourEyesMode applies when the config is unreadable. A render that
// runs before the first config load must not offer the creator Approve.
describe('dual-control default before the first config load (#320)', () => {
  test('renderApprovalQueue hides Approve on the creator\'s own row until the config says dual control is off', async () => {
    setupDOM();
    let history!: typeof import('../history');
    let freshState!: { getCurrentUser: jest.Mock };
    let freshApi!: { getConfig: jest.Mock; getHistory: jest.Mock };
    jest.isolateModules(() => {
      freshState = jest.requireMock('../state');
      freshApi = jest.requireMock('../api');
      history = jest.requireActual('../history');
    });
    freshState.getCurrentUser.mockReturnValue(REG_USER);
    const row = makeRow({ purchase_id: 'exec-own', created_by_user_id: REG_USER.id });
    const approveIds = (): (string | undefined)[] => Array.from(
      document.querySelectorAll<HTMLButtonElement>('#purchases-approval-queue .history-approve-btn'),
      b => b.dataset['approveId'],
    );

    history.renderApprovalQueue([row] as never);
    expect(approveIds()).toEqual([]);

    freshApi.getConfig.mockResolvedValue({ global: { require_different_approver: false } });
    freshApi.getHistory.mockResolvedValue({ summary: {}, purchases: [row] });
    await history.loadHistory();
    expect(approveIds()).toEqual(['exec-own']);
  });
});
