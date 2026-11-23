/**
 * History inline Retry button tests (issue #47).
 *
 * Mirror of the backend RBAC matrix in
 * internal/api/handler_purchases_retry.go::authorizeSessionRetry — keep both
 * sides in sync. The backend remains authoritative; these tests only
 * verify the UX gate (don't render buttons users can't use, render
 * the right control for the row's state).
 *
 * Tested matrix:
 *   1. admin sees Retry on every failed row (modulo ops_hint / lineage).
 *   2. regular user sees Retry only on rows they themselves created.
 *   3. Anonymous (no current user cached) sees no Retry buttons.
 *   4. ops_hint row → ops-hint badge replaces the Retry button.
 *   5. retry_attempt_n >= 5 → "Retried 5×" override button.
 *   6. retry_execution_id set → no Retry button (act on the descendant).
 *   7. lineage links: "↻ Retried as #abc" for predecessors, "↻ Retry #n" for retries.
 *   8. Click + decline confirmDialog → no API call.
 *   9. Click + accept → retryPurchase + reload + success toast (email_sent true).
 *  10. retryPurchase rejects → toast.error + button re-enabled.
 *  11. Over-threshold click sends force=true.
 *  12. email_sent absent but status=pending → success toast.
 *  13. email_sent false → warning toast (approval email failed).
 *  14. email_sent false + status pending → warning toast (explicit failure overrides status).
 */

// The harness registers the module mocks, so it must be imported first.
import {
  ADMIN_USER,
  REG_USER,
  ADMIN_NO_PURCH,
  OTHER_UUID,
  setupDOM,
  makeRow,
} from './history-retry-button-harness';
import { loadHistory } from '../history';
import * as api from '../api';
import { confirmDialog } from '../confirmDialog';
import { showToast } from '../toast';
import { getCurrentUser } from '../state';
import { flushPromises } from './flush-promises';

