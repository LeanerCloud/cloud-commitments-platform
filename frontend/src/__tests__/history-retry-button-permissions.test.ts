/**
 * History Retry button: retry-own permission gate (issue #1418) and the
 * deferred-confirmation row ownership (#249). Split from
 * history-retry-button.test.ts to stay under the 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import { REG_USER, ADMIN_USER, OTHER_UUID, setupDOM, makeRow } from './history-retry-button-harness';
import { loadHistory } from '../history';
import * as api from '../api';
import { confirmDialog } from '../confirmDialog';
import { getCurrentUser } from '../state';

// Issue #1418 regression: user without retry-own:purchases sees no Retry button
// even on rows they created (closes the same pattern fixed for approve-own by
// PR #1422 / issue #1407).
describe('History retry-own permission gate (issue #1418)', () => {
  beforeEach(() => {
    setupDOM();
    jest.clearAllMocks();
  });

  test('user without retry-own:purchases sees no Retry button on own failed row', async () => {
    // A Plan Authors-style user: holds plan verbs only, no purchase retry perm.
    const planAuthorUser = {
      id: 'plan-author-uuid',
      email: 'plan-author@example.com',
      groups: [],
      effectivePermissions: [
        { action: 'create', resource: 'plans' },
        { action: 'update', resource: 'plans' },
        { action: 'delete', resource: 'plans' },
      ],
    };
    (getCurrentUser as jest.Mock).mockReturnValue(planAuthorUser);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'own-fail', created_by_user_id: planAuthorUser.id }),
      ],
    });

    await loadHistory();

    // Fail-before: without the retry-own check the button appeared; now it must not.
    expect(document.querySelectorAll('.history-retry-btn')).toHaveLength(0);
  });

  test('user with retry-own:purchases sees Retry on own failed rows', async () => {
    (getCurrentUser as jest.Mock).mockReturnValue(REG_USER);
    (api.getHistory as jest.Mock).mockResolvedValue({
      summary: {},
      purchases: [
        makeRow({ purchase_id: 'own-fail', created_by_user_id: REG_USER.id }),
        makeRow({ purchase_id: 'other-fail', created_by_user_id: OTHER_UUID }),
      ],
    });

    await loadHistory();

    const buttons = document.querySelectorAll<HTMLButtonElement>('.history-retry-btn');
    const ids = Array.from(buttons).map((b) => b.dataset['retryId']);
    expect(ids).toEqual(['own-fail']);
  });
});

 test('retry owns its row while confirmation is deferred and permits an explicit retry (#249)', async () => {
  setupDOM();
  jest.clearAllMocks();
  (getCurrentUser as jest.Mock).mockReturnValue(ADMIN_USER);
  (api.getHistory as jest.Mock).mockResolvedValue({ summary: {}, purchases: [makeRow({ retry_attempt_n: 5 })] });
  (api.retryPurchase as jest.Mock).mockResolvedValue({ status: 'pending', email_sent: true });
  let resolveConfirm!: (value: boolean) => void;
  (confirmDialog as jest.Mock).mockImplementationOnce(() => new Promise<boolean>(resolve => { resolveConfirm = resolve; }));
  await loadHistory();
  const btn = document.querySelector<HTMLButtonElement>('#history-list .history-retry-btn')!;
  btn.click();
  btn.dispatchEvent(new MouseEvent('click'));
  const disabledDuringConfirmation = btn.disabled;
  resolveConfirm(false);
  await new Promise(resolve => setTimeout(resolve, 0));
  expect(disabledDuringConfirmation).toBe(true);
  expect(confirmDialog).toHaveBeenCalledTimes(1);
  expect(api.retryPurchase).not.toHaveBeenCalled();
  expect(btn.disabled).toBe(false);
  (confirmDialog as jest.Mock).mockResolvedValue(true);
  btn.click();
  await new Promise(resolve => setTimeout(resolve, 0));
  expect(api.retryPurchase).toHaveBeenCalledTimes(1);
 });
