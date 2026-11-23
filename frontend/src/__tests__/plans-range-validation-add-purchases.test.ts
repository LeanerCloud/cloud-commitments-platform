/**
 * Add Purchases modal tests for the plan range validation suite: the Number of
 * Purchases range guard, the suggested start date and the notice lead time.
 * Split from plans-range-validation.test.ts to stay under the 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import './plans-range-validation-harness';
import { openAddPurchasesModal } from '../plans';
import * as api from '../api';

// ---------------------------------------------------------------------------
// Add Purchases modal: start-date picker min attribute (QA 5.6)
// ---------------------------------------------------------------------------

describe('Add Purchases modal: suggested start date (QA 5.6)', () => {
  beforeEach(async () => {
    jest.clearAllMocks();
    const plansList = document.createElement('div');
    plansList.id = 'plans-list';
    const ppList = document.createElement('div');
    ppList.id = 'planned-purchases-list';
    document.body.replaceChildren(plansList, ppList);

    await openAddPurchasesModal('plan-xyz', 'My Test Plan', 3);
  });

  it('suggests a UTC date beyond the configured notice window', () => {
    const input = document.getElementById('add-purchases-start-date') as HTMLInputElement;
    const now = new Date();
    expect(input.value).toBe(new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + 5)).toISOString().slice(0, 10));
  });
});

// The server parses start_date as midnight UTC and requires it after now
// (handler_plans.go), so tomorrow UTC is the first accepted day. jest's
// globalSetup runs the suite in America/New_York, where the local day trails
// UTC in the evening, so a local-date implementation fails the NY case below.
describe('Add Purchases start date matches the server rule: after today UTC (#50)', () => {
  afterEach(() => jest.useRealTimers());

  async function submitWith(now: string, date: string): Promise<HTMLInputElement> {
    jest.clearAllMocks();
    document.body.replaceChildren();
    jest.useFakeTimers({ now: new Date(now) });
    (api.createPlannedPurchases as jest.Mock).mockResolvedValue({});
    (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
    await openAddPurchasesModal('plan-past', 'Past plan', 0);
    const input = document.getElementById('add-purchases-start-date') as HTMLInputElement;
    input.value = date;
    document.getElementById('add-purchases-form')?.dispatchEvent(new Event('submit', { cancelable: true }));
    await Promise.resolve();
    await Promise.resolve();
    return input;
  }

  function expectRejected(): void {
    expect(api.createPlannedPurchases).not.toHaveBeenCalled();
    const err = document.getElementById('add-purchases-error');
    expect(err?.classList.contains('hidden')).toBe(false);
    expect(err?.textContent).toMatch(/after today UTC/i);
  }

  it('rejects yesterday (UTC)', async () => {
    await submitWith('2026-10-15T12:00:00Z', '2026-10-14');
    expectRejected();
  });

  it('rejects today (UTC)', async () => {
    await submitWith('2026-10-15T12:00:00Z', '2026-10-15');
    expectRejected();
  });

  it('accepts tomorrow (UTC)', async () => {
    await submitWith('2026-10-15T12:00:00Z', '2026-10-16');
    expect(api.createPlannedPurchases).toHaveBeenCalledWith('plan-past', 1, '2026-10-16');
  });

  it('rejects the UTC day when the local day is behind it (21:30 Oct 15 in New York, UTC-4, is Oct 16 UTC)', async () => {
    const input = await submitWith('2026-10-16T01:30:00Z', '2026-10-16');
    expect(new Date().getDate()).toBe(15); // guards the premise: local day is not the UTC day
    expectRejected();
    expect(input.min).toBe('2026-10-17');
  });
});

describe('Add Purchases configured notice lead time', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    document.body.replaceChildren();
  });
  afterEach(() => jest.useRealTimers());

  it.each([
    [0, '2026-10-15T23:59:00Z', '2026-10-17'],
    [1, '2026-12-31T23:59:00Z', '2027-01-03'],
    [3, '2026-10-15T23:59:00Z', '2026-10-20'],
    [30, '2026-01-31T23:59:00Z', '2026-03-04'],
    [3, '2026-10-14T23:00:00-07:00', '2026-10-20'],
    [3, '2026-10-15T00:01:00+14:00', '2026-10-19'],
    [3, '2026-03-07T23:59:00-05:00', '2026-03-13'],
    [3, '2026-10-24T23:59:00+02:00', '2026-10-29'],
  ])('uses N=%s at %s to suggest %s', async (notice, now, expected) => {
    jest.useFakeTimers({ now: new Date(now) });
    await openAddPurchasesModal('plan-notice', 'Notice plan', notice);
    expect((document.getElementById('add-purchases-start-date') as HTMLInputElement).value).toBe(expected);
    expect(document.getElementById('add-purchases-date-help')?.textContent).toContain(`${notice}-day notification window`);
    expect(document.querySelector('button[type="submit"]')).not.toBeDisabled();
  });

  it.each([undefined, NaN, Infinity, -1, 31, 1.5])('fails visibly for invalid notice %s', async notice => {
    await openAddPurchasesModal('plan-notice', 'Notice plan', notice as number);
    expect(document.getElementById('add-purchases-error')?.textContent).toContain('missing or invalid');
    const count = document.getElementById('add-purchases-count') as HTMLInputElement;
    count.dispatchEvent(new Event('input'));
    expect(document.querySelector('button[type="submit"]')).toBeDisabled();
    document.getElementById('add-purchases-form')?.dispatchEvent(new Event('submit', { cancelable: true }));
    expect(api.createPlannedPurchases).not.toHaveBeenCalled();
  });

  it('preserves an earlier selected date, warns that it will be refused, and shows the server refusal', async () => {
    jest.useFakeTimers({ now: new Date('2026-10-15T23:59:00Z') });
    const refusal = 'start_date leaves too little notice before the first purchase; choose a start date on or after 2026-10-20';
    (api.createPlannedPurchases as jest.Mock).mockRejectedValue(new Error(refusal));
    (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
    await openAddPurchasesModal('plan-notice', 'Notice plan', 3);
    const date = document.getElementById('add-purchases-start-date') as HTMLInputElement;
    const minimum = date.min;
    date.value = '2026-10-16';
    date.dispatchEvent(new Event('input'));
    expect(date.value).toBe('2026-10-16');
    expect(date.min).toBe(minimum);
    expect(document.getElementById('add-purchases-date-warning')?.textContent).toContain('will be refused');
    expect(document.querySelector('button[type="submit"]')).not.toBeDisabled();
    document.getElementById('add-purchases-form')?.dispatchEvent(new Event('submit', { cancelable: true }));
    expect(api.createPlannedPurchases).toHaveBeenCalledWith('plan-notice', 1, '2026-10-16');
    await Promise.resolve();
    await Promise.resolve();
    expect(document.getElementById('add-purchases-error')?.textContent).toBe(refusal);
    expect(document.getElementById('add-purchases-modal')).not.toBeNull();
  });

  // N=3 at 23:59 UTC on 15 Oct: now+(N+1)d is 19 Oct 23:59, so 19 Oct is refused
  // and 20 Oct (the suggested date) is the first accepted midnight.
  it.each([
    ['2026-10-19', 'will be refused'],
    ['2026-10-20', ''],
  ])('warns for %s only when it is inside the (N+1)-day margin', async (selected, text) => {
    jest.useFakeTimers({ now: new Date('2026-10-15T23:59:00Z') });
    await openAddPurchasesModal('plan-notice', 'Notice plan', 3);
    const date = document.getElementById('add-purchases-start-date') as HTMLInputElement;
    date.value = selected;
    date.dispatchEvent(new Event('input'));
    const warning = document.getElementById('add-purchases-date-warning')?.textContent ?? '';
    if (text) expect(warning).toContain(text); else expect(warning).toBe('');
  });
});
