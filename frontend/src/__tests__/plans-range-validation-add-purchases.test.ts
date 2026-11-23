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
