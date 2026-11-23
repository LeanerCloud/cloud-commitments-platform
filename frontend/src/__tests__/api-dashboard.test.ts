/**
 * Unit tests for API module (dashboard and recommendations)
 */
import { fetchMock } from './setup';
import { getDashboardSummary, getUpcomingPurchases } from '../api';
import type { UpcomingPurchasesResponse } from '../api';

describe('Dashboard API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({})
    });
  });

  describe('getDashboardSummary', () => {
    test('fetches summary with provider filter', async () => {
      await getDashboardSummary('aws');
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/dashboard/summary?provider=aws',
        expect.anything()
      );
    });

    test('uses all providers by default (empty provider filter)', async () => {
      await getDashboardSummary();
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/dashboard/summary?provider=',
        expect.anything()
      );
    });
  });

  describe('getUpcomingPurchases', () => {
    test('fetches upcoming purchases', async () => {
      await getUpcomingPurchases();
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/dashboard/upcoming',
        expect.anything()
      );
    });

    test('sends provider and account chips as query params (#713)', async () => {
      await getUpcomingPurchases('aws', ['a1', 'a2']);
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/dashboard/upcoming?provider=aws&account_ids=a1%2Ca2',
        expect.anything()
      );
    });

    // #1189: the endpoint returns { purchases: [...] }, not a bare array. The
    // annotation makes tsc (npm run typecheck) reject a regression to the
    // old UpcomingPurchase[] return type.
    test('resolves the { purchases } envelope the handler returns', async () => {
      const row = {
        execution_id: 'exec-1', plan_id: 'plan-1', plan_name: 'Plan', scheduled_date: '2026-11-01',
        provider: 'aws', service: 'ec2', step_number: 1, total_steps: 2, estimated_savings: 10,
        has_recommendations: true,
      };
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, headers: new Headers(), json: () => Promise.resolve({ purchases: [row] }) });
      const res: UpcomingPurchasesResponse = await getUpcomingPurchases();
      expect(Array.isArray(res)).toBe(false);
      expect(res.purchases).toEqual([row]);
    });
  });
});
