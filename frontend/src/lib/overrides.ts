import * as api from '../api';
import type { AccountServiceOverride } from '../api/accounts';

export interface FetchedOverrides {
  byAccount: Map<string, AccountServiceOverride[]>;
  /** Account IDs whose override fetch rejected; absent from byAccount. */
  failedIds: string[];
}

/**
 * Fetches AccountServiceOverride arrays for a set of account IDs in parallel.
 * Never rejects: a per-account failure is recorded in failedIds so callers
 * can warn the user before falling back to their default seed.
 */
export async function fetchOverridesForAccounts(
  ids: Iterable<string>,
): Promise<FetchedOverrides> {
  const byAccount = new Map<string, AccountServiceOverride[]>();
  const failedIds: string[] = [];
  await Promise.all(
    Array.from(ids).map(async (id) => {
      try {
        byAccount.set(id, await api.listAccountServiceOverrides(id));
      } catch (err) {
        console.warn(`Failed to load payment overrides for account ${id}`, err);
        failedIds.push(id);
      }
    }),
  );
  return { byAccount, failedIds };
}