describe('History inline Retry button (issue #47)', () => {
  beforeEach(() => {
    setupDOM();
    jest.clearAllMocks();
  });

  test('admin sees Retry on every failed row regardless of creator', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'fail-mine', created_by_user_id: ADMIN_USER.id }),
        makeRow({ purchase_id: 'fail-other', created_by_user_id: OTHER_UUID }),
        makeRow({ purchase_id: 'fail-legacy', created_by_user_id: undefined }),
      ],
    });

    await loadHistory();

    const buttons = document.querySelectorAll<HTMLButtonElement>('.history-retry-btn');
    const ids = Array.from(buttons).map((b) => b.dataset['retryId']);
    expect(ids).toEqual(expect.arrayContaining(['fail-mine', 'fail-other', 'fail-legacy']));
    expect(ids).toHaveLength(3);
  });

  test('regular user sees Retry only on rows they created', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(REG_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'fail-mine', created_by_user_id: REG_USER.id }),
        makeRow({ purchase_id: 'fail-other', created_by_user_id: OTHER_UUID }),
        makeRow({ purchase_id: 'fail-legacy', created_by_user_id: undefined }),
      ],
    });

    await loadHistory();

    const buttons = document.querySelectorAll<HTMLButtonElement>('.history-retry-btn');
    const ids = Array.from(buttons).map((b) => b.dataset['retryId']);
    expect(ids).toEqual(['fail-mine']);
  });

  test('renders no Retry buttons when no current user is cached', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(null);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ created_by_user_id: 'anything' })],
    });

    await loadHistory();
    expect(document.querySelectorAll('.history-retry-btn')).toHaveLength(0);
  });

  test('Retry button absent on non-failed rows even for admin', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'r-pending', status: 'pending', created_by_user_id: ADMIN_USER.id }),
        makeRow({ purchase_id: 'r-completed', status: 'completed', created_by_user_id: ADMIN_USER.id }),
        makeRow({ purchase_id: 'r-cancelled', status: 'cancelled', created_by_user_id: ADMIN_USER.id }),
        makeRow({ purchase_id: 'r-expired', status: 'expired', created_by_user_id: ADMIN_USER.id }),
      ],
    });

    await loadHistory();
    expect(document.querySelectorAll('.history-retry-btn')).toHaveLength(0);
  });

  test('ops_hint replaces the Retry button on failed rows', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'r-1', created_by_user_id: ADMIN_USER.id, ops_hint: 'Set FROM_EMAIL tfvar then retry' }),
      ],
    });

    await loadHistory();
    expect(document.querySelectorAll('.history-retry-btn')).toHaveLength(0);
    const hint = document.querySelector('.history-ops-hint');
    expect(hint).not.toBeNull();
    expect(hint?.textContent).toContain('FROM_EMAIL');
    expect(hint?.getAttribute('title')).toContain('Operator action required');
  });

  test('redrive_unsafe row hides Retry and words the hint as permanent', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({
          purchase_id: 'r-1',
          created_by_user_id: ADMIN_USER.id,
          ops_hint: 'Azure savings plans have no provider-side duplicate guard',
          redrive_unsafe: true,
        }),
      ],
    });

    await loadHistory();
    expect(document.querySelectorAll('.history-retry-btn')).toHaveLength(0);
    const hint = document.querySelector('.history-ops-hint');
    expect(hint?.textContent).toContain('Azure savings plans');
    expect(hint?.getAttribute('title')).toContain('Permanent: this execution can never be retried; check the account');
    expect(hint?.getAttribute('title')).not.toContain('Operator action required');
  });

  test('ops_hint row keeps its lineage links next to the hint (issue #1040)', async () => {
    // A legacy row retried before the re-drive gate (#967) now carries the
    // permanent hint; it must still link to its successor, and Retry stays hidden.
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({
          purchase_id: 'r-legacy',
          created_by_user_id: ADMIN_USER.id,
          ops_hint: 'Azure savings plans have no provider-side duplicate guard',
          redrive_unsafe: true,
          retry_execution_id: 'succ-5678-aaaa',
          retry_attempt_n: 2,
        }),
      ],
    });

    await loadHistory();
    const row = document.querySelector('#history-list tr[data-execution-id="r-legacy"]')!;
    expect(row.querySelector('.history-ops-hint')?.textContent).toContain('Azure savings plans');
    const link = row.querySelector<HTMLAnchorElement>('a.history-retry-link');
    expect(link?.textContent).toContain('Retried as #succ-567');
    expect(link?.getAttribute('href')).toBe('#history?execution=succ-5678-aaaa');
    expect(row.querySelector('.history-retry-of')?.textContent).toContain('Retry #2');
    expect(row.querySelectorAll('.history-retry-btn')).toHaveLength(0);
  });

  test('ops_hint row without lineage renders the hint alone', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'r-1', created_by_user_id: ADMIN_USER.id, ops_hint: 'Set FROM_EMAIL tfvar then retry' }),
      ],
    });

    await loadHistory();
    const row = document.querySelector('#history-list tr[data-execution-id="r-1"]')!;
    expect(row.querySelector('.history-ops-hint')).not.toBeNull();
    expect(row.querySelectorAll('.history-retry-link')).toHaveLength(0);
  });

  test('threshold-reached row renders override button + sends force=true on accept', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(true);
    (api.retryPurchase as jest.Mock).mockResolvedValue({
      execution_id: 'new',
      original_execution: 'r-1',
      status: 'pending',
      retry_attempt_n: 6,
      email_sent: true,
    });
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'r-1', created_by_user_id: ADMIN_USER.id, retry_attempt_n: 5 }),
      ],
    });

    await loadHistory();
    const btn = document.querySelector<HTMLButtonElement>('.history-retry-btn');
    expect(btn?.classList.contains('history-retry-over-threshold')).toBe(true);
    expect(btn?.textContent).toContain('Retried 5×');

    btn?.click();
    await flushPromises();

    expect(api.retryPurchase).toHaveBeenCalledWith('r-1', { force: true });
  });

  test('retry_execution_id set → no Retry button, lineage link rendered', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'old-1', created_by_user_id: ADMIN_USER.id, retry_execution_id: 'abcdef0123456789' }),
      ],
    });

    await loadHistory();
    expect(document.querySelectorAll('.history-retry-btn')).toHaveLength(0);
    const link = document.querySelector('.history-retry-link');
    expect(link).not.toBeNull();
    expect(link?.textContent).toContain('Retried as');
    expect(link?.getAttribute('href')).toContain('execution=abcdef0123456789');
  });

  test('retry_attempt_n > 0 row gets a "Retry #n" provenance badge', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        // n=2 retry that hasn't been retried again — eligible to retry,
        // so we ALSO see the Retry button. This exercises the case where
        // a middle-of-chain row has BOTH ↻ Retry button AND lineage
        // metadata visible to the user.
        makeRow({ purchase_id: 'mid-2', status: 'completed', created_by_user_id: ADMIN_USER.id, retry_attempt_n: 2 }),
      ],
    });

    await loadHistory();
    const badge = document.querySelector('.history-retry-of');
    expect(badge).not.toBeNull();
    expect(badge?.textContent).toContain('Retry #2');
  });

  test('declined confirm dialog skips API call', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(false);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'r-1', created_by_user_id: ADMIN_USER.id })],
    });

    await loadHistory();
    const btn = document.querySelector<HTMLButtonElement>('.history-retry-btn');
    btn?.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(confirmDialog).toHaveBeenCalled();
    expect(api.retryPurchase).not.toHaveBeenCalled();
  });

  test('confirm names the purchase being retried (issue #728)', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(false);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'r-1', service: 'rds', resource_type: 'db.r5.large', count: 2, created_by_user_id: ADMIN_USER.id })],
    });

    await loadHistory();
    document.querySelector<HTMLButtonElement>('.history-retry-btn')!.click();
    await new Promise((r) => setTimeout(r, 0));

    const body = (confirmDialog as jest.Mock).mock.calls[0][0].body as HTMLElement;
    expect(body.querySelector('.history-confirm-summary strong')?.textContent).toBe('AWS RDS db.r5.large × 2 in us-east-1');
    expect(body.textContent).toContain('This will create a new purchase execution');
  });

  test('accepted confirm posts retry + reloads + success toast when email_sent is true', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(true);
    (api.retryPurchase as jest.Mock).mockResolvedValue({
      execution_id: 'new',
      original_execution: 'r-1',
      status: 'pending',
      retry_attempt_n: 1,
      email_sent: true,
    });
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'r-1', created_by_user_id: ADMIN_USER.id })],
    });

    await loadHistory();
    expect(api.getHistory).toHaveBeenCalledTimes(1);
    const btn = document.querySelector<HTMLButtonElement>('.history-retry-btn');
    btn?.click();
    await flushPromises();

    expect(api.retryPurchase).toHaveBeenCalledWith('r-1', undefined);
    expect(api.getHistory).toHaveBeenCalledTimes(2);
    expect(showToast).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'success', message: 'Purchase request sent for approval' }),
    );
  });

  test('retry success toast uses approval wording when status is pending but email_sent absent', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(true);
    // Backend may omit email_sent; status==='pending' is sufficient.
    (api.retryPurchase as jest.Mock).mockResolvedValue({
      execution_id: 'new',
      original_execution: 'r-1',
      status: 'pending',
      retry_attempt_n: 1,
    });
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'r-1', created_by_user_id: ADMIN_USER.id })],
    });

    await loadHistory();
    const btn = document.querySelector<HTMLButtonElement>('.history-retry-btn');
    btn?.click();
    await flushPromises();

    expect(showToast).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'success', message: 'Purchase request sent for approval' }),
    );
  });

  test('retry shows warning toast when email_sent is false', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(true);
    (api.retryPurchase as jest.Mock).mockResolvedValue({
      execution_id: 'new',
      original_execution: 'r-1',
      status: 'failed',
      retry_attempt_n: 1,
      email_sent: false,
    });
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'r-1', created_by_user_id: ADMIN_USER.id })],
    });
    console.error = jest.fn();

    await loadHistory();
    const btn = document.querySelector<HTMLButtonElement>('.history-retry-btn');
    btn?.click();
    await flushPromises();

    expect(showToast).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: 'warning',
        message: expect.stringContaining('approval email failed'),
      }),
    );
  });

  test('email_sent false overrides pending status - shows warning not success toast', async () => {
    // This is the case CR round 2 identified: email_sent===false must win over
    // status==='pending'. The prior logic short-circuited on the status check
    // and could produce a false success toast on conflicting payloads.
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(true);
    (api.retryPurchase as jest.Mock).mockResolvedValue({
      execution_id: 'new',
      original_execution: 'r-1',
      status: 'pending',
      retry_attempt_n: 1,
      email_sent: false,
    });
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'r-1', created_by_user_id: ADMIN_USER.id })],
    });

    await loadHistory();
    const btn = document.querySelector<HTMLButtonElement>('.history-retry-btn');
    btn?.click();
    await flushPromises();

    expect(showToast).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: 'warning',
        message: expect.stringContaining('approval email failed'),
      }),
    );
    expect(showToast).not.toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'success' }),
    );
  });

  test('retry API failure surfaces toast and re-enables the button', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(true);
    (api.retryPurchase as jest.Mock).mockRejectedValue(new Error('boom'));
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'r-1', created_by_user_id: ADMIN_USER.id })],
    });
    console.error = jest.fn();

    await loadHistory();
    const btn = document.querySelector<HTMLButtonElement>('.history-retry-btn');
    btn?.click();
    await flushPromises();

    expect(showToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error', timeout: null }));
    expect(btn?.disabled).toBe(false);
  });

  test('Redshift settle 409 with retry_after_seconds shows a wait toast, not a generic error', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
    (confirmDialog as jest.Mock).mockResolvedValue(true);
    const settleError = Object.assign(new Error('this Redshift purchase failed too recently to retry safely; retry after the wait'), {
      details: { retry_after_seconds: 90 },
    });
    (api.retryPurchase as jest.Mock).mockRejectedValue(settleError);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [makeRow({ purchase_id: 'r-1', created_by_user_id: ADMIN_USER.id })],
    });
    console.error = jest.fn();

    await loadHistory();
    document.querySelector<HTMLButtonElement>('.history-retry-btn')?.click();
    await flushPromises();

    expect(showToast).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: 'warning',
        message: expect.stringContaining('Try again in 2 minutes'),
      }),
    );
    expect(showToast).not.toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));
  });

  test('admin WITHOUT Purchaser membership does not see Retry on rows they did not create (CR #924 F5)', async () => {
    // Issue #923 + CR #924 F5: retry-any:purchases is carved out of
    // admin:*. canRetryFailedRow must gate on
    // canAccess('retry-any', 'purchases'), NOT on isAdmin() or
    // isPurchaser() group membership alone. A bare admin (no Purchaser
    // group, no effectivePermissions yet) is exactly the case where
    // the carve-out matters: the legacy implementation would have
    // shown Retry on every failed row.
    (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_NO_PURCH);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'fail-mine', created_by_user_id: ADMIN_NO_PURCH.id }),
        makeRow({ purchase_id: 'fail-other', created_by_user_id: OTHER_UUID }),
        makeRow({ purchase_id: 'fail-legacy', created_by_user_id: undefined }),
      ],
    });

    await loadHistory();

    const buttons = document.querySelectorAll<HTMLButtonElement>('.history-retry-btn');
    const ids = Array.from(buttons).map((b) => b.dataset['retryId']);
    // Retry renders only via the retry-own fallback (matching
    // created_by_user_id), NOT retry-any.
    expect(ids).toEqual(['fail-mine']);
  });
});
