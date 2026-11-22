/**
 * Real (unmocked) tests for the column-filter slices in stateflow/columnFilters.ts
 * (moved from state.ts in #210). Other suites mock ../state, so none of them
 * exercise these functions.
 */
import * as cf from '../stateflow/columnFilters';
import * as state from '../state';

const SET: { kind: 'set'; values: string[] } = { kind: 'set', values: ['a', 'b'] };
const EXPR: { kind: 'expr'; expr: string } = { kind: 'expr', expr: '>5' };

interface Slice {
  name: string;
  get: () => Record<string, unknown>;
  set: (col: never, f: never) => void;
  clear: () => void;
  colA: string;
  colB: string;
}

const slices: Slice[] = [
  {
    name: 'riExchange',
    get: cf.getRiExchangeColumnFilters,
    set: cf.setRiExchangeColumnFilter as Slice['set'],
    clear: cf.clearAllRiExchangeColumnFilters,
    colA: 'source_ri_id',
    colB: 'reason',
  },
  {
    name: 'activeRi',
    get: cf.getActiveRiColumnFilters,
    set: cf.setActiveRiColumnFilter as Slice['set'],
    clear: cf.clearAllActiveRiColumnFilters,
    colA: 'instance_type',
    colB: 'offering_type',
  },
  {
    name: 'purchaseHistory',
    get: cf.getPurchaseHistoryColumnFilters,
    set: cf.setPurchaseHistoryColumnFilter as Slice['set'],
    clear: cf.clearAllPurchaseHistoryColumnFilters,
    colA: 'provider',
    colB: 'region',
  },
  {
    name: 'approvalQueue',
    get: cf.getApprovalQueueColumnFilters,
    set: cf.setApprovalQueueColumnFilter as Slice['set'],
    clear: cf.clearAllApprovalQueueColumnFilters,
    colA: 'provider',
    colB: 'created_by',
  },
];

beforeEach(() => {
  slices.forEach((s) => s.clear());
});

describe.each(slices)('$name column filter slice', (s) => {
  const put = (col: string, f: unknown): void => s.set(col as never, f as never);

  test('defaults to an empty object', () => {
    expect(s.get()).toEqual({});
  });

  test('set then get returns an equal value', () => {
    put(s.colA, SET);
    put(s.colB, EXPR);
    expect(s.get()).toEqual({ [s.colA]: SET, [s.colB]: EXPR });
  });

  test('getter returns a copy: mutating it does not change the stored slice', () => {
    put(s.colA, SET);
    const copy = s.get();
    expect(s.get()).not.toBe(copy);
    delete copy[s.colA];
    copy[s.colB] = EXPR;
    expect(s.get()).toEqual({ [s.colA]: SET });
  });

  test('setting an existing column replaces its filter', () => {
    put(s.colA, SET);
    put(s.colA, EXPR);
    expect(s.get()).toEqual({ [s.colA]: EXPR });
  });

  test('null removes only that column', () => {
    put(s.colA, SET);
    put(s.colB, EXPR);
    put(s.colA, null);
    expect(s.get()).toEqual({ [s.colB]: EXPR });
    expect(s.colA in s.get()).toBe(false);
  });

  test('null on an unset column is a no-op', () => {
    put(s.colA, null);
    expect(s.get()).toEqual({});
  });

  test('clearAll resets every column', () => {
    put(s.colA, SET);
    put(s.colB, EXPR);
    s.clear();
    expect(s.get()).toEqual({});
  });
});

describe('slice isolation', () => {
  test.each(slices.map((s) => [s.name, s] as const))('writing %s leaves the other slices empty', (_n, written) => {
    written.set(written.colA as never, SET as never);
    slices.forEach((other) => {
      expect(other.get()).toEqual(other === written ? { [written.colA]: SET } : {});
    });
  });

  test('clearing one slice leaves the others populated', () => {
    slices.forEach((s) => s.set(s.colA as never, SET as never));
    cf.clearAllApprovalQueueColumnFilters();
    expect(cf.getApprovalQueueColumnFilters()).toEqual({});
    expect(cf.getPurchaseHistoryColumnFilters()).toEqual({ provider: SET });
    expect(cf.getRiExchangeColumnFilters()).toEqual({ source_ri_id: SET });
    expect(cf.getActiveRiColumnFilters()).toEqual({ instance_type: SET });
  });
});

describe('../state re-exports', () => {
  test('are the same functions, sharing the same state', () => {
    expect(state.getRiExchangeColumnFilters).toBe(cf.getRiExchangeColumnFilters);
    expect(state.setActiveRiColumnFilter).toBe(cf.setActiveRiColumnFilter);
    state.setApprovalQueueColumnFilter('account', SET);
    expect(cf.getApprovalQueueColumnFilters()).toEqual({ account: SET });
  });
});
