import * as api from '../api';
import { fetchOverridesForAccounts } from '../lib/overrides';

jest.mock('../api', () => ({
  listAccountServiceOverrides: jest.fn(),
}));

const listMock = api.listAccountServiceOverrides as jest.Mock;

describe('fetchOverridesForAccounts', () => {
  beforeEach(() => jest.resetAllMocks());

  test('no failures: every account in byAccount, failedIds empty', async () => {
    listMock.mockResolvedValue([]);
    const { byAccount, failedIds } = await fetchOverridesForAccounts(['a', 'b']);
    expect([...byAccount.keys()].sort()).toEqual(['a', 'b']);
    expect(failedIds).toEqual([]);
  });

  test('partial failure keeps the good overrides and records the bad id', async () => {
    const good = [{ id: 'o1', account_id: 'a', provider: 'aws', service: 'ec2', payment: 'no-upfront' }];
    listMock.mockImplementation(async (id: string) => {
      if (id === 'bad') throw new Error('boom');
      return good;
    });
    const { byAccount, failedIds } = await fetchOverridesForAccounts(['a', 'bad', 'c']);
    expect([...byAccount.keys()].sort()).toEqual(['a', 'c']);
    expect(byAccount.get('a')).toBe(good);
    expect(byAccount.has('bad')).toBe(false);
    expect(failedIds).toEqual(['bad']);
  });

  test('all fail: empty byAccount, every id in failedIds, no rejection', async () => {
    listMock.mockRejectedValue(new Error('down'));
    const { byAccount, failedIds } = await fetchOverridesForAccounts(['a', 'b']);
    expect(byAccount.size).toBe(0);
    expect([...failedIds].sort()).toEqual(['a', 'b']);
  });
});
