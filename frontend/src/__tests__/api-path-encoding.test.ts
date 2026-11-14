/**
 * Every id interpolated into an API path is percent-encoded, so an id holding
 * `/`, `?`, `#` or `..` cannot re-route a request to another endpoint (#134).
 */
import { fetchMock } from './setup';
import { clearAuth, setAuthToken } from '../api/client';
import { getGroup, deleteGroup } from '../api/groups';
import { getPlan, patchPlan } from '../api/plans';
import { getUser, deleteUser } from '../api/users';
import { revokeApiKey, deleteApiKey } from '../api/apikeys';
import { getRegistration } from '../api/registrations';
import { getAccount, saveAccountServiceOverride, listPlanAccounts, setPlanAccounts } from '../api/accounts';
import { getPurchaseDetails, getRevokeQuote, cancelMarketplaceListing, pausePlannedPurchase, retryPurchase } from '../api/purchases';

const EVIL = 'a/../b?x#y';
const ENC = encodeURIComponent(EVIL);

describe('API path ids are encoded', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    clearAuth();
    setAuthToken('test-token');
    fetchMock.mockResolvedValue({ ok: true, status: 200, json: () => Promise.resolve({}) });
  });

  const calls: Array<[string, () => Promise<unknown>, string]> = [
    ['getGroup', () => getGroup(EVIL), `/api/groups/${ENC}`],
    ['deleteGroup', () => deleteGroup(EVIL), `/api/groups/${ENC}`],
    ['getPlan', () => getPlan(EVIL), `/api/plans/${ENC}`],
    ['patchPlan', () => patchPlan(EVIL, {}), `/api/plans/${ENC}`],
    ['getUser', () => getUser(EVIL), `/api/users/${ENC}`],
    ['deleteUser', () => deleteUser(EVIL), `/api/users/${ENC}`],
    ['revokeApiKey', () => revokeApiKey(EVIL), `/api/api-keys/${ENC}/revoke`],
    ['deleteApiKey', () => deleteApiKey(EVIL), `/api/api-keys/${ENC}`],
    ['getRegistration', () => getRegistration(EVIL), `/api/registrations/${ENC}`],
    ['getAccount', () => getAccount(EVIL), `/api/accounts/${ENC}`],
    [
      'saveAccountServiceOverride',
      () => saveAccountServiceOverride('acc', 'aws', EVIL, {} as never),
      `/api/accounts/acc/service-overrides/aws/${ENC}`,
    ],
    ['getPurchaseDetails', () => getPurchaseDetails(EVIL), `/api/purchases/${ENC}`],
    ['getRevokeQuote', () => getRevokeQuote(EVIL), `/api/purchases/${ENC}/revoke/calculate`],
    ['cancelMarketplaceListing', () => cancelMarketplaceListing(EVIL), `/api/purchases/${ENC}/marketplace-cancel`],
    ['pausePlannedPurchase', () => pausePlannedPurchase(EVIL), `/api/purchases/planned/${ENC}/pause`],
    ['retryPurchase', () => retryPurchase(EVIL), `/api/purchases/retry/${ENC}`],
    ['retryPurchase force', () => retryPurchase(EVIL, { force: true }), `/api/purchases/retry/${ENC}?force=true`],
    ['listPlanAccounts', () => listPlanAccounts(EVIL), `/api/plans/${ENC}/accounts`],
    ['setPlanAccounts', () => setPlanAccounts(EVIL, ['a']), `/api/plans/${ENC}/accounts`],
  ];

  test.each(calls)('%s', async (_name, call, expectedUrl) => {
    await call();
    expect(fetchMock.mock.calls[0]![0]).toBe(expectedUrl);
  });
});
