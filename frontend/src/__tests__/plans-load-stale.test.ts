/**
 * #1957: loadPlans / loadPlannedPurchases must ignore a response that was
 * superseded by a newer call (filter change), so an older, slower response
 * cannot paint over fresh data or show a stale error callout.
 */
import { loadPlans } from '../plans';

jest.mock('../api', () => ({
  getPlans: jest.fn(),
  getPlannedPurchases: jest.fn(),
  listPlanAccounts: jest.fn().mockResolvedValue([]),
}));

jest.mock('../state', () => ({
  getRecommendations: jest.fn().mockReturnValue([]),
  getSelectedRecommendationIDs: jest.fn().mockReturnValue(new Set()),
  getVisibleRecommendations: jest.fn().mockReturnValue([]),
  setVisibleRecommendations: jest.fn(),
  getCurrentProvider: jest.fn().mockReturnValue(''),
  setCurrentProvider: jest.fn(),
  getCurrentAccountIDs: jest.fn().mockReturnValue([]),
  setCurrentAccountIDs: jest.fn(),
  subscribeProvider: jest.fn().mockReturnValue(() => {}),
  subscribeAccount: jest.fn().mockReturnValue(() => {}),
  getCurrentUser: jest.fn().mockReturnValue(null),
  getPlansColumnFilters: jest.fn().mockReturnValue({}),
  setPlansColumnFilter: jest.fn(),
  clearAllPlansColumnFilters: jest.fn(),
}));

jest.mock('../history', () => ({ viewPlanHistory: jest.fn() }));

import * as api from '../api';

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (v: T) => void;
  reject: (e: Error) => void;
}
const deferred = <T,>(): Deferred<T> => {
  let resolve!: (v: T) => void;
  let reject!: (e: Error) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
};
const flush = async (): Promise<void> => { for (let i = 0; i < 5; i++) await Promise.resolve(); };

const plan = (name: string) => ({
  id: `id-${name}`, name, enabled: true, auto_purchase: false,
  services: { ec2: { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'all-upfront', coverage: 80 } },
});
const purchase = (planName: string) => ({
  id: `pp-${planName}`, plan_id: 'p', plan_name: planName, scheduled_date: '2026-06-01T00:00:00Z',
  provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1,
  payment: 'all-upfront', upfront_cost: 1, estimated_savings: 1, step_number: 1, total_steps: 1, status: 'pending',
});

const plansHtml = (): string => (document.getElementById('plans-list') as HTMLElement).innerHTML;
const plannedHtml = (): string => (document.getElementById('planned-purchases-list') as HTMLElement).innerHTML;
// aria-busy is cleared by a MutationObserver (async), so check for leftover skeleton nodes instead.
const isBusy = (id: string): boolean =>
  (document.getElementById(id) as HTMLElement)
    .querySelector('.skeleton, .skeleton-group, .skeleton-tile, .skeleton-row, .skeleton-table') !== null;

describe('stale response guard (#1957)', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    const list = document.createElement('div');
    list.id = 'plans-list';
    const planned = document.createElement('div');
    planned.id = 'planned-purchases-list';
    document.body.replaceChildren(list, planned);
    (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
    jest.spyOn(console, 'error').mockImplementation(() => {});
  });

  describe('loadPlans', () => {
    it('does not let an older response paint over the newer one', async () => {
      const a = deferred<{ plans: unknown[] }>();
      const b = deferred<{ plans: unknown[] }>();
      (api.getPlans as jest.Mock).mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise);
      const first = loadPlans();
      const second = loadPlans();
      b.resolve({ plans: [plan('NEW-PLAN')] });
      await second;
      a.resolve({ plans: [plan('OLD-PLAN')] });
      await first;
      expect(plansHtml()).toContain('NEW-PLAN');
      expect(plansHtml()).not.toContain('OLD-PLAN');
      expect(isBusy('plans-list')).toBe(false);
      // The superseded call must not fall through to its own planned-purchases load.
      expect(api.getPlannedPurchases).toHaveBeenCalledTimes(1);
    });

    it('does not paint an error callout for a superseded failing call', async () => {
      const a = deferred<{ plans: unknown[] }>();
      const b = deferred<{ plans: unknown[] }>();
      (api.getPlans as jest.Mock).mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise);
      const first = loadPlans();
      const second = loadPlans();
      b.resolve({ plans: [plan('NEW-PLAN')] });
      await second;
      a.reject(new Error('boom'));
      await first;
      expect(plansHtml()).toContain('NEW-PLAN');
      expect(plansHtml()).not.toContain('Failed to load plans');
    });

    it('still renders and clears the skeleton for the latest call', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [plan('ONLY-PLAN')] });
      await loadPlans();
      expect(plansHtml()).toContain('ONLY-PLAN');
      expect(isBusy('plans-list')).toBe(false);
    });

    it('still shows the error callout when the latest call fails', async () => {
      (api.getPlans as jest.Mock).mockRejectedValue(new Error('boom'));
      await loadPlans();
      expect(plansHtml()).toContain('Failed to load plans: boom');
      expect(isBusy('plans-list')).toBe(false);
    });
  });

  describe('loadPlannedPurchases', () => {
    // Start A, let it reach the planned-purchases fetch, then start B.
    const startTwo = async (): Promise<{ first: Promise<void>; second: Promise<void>;
      a: Deferred<{ purchases: unknown[] }>; b: Deferred<{ purchases: unknown[] }> }> => {
      const a = deferred<{ purchases: unknown[] }>();
      const b = deferred<{ purchases: unknown[] }>();
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise);
      const first = loadPlans();
      await flush();
      const second = loadPlans();
      await flush();
      return { first, second, a, b };
    };

    it('does not let an older response paint over the newer one', async () => {
      const { first, second, a, b } = await startTwo();
      b.resolve({ purchases: [purchase('NEW-PP')] });
      await second;
      a.resolve({ purchases: [purchase('OLD-PP')] });
      await first;
      expect(plannedHtml()).toContain('NEW-PP');
      expect(plannedHtml()).not.toContain('OLD-PP');
      expect(isBusy('planned-purchases-list')).toBe(false);
    });

    it('does not paint an error callout for a superseded failing call', async () => {
      const { first, second, a, b } = await startTwo();
      b.resolve({ purchases: [purchase('NEW-PP')] });
      await second;
      a.reject(new Error('boom'));
      await first;
      expect(plannedHtml()).toContain('NEW-PP');
      expect(plannedHtml()).not.toContain('Failed to load planned purchases');
    });

    it('still shows the error callout when the latest call fails', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockRejectedValue(new Error('boom'));
      await loadPlans();
      expect(plannedHtml()).toContain('Failed to load planned purchases: boom');
      expect(isBusy('planned-purchases-list')).toBe(false);
    });
  });
});
